package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mundi/popquiz/internal/db"
	"github.com/mundi/popquiz/internal/handlers"
	"github.com/mundi/popquiz/internal/sse"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	adminPassword := os.Getenv("ADMIN_PASSWORD")
	adminSessionSecret := os.Getenv("ADMIN_SESSION_SECRET")
	if adminSessionSecret == "" {
		adminSessionSecret = "default-admin-secret-change-me"
	}
	playerSessionSecret := os.Getenv("PLAYER_SESSION_SECRET")
	if playerSessionSecret == "" {
		playerSessionSecret = "default-player-secret-change-me"
	}

	database, err := db.Open(dataDir)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	broker := sse.NewBroker()

	// Start background goroutine for Team Head promotion
	stopHeadPromotion := make(chan struct{})
	go headPromotionLoop(database, broker, stopHeadPromotion)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	// Video files served from data dir
	r.Handle("/static/videos/*", http.StripPrefix("/static/videos/", http.FileServer(http.Dir(dataDir+"/videos"))))

	// Handler constructors
	joinHandler := handlers.NewJoinHandler(database, broker, playerSessionSecret)
	gameHandler := handlers.NewGameHandler(database, broker, playerSessionSecret)
	adminHandler := handlers.NewAdminHandler(database, broker, adminPassword, adminSessionSecret, dataDir)

	// Public routes
	r.Get("/", joinHandler.GetJoin)
	r.Post("/join", joinHandler.PostJoin)
	r.Get("/game/{code}", gameHandler.GetGame)
	r.Get("/game/{code}/events", gameHandler.GetEvents)
	r.Post("/game/{code}/answer", gameHandler.PostAnswer)
	r.Get("/game/{code}/results", gameHandler.GetResults)

	// Admin routes
	r.Get("/admin/login", adminHandler.GetLogin)
	r.Post("/admin/login", adminHandler.PostLogin)
	r.Route("/admin", func(r chi.Router) {
		r.Use(adminHandler.AuthMiddleware)
		r.Get("/", adminHandler.GetIndex)
		r.Get("/quiz/new", adminHandler.GetQuizNew)
		r.Post("/quiz", adminHandler.PostQuiz)
		r.Get("/quiz/{id}", adminHandler.GetQuizEditor)
		r.Post("/quiz/{id}/round", adminHandler.PostRound)
		r.Delete("/round/{id}", adminHandler.DeleteRound)
		r.Post("/round/{id}/question", adminHandler.PostQuestion)
		r.Delete("/question/{id}", adminHandler.DeleteQuestion)
		r.Post("/quiz/{id}/game", adminHandler.PostCreateGame)
		r.Get("/game/{code}", adminHandler.GetGamePanel)
		r.Get("/game/{code}/events", adminHandler.GetGameEvents)
		r.Post("/game/{code}/start-round", adminHandler.PostStartRound)
		r.Post("/game/{code}/next", adminHandler.PostNextQuestion)
		r.Post("/game/{code}/end-round", adminHandler.PostEndRound)
		r.Post("/game/{code}/video-play", adminHandler.PostVideoPlay)
		r.Post("/game/{code}/show-question", adminHandler.PostShowQuestion)
		r.Post("/game/{code}/mark", adminHandler.PostMark)
		r.Post("/game/{code}/end-game", adminHandler.PostEndGame)
		r.Delete("/game/{code}/team/{id}", adminHandler.DeleteTeam)
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("PopQuiz server starting on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-done
	log.Println("Shutting down server...")
	close(stopHeadPromotion)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Server stopped.")
}
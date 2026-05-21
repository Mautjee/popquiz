package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mundi/popquiz/internal/models"
	"github.com/mundi/popquiz/internal/sse"
)

type JoinHandler struct {
	db               *sql.DB
	broker           *sse.Broker
	sessionSecret    string
	templates        *template.Template
}

func NewJoinHandler(db *sql.DB, broker *sse.Broker, sessionSecret string) *JoinHandler {
	tmpl := template.Must(template.ParseFiles(
		"templates/base.html",
		"templates/join.html",
	))
	return &JoinHandler{
		db:            db,
		broker:        broker,
		sessionSecret: sessionSecret,
		templates:     tmpl,
	}
}

func (h *JoinHandler) GetJoin(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	type pageData struct {
		Code  string
		Error string
	}
	data := pageData{Code: code}
	h.templates.ExecuteTemplate(w, "join.html", data)
}

func (h *JoinHandler) PostJoin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	teamName := strings.TrimSpace(r.FormValue("team_name"))
	playerName := strings.TrimSpace(r.FormValue("player_name"))

	type pageData struct {
		Code  string
		Error string
	}

	if code == "" || teamName == "" || playerName == "" {
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "All fields are required"})
		return
	}

	if len(code) != 6 {
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Room code must be 6 characters"})
		return
	}

	// Look up game by room code
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)

	if err == sql.ErrNoRows {
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Game not found"})
		return
	}
	if err != nil {
		log.Printf("Error looking up game: %v", err)
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "An error occurred"})
		return
	}

	if game.State == "ended" {
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Game has ended"})
		return
	}

	// Check if joining is blocked during video question
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var roundType string
		err := h.db.QueryRow(`
			SELECT r.type FROM rounds r
			JOIN questions q ON q.round_id = r.id
			WHERE q.id = ?
		`, game.CurrentQuestionID.Int64).Scan(&roundType)
		if err == nil && roundType == "video" {
			h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "A video question is in progress. Please wait for the next question."})
			return
		}
	}

	// Find or create team
	var teamID int64
	var isHead int

	err = h.db.QueryRow("SELECT id FROM teams WHERE game_id = ? AND name = ?", game.ID, teamName).Scan(&teamID)
	if err == sql.ErrNoRows {
		// Create new team — player becomes head
		result, err := h.db.Exec("INSERT INTO teams (game_id, name, score) VALUES (?, ?, 0)", game.ID, teamName)
		if err != nil {
			log.Printf("Error creating team: %v", err)
			h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "An error occurred"})
			return
		}
		teamID, _ = result.LastInsertId()
		isHead = 1
	} else if err != nil {
		log.Printf("Error looking up team: %v", err)
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "An error occurred"})
		return
	} else {
		// Join existing team as member
		isHead = 0

		// Check if team has any members (if empty, promote joiner)
		var memberCount int
		h.db.QueryRow("SELECT COUNT(*) FROM players WHERE team_id = ?", teamID).Scan(&memberCount)
		if memberCount == 0 {
			isHead = 1
		}
	}

	// Create player
	result, err := h.db.Exec(
		"INSERT INTO players (team_id, name, is_head, last_seen_at, joined_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))",
		teamID, playerName, isHead,
	)
	if err != nil {
		log.Printf("Error creating player: %v", err)
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "An error occurred"})
		return
	}
	playerID, _ := result.LastInsertId()

	// Set signed player session cookie
	cookieValue := signPlayerSession(playerID, teamID, h.sessionSecret)
	http.SetCookie(w, &http.Cookie{
		Name:     "player_session",
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400 * 7, // 7 days
		SameSite: http.SameSiteLaxMode,
	})

	// Notify via SSE
	teamNameForEvent := teamName
	isHeadStr := "0"
	if isHead == 1 {
		isHeadStr = "1"
	}
	eventData := fmt.Sprintf(`{"team_name":"%s","player_name":"%s","is_head":%s}`, teamNameForEvent, playerName, isHeadStr)
	h.broker.Publish(code, sse.Event{Type: "player_joined", Data: eventData})
	h.broker.Publish("admin:"+code, sse.Event{Type: "player_joined", Data: eventData})

	http.Redirect(w, r, "/game/"+code, http.StatusSeeOther)
}

func signPlayerSession(playerID, teamID int64, secret string) string {
	payload := fmt.Sprintf("%d:%d", playerID, teamID)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s|%s", payload, sig)
}

func parsePlayerSession(cookieValue, secret string) (playerID, teamID int64, ok bool) {
	parts := strings.SplitN(cookieValue, "|", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}

	payload := parts[0]
	sig := parts[1]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return 0, 0, false
	}

	idParts := strings.SplitN(payload, ":", 2)
	if len(idParts) != 2 {
		return 0, 0, false
	}

	pid, err := strconv.ParseInt(idParts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	tid, err := strconv.ParseInt(idParts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}

	return pid, tid, true
}

func generateRoomCode(db *sql.DB) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for {
		code := make([]byte, 6)
		rand.Read(code)
		for i := range code {
			code[i] = chars[int(code[i])%len(chars)]
		}
		roomCode := string(code)

		var exists int
		err := db.QueryRow("SELECT 1 FROM games WHERE room_code = ?", roomCode).Scan(&exists)
		if err == sql.ErrNoRows {
			return roomCode, nil
		}
		if err != nil {
			return "", err
		}
		// Code exists, try again
	}
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
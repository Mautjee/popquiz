package handlers

import (
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mundi/popquiz/internal/models"
	"github.com/mundi/popquiz/internal/scoring"
	"github.com/mundi/popquiz/internal/sse"
)

type AdminHandler struct {
	db            *sql.DB
	broker        *sse.Broker
	adminPassword string
	sessionSecret string
	dataDir       string
}

func NewAdminHandler(db *sql.DB, broker *sse.Broker, adminPassword, sessionSecret, dataDir string) *AdminHandler {
	return &AdminHandler{
		db:            db,
		broker:        broker,
		adminPassword: adminPassword,
		sessionSecret: sessionSecret,
		dataDir:       dataDir,
	}
}

// render parses base.html + the given page files fresh each call, preventing
// {{define "content"}} conflicts when multiple pages share the same template set.
func (h *AdminHandler) render(w http.ResponseWriter, data interface{}, name string, files ...string) {
	allFiles := append([]string{"templates/base.html"}, files...)
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).ParseFiles(allFiles...))
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render error (%s): %v", name, err)
	}
}

// renderPartial parses partial templates without base.html.
func (h *AdminHandler) renderPartial(w http.ResponseWriter, data interface{}, name string, files ...string) {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).ParseFiles(files...))
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("renderPartial error (%s): %v", name, err)
	}
}

// --- Auth ---

func (h *AdminHandler) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If no admin password set, allow access (dev mode)
		if h.adminPassword == "" {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("admin_session")
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// Validate token in DB
		var expiresAt string
		err = h.db.QueryRow("SELECT expires_at FROM admin_sessions WHERE token = ?", cookie.Value).Scan(&expiresAt)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		expires, err := time.Parse("2006-01-02 15:04:05", expiresAt)
		if err != nil || time.Now().After(expires) {
			// Session expired
			h.db.Exec("DELETE FROM admin_sessions WHERE token = ?", cookie.Value)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *AdminHandler) GetLogin(w http.ResponseWriter, r *http.Request) {
	// If no admin password, redirect directly
	if h.adminPassword == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	type pageData struct {
		Error string
	}
	h.render(w, pageData{}, "login.html", "templates/admin/login.html")
}

func (h *AdminHandler) PostLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	password := r.FormValue("password")

	if !hmac.Equal([]byte(password), []byte(h.adminPassword)) {
		type pageData struct {
			Error string
		}
		h.render(w, pageData{Error: "Incorrect password"}, "login.html", "templates/admin/login.html")
		return
	}

	// Create session
	token := generateToken()
	expiresAt := time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := h.db.Exec("INSERT INTO admin_sessions (token, created_at, expires_at) VALUES (?, datetime('now'), ?)", token, expiresAt)
	if err != nil {
		log.Printf("Error creating admin session: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		MaxAge:   86400,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// --- Admin Index ---

func (h *AdminHandler) GetIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, title, created_at FROM quizzes ORDER BY created_at DESC")
	if err != nil {
		log.Printf("Error loading quizzes: %v", err)
		http.Error(w, "Error loading quizzes", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var quizzes []models.Quiz
	for rows.Next() {
		var q models.Quiz
		rows.Scan(&q.ID, &q.Title, &q.CreatedAt)
		quizzes = append(quizzes, q)
	}

	data := map[string]interface{}{
		"Quizzes": quizzes,
	}
	h.render(w, data, "index.html", "templates/admin/index.html")
}

// --- Quiz CRUD ---

func (h *AdminHandler) GetQuizNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, map[string]interface{}{
		"Quiz":    nil,
		"Rounds":  nil,
		"IsNew":   true,
		"Game":    nil,
	}, "quiz_editor.html", "templates/admin/quiz_editor.html")
}

func (h *AdminHandler) PostQuiz(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "Title is required", http.StatusUnprocessableEntity)
		return
	}

	result, err := h.db.Exec("INSERT INTO quizzes (title) VALUES (?)", title)
	if err != nil {
		log.Printf("Error creating quiz: %v", err)
		http.Error(w, "Error creating quiz", http.StatusInternalServerError)
		return
	}

	quizID, _ := result.LastInsertId()
	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

func (h *AdminHandler) GetQuizEditor(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	var quiz models.Quiz
	err = h.db.QueryRow("SELECT id, title, created_at FROM quizzes WHERE id = ?", quizID).Scan(&quiz.ID, &quiz.Title, &quiz.CreatedAt)
	if err != nil {
		http.Error(w, "Quiz not found", http.StatusNotFound)
		return
	}

	// Load rounds
	rows, err := h.db.Query(`
		SELECT id, quiz_id, name, type, order_index FROM rounds
		WHERE quiz_id = ? ORDER BY order_index
	`, quizID)
	if err != nil {
		log.Printf("Error loading rounds: %v", err)
	}
	var rounds []models.Round
	if err == nil {
		for rows.Next() {
			var r models.Round
			rows.Scan(&r.ID, &r.QuizID, &r.Name, &r.Type, &r.OrderIndex)
			rounds = append(rounds, r)
		}
		rows.Close()
	}

	// Load questions for each round
	type RoundWithQuestions struct {
		Round     models.Round
		Questions []models.Question
	}
	var roundsWithQuestions []RoundWithQuestions
	for _, rd := range rounds {
		qRows, err := h.db.Query(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
			FROM questions WHERE round_id = ? ORDER BY order_index
		`, rd.ID)
		if err != nil {
			roundsWithQuestions = append(roundsWithQuestions, RoundWithQuestions{Round: rd})
			continue
		}
		var questions []models.Question
		for qRows.Next() {
			var q models.Question
			qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.Points, &q.OrderIndex)
			questions = append(questions, q)
		}
		qRows.Close()
		roundsWithQuestions = append(roundsWithQuestions, RoundWithQuestions{Round: rd, Questions: questions})
	}

	// Check if a game already exists for this quiz
	var game models.Game
	gameErr := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE quiz_id = ? ORDER BY created_at DESC LIMIT 1
	`, quizID).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State, &game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)

	data := map[string]interface{}{
		"Quiz":                quiz,
		"RoundsWithQuestions": roundsWithQuestions,
		"IsNew":               false,
		"Game":                nil,
	}
	if gameErr == nil {
		data["Game"] = game
	}

	h.render(w, data, "quiz_editor.html", "templates/admin/quiz_editor.html")
}

func (h *AdminHandler) PostRound(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	roundType := r.FormValue("type")
	if name == "" || (roundType != "text" && roundType != "video") {
		http.Error(w, "Name and valid type required", http.StatusUnprocessableEntity)
		return
	}

	// Get next order index
	var maxOrder sql.NullInt64
	h.db.QueryRow("SELECT MAX(order_index) FROM rounds WHERE quiz_id = ?", quizID).Scan(&maxOrder)
	orderIndex := 0
	if maxOrder.Valid {
		orderIndex = int(maxOrder.Int64) + 1
	}

	_, err = h.db.Exec("INSERT INTO rounds (quiz_id, name, type, order_index) VALUES (?, ?, ?, ?)",
		quizID, name, roundType, orderIndex)
	if err != nil {
		log.Printf("Error creating round: %v", err)
		http.Error(w, "Error creating round", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

func (h *AdminHandler) DeleteRound(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}

	// Get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	_, err = h.db.Exec("DELETE FROM rounds WHERE id = ?", roundID)
	if err != nil {
		log.Printf("Error deleting round: %v", err)
		http.Error(w, "Error deleting round", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

func (h *AdminHandler) PostQuestion(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}

	// Multipart form for video upload
	if err := r.ParseMultipartForm(50 << 20); err != nil { // 50MB max
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	questionText := strings.TrimSpace(r.FormValue("question_text"))
	questionType := r.FormValue("question_type")
	correctAnswer := strings.TrimSpace(r.FormValue("correct_answer"))
	pointsStr := r.FormValue("points")
	options := r.FormValue("options")

	points := 1
	if pointsStr != "" {
		if p, err := strconv.Atoi(pointsStr); err == nil && p > 0 {
			points = p
		}
	}

	if questionText == "" || correctAnswer == "" {
		http.Error(w, "Question text and correct answer required", http.StatusUnprocessableEntity)
		return
	}

	// Get next order index
	var maxOrder sql.NullInt64
	h.db.QueryRow("SELECT MAX(order_index) FROM questions WHERE round_id = ?", roundID).Scan(&maxOrder)
	orderIndex := 0
	if maxOrder.Valid {
		orderIndex = int(maxOrder.Int64) + 1
	}

	// Handle video upload
	var videoFilename sql.NullString
	file, header, err := r.FormFile("video_file")
	if err == nil {
		defer file.Close()

		// Generate unique filename
		ext := filepath.Ext(header.Filename)
		if ext == "" {
			ext = ".mp4"
		}
		filename := generateToken() + ext
		videoPath := filepath.Join(h.dataDir, "videos", filename)

		dst, err := os.Create(videoPath)
		if err != nil {
			log.Printf("Error creating video file: %v", err)
			http.Error(w, "Error saving video", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			log.Printf("Error writing video file: %v", err)
			http.Error(w, "Error saving video", http.StatusInternalServerError)
			return
		}

		videoFilename = sql.NullString{String: filename, Valid: true}
	}

	// Handle options JSON for MC
	var optionsJSON sql.NullString
	if questionType == "multiple_choice" && options != "" {
		optionsJSON = sql.NullString{String: options, Valid: true}
	}

	_, err = h.db.Exec(`
		INSERT INTO questions (round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, roundID, questionText, questionType, correctAnswer, optionsJSON, videoFilename, points, orderIndex)

	// Get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

func (h *AdminHandler) DeleteQuestion(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	// Delete video file if exists
	var videoFilename sql.NullString
	h.db.QueryRow("SELECT video_filename FROM questions WHERE id = ?", questionID).Scan(&videoFilename)
	if videoFilename.Valid && videoFilename.String != "" {
		videoPath := filepath.Join(h.dataDir, "videos", videoFilename.String)
		os.Remove(videoPath) // Ignore error
	}

	// Get round_id -> quiz_id for redirect
	var roundID int64
	h.db.QueryRow("SELECT round_id FROM questions WHERE id = ?", questionID).Scan(&roundID)
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	_, err = h.db.Exec("DELETE FROM questions WHERE id = ?", questionID)
	if err != nil {
		log.Printf("Error deleting question: %v", err)
		http.Error(w, "Error deleting question", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

func (h *AdminHandler) PostCreateGame(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	roomCode, err := generateRoomCode(h.db)
	if err != nil {
		log.Printf("Error generating room code: %v", err)
		http.Error(w, "Error creating game", http.StatusInternalServerError)
		return
	}

	result, err := h.db.Exec("INSERT INTO games (quiz_id, room_code) VALUES (?, ?)", quizID, roomCode)
	if err != nil {
		log.Printf("Error creating game: %v", err)
		http.Error(w, "Error creating game", http.StatusInternalServerError)
		return
	}

	_ = result
	http.Redirect(w, r, fmt.Sprintf("/admin/game/%s", roomCode), http.StatusSeeOther)
}

// --- Game Panel ---

// buildGameData loads all data needed for the game panel template.
func (h *AdminHandler) buildGameData(code string) (map[string]interface{}, error) {
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)
	if err != nil {
		return nil, err
	}

	// Load quiz
	var quiz models.Quiz
	h.db.QueryRow("SELECT id, title, created_at FROM quizzes WHERE id = ?", game.QuizID).Scan(&quiz.ID, &quiz.Title, &quiz.CreatedAt)

	data := map[string]interface{}{
		"Game": game,
		"Quiz": quiz,
		"Code": code,
	}

	// Load teams
	tRows, err := h.db.Query("SELECT id, game_id, name, score FROM teams WHERE game_id = ? ORDER BY name", game.ID)
	if err != nil {
		return nil, err
	}
	var teams []models.Team
	for tRows.Next() {
		var t models.Team
		tRows.Scan(&t.ID, &t.GameID, &t.Name, &t.Score)
		teams = append(teams, t)
	}
	tRows.Close()
	if teams == nil {
		teams = []models.Team{}
	}
	data["Teams"] = teams

	// Load current question info if in question state
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var q models.Question
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
			FROM questions WHERE id = ?
		`, game.CurrentQuestionID.Int64).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
			&q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.Points, &q.OrderIndex)
		if err == nil {
			data["CurrentQuestion"] = q
			// Count answers for this question
			var answeredCount, totalTeams int
			h.db.QueryRow("SELECT COUNT(DISTINCT team_id) FROM answers WHERE question_id = ?", q.ID).Scan(&answeredCount)
			h.db.QueryRow("SELECT COUNT(*) FROM teams WHERE game_id = ?", game.ID).Scan(&totalTeams)
			data["AnsweredCount"] = answeredCount
			data["TotalTeams"] = totalTeams
		}
	}

	// Load rounds with questions
	rows, err := h.db.Query(`
		SELECT id, quiz_id, name, type, order_index FROM rounds
		WHERE quiz_id = ? ORDER BY order_index
	`, game.QuizID)
	if err != nil {
		return data, nil
	}
	var rounds []models.Round
	for rows.Next() {
		var rd models.Round
		rows.Scan(&rd.ID, &rd.QuizID, &rd.Name, &rd.Type, &rd.OrderIndex)
		rounds = append(rounds, rd)
	}
	rows.Close()

	type RoundWithQuestions struct {
		Round     models.Round
		Questions []models.Question
	}
	var rwq []RoundWithQuestions
	for _, rd := range rounds {
		qRows, err := h.db.Query(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
			FROM questions WHERE round_id = ? ORDER BY order_index
		`, rd.ID)
		if err != nil {
			rwq = append(rwq, RoundWithQuestions{Round: rd})
			continue
		}
		var questions []models.Question
		for qRows.Next() {
			var q models.Question
			qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.Points, &q.OrderIndex)
			questions = append(questions, q)
		}
		qRows.Close()
		rwq = append(rwq, RoundWithQuestions{Round: rd, Questions: questions})
	}
	data["RoundsWithQuestions"] = rwq

	return data, nil
}

func (h *AdminHandler) GetGamePanel(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	data, err := h.buildGameData(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	h.render(w, data, "game_panel.html", "templates/admin/game_panel.html", "templates/admin/partials/game_panel_game_state.html", "templates/admin/partials/game_panel_teams.html")
}

// GetGamePanelPartial returns just the game state panel fragment for HTMX updates.
func (h *AdminHandler) GetGamePanelPartial(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	data, err := h.buildGameData(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "game_panel_game_state", "templates/admin/partials/game_panel_game_state.html")
}

// GetAdminTeamsList returns just the teams list fragment for HTMX updates.
func (h *AdminHandler) GetAdminTeamsList(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	data, err := h.buildGameData(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "game_panel_teams", "templates/admin/partials/game_panel_teams.html")
}

func (h *AdminHandler) GetGameEvents(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	h.broker.ServeHTTP("admin:"+code, w, r)
}

// --- Game State Transitions ---

func (h *AdminHandler) getGame(code string) (*models.Game, error) {
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &game, nil
}

// renderGamePanelPartial renders and returns the game state panel HTML fragment.
func (h *AdminHandler) renderGamePanelPartial(w http.ResponseWriter, code string) {
	data, err := h.buildGameData(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "game_panel_game_state", "templates/admin/partials/game_panel_game_state.html")
}

func (h *AdminHandler) PostStartRound(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "lobby" && game.State != "round_reveal" {
		http.Error(w, fmt.Sprintf("Cannot start round from state %s", game.State), http.StatusUnprocessableEntity)
		return
	}

	// Determine which round to start
	var nextRoundOrder int
	if game.State == "lobby" {
		nextRoundOrder = 0
	} else {
		// After round_reveal, start the next round
		var currentOrder int
		h.db.QueryRow("SELECT order_index FROM rounds WHERE id = ?", game.CurrentRoundID.Int64).Scan(&currentOrder)
		nextRoundOrder = currentOrder + 1
	}

	// Find the round
	var round models.Round
	err = h.db.QueryRow(`
		SELECT id, quiz_id, name, type, order_index FROM rounds
		WHERE quiz_id = ? AND order_index = ?
	`, game.QuizID, nextRoundOrder).Scan(&round.ID, &round.QuizID, &round.Name, &round.Type, &round.OrderIndex)

	if err == sql.ErrNoRows {
		// No more rounds — host should end game instead
		http.Error(w, "No more rounds available", http.StatusUnprocessableEntity)
		return
	}

	// Find the first question in this round
	var firstQuestion models.Question
	err = h.db.QueryRow(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
		FROM questions WHERE round_id = ? ORDER BY order_index LIMIT 1
	`, round.ID).Scan(&firstQuestion.ID, &firstQuestion.RoundID, &firstQuestion.QuestionText,
		&firstQuestion.QuestionType, &firstQuestion.CorrectAnswer, &firstQuestion.Options,
		&firstQuestion.VideoFilename, &firstQuestion.Points, &firstQuestion.OrderIndex)

	if err != nil {
		http.Error(w, "No questions in this round", http.StatusUnprocessableEntity)
		return
	}

	// Update game state
	_, err = h.db.Exec(`
		UPDATE games SET state = 'question', current_question_id = ?, current_round_id = ?
		WHERE id = ?
	`, firstQuestion.ID, round.ID, game.ID)
	if err != nil {
		http.Error(w, "Error updating game state", http.StatusInternalServerError)
		return
	}

	// Publish state_change event
	eventData := buildStateChangeEventData(h.db, game.ID, "question", &firstQuestion, &round)
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: eventData})

	// Return updated game state panel
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostNextQuestion(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "question" {
		http.Error(w, fmt.Sprintf("Cannot advance question from state %s", game.State), http.StatusUnprocessableEntity)
		return
	}

	// Find next question in the current round
	var nextQuestion models.Question
	err = h.db.QueryRow(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
		FROM questions WHERE round_id = ? AND order_index > (
			SELECT order_index FROM questions WHERE id = ?
		) ORDER BY order_index LIMIT 1
	`, game.CurrentRoundID.Int64, game.CurrentQuestionID.Int64).Scan(
		&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
		&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
		&nextQuestion.VideoFilename, &nextQuestion.Points, &nextQuestion.OrderIndex)

	if err == sql.ErrNoRows {
		// No more questions in this round — host should end round instead
		http.Error(w, "No more questions in this round", http.StatusUnprocessableEntity)
		return
	}

	// Update game state
	_, err = h.db.Exec(`
		UPDATE games SET current_question_id = ? WHERE id = ?
	`, nextQuestion.ID, game.ID)
	if err != nil {
		http.Error(w, "Error updating game state", http.StatusInternalServerError)
		return
	}

	// Publish state_change event
	var round models.Round
	h.db.QueryRow("SELECT id, quiz_id, name, type, order_index FROM rounds WHERE id = ?", game.CurrentRoundID.Int64).
		Scan(&round.ID, &round.QuizID, &round.Name, &round.Type, &round.OrderIndex)

	eventData := buildStateChangeEventData(h.db, game.ID, "question", &nextQuestion, &round)
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: eventData})

	// Return updated game state panel
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostEndRound(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "question" {
		http.Error(w, fmt.Sprintf("Cannot end round from state %s", game.State), http.StatusUnprocessableEntity)
		return
	}

	// Auto-score ranged and multiple choice questions for this round
	autoScoreRound(h.db, h.broker, game)

	// Update game state to round_reveal
	_, err = h.db.Exec("UPDATE games SET state = 'round_reveal' WHERE id = ?", game.ID)
	if err != nil {
		http.Error(w, "Error updating game state", http.StatusInternalServerError)
		return
	}

	// Publish state_change event
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: `{"state":"round_reveal"}`})

	// Publish round_reveal with all questions and answers
	publishRoundReveal(h.db, h.broker, game)

	// Return updated game state panel
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostVideoPlay(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "question" || !game.CurrentQuestionID.Valid {
		http.Error(w, "Not in question state", http.StatusUnprocessableEntity)
		return
	}

	// Verify it's a video question
	var roundType string
	err = h.db.QueryRow(`
		SELECT r.type FROM rounds r
		JOIN questions q ON q.round_id = r.id
		WHERE q.id = ?
	`, game.CurrentQuestionID.Int64).Scan(&roundType)
	if err != nil || roundType != "video" {
		http.Error(w, "Current question is not a video question", http.StatusUnprocessableEntity)
		return
	}

	h.broker.Publish(code, sse.Event{
		Type: "video_play",
		Data: fmt.Sprintf(`{"question_id":%d}`, game.CurrentQuestionID.Int64),
	})

	// Return updated game state panel (no state change, but refreshes UI)
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostShowQuestion(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "question" || !game.CurrentQuestionID.Valid {
		http.Error(w, "Not in question state", http.StatusUnprocessableEntity)
		return
	}

	h.broker.Publish(code, sse.Event{
		Type: "show_question",
		Data: fmt.Sprintf(`{"question_id":%d}`, game.CurrentQuestionID.Int64),
	})

	// Return updated game state panel (no state change, but refreshes UI)
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostMark(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "round_reveal" {
		http.Error(w, "Game is not in round_reveal state", http.StatusUnprocessableEntity)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	answerID, err := strconv.ParseInt(r.FormValue("answer_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid answer ID", http.StatusBadRequest)
		return
	}

	isCorrect, err := strconv.Atoi(r.FormValue("is_correct"))
	if err != nil || (isCorrect != 0 && isCorrect != 1) {
		http.Error(w, "is_correct must be 0 or 1", http.StatusBadRequest)
		return
	}

	// Mark the answer
	_, err = h.db.Exec("UPDATE answers SET is_correct = ?, scored_at = datetime('now') WHERE id = ?", isCorrect, answerID)
	if err != nil {
		http.Error(w, "Error marking answer", http.StatusInternalServerError)
		return
	}

	// Recalculate team scores and publish score_update
	publishScoreUpdate(h.db, h.broker, game)

	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) PostEndGame(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Auto-score any remaining unmarked open answers as 0
	_, err = h.db.Exec(`
		UPDATE answers SET is_correct = 0, scored_at = datetime('now')
		WHERE is_correct IS NULL AND question_id IN (
			SELECT q.id FROM questions q
			JOIN rounds r ON q.round_id = r.id
			WHERE r.quiz_id = ?
		)
	`, game.QuizID)
	if err != nil {
		log.Printf("Warning: error auto-scoring unmarked answers: %v", err)
	}

	// Recalculate all team scores one final time
	recalculateAllScores(h.db, game.ID)

	_, err = h.db.Exec("UPDATE games SET state = 'ended' WHERE id = ?", game.ID)
	if err != nil {
		http.Error(w, "Error ending game", http.StatusInternalServerError)
		return
	}

	h.broker.Publish(code, sse.Event{Type: "game_ended", Data: `{}`})

	// Return updated game state panel
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	teamID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}

	// Delete team (cascading will remove players and answers)
	_, err = h.db.Exec("DELETE FROM teams WHERE id = ?", teamID)
	if err != nil {
		log.Printf("Error deleting team: %v", err)
		http.Error(w, "Error deleting team", http.StatusInternalServerError)
		return
	}

	// Notify admin
	h.broker.Publish("admin:"+code, sse.Event{Type: "team_removed", Data: fmt.Sprintf(`{"team_id":%d}`, teamID)})
	// Note: ideally, we'd notify the specific players, but we no longer have their IDs.
	// The SSE disconnect and game page redirect will handle this.

	// Return 200 OK with empty body for HTMX to remove the row
	w.WriteHeader(http.StatusOK)
}

// --- Helper Functions ---

func autoScoreRound(db *sql.DB, broker *sse.Broker, game *models.Game) {
	// Get all questions for the current round
	rows, err := db.Query(`
		SELECT id, question_type, correct_answer, points FROM questions
		WHERE round_id = ? ORDER BY order_index
	`, game.CurrentRoundID.Int64)
	if err != nil {
		log.Printf("Error loading questions for scoring: %v", err)
		return
	}
	defer rows.Close()

	type questionInfo struct {
		ID            int64
		QuestionType  string
		CorrectAnswer string
		Points        int
	}
	var questions []questionInfo
	for rows.Next() {
		var q questionInfo
		rows.Scan(&q.ID, &q.QuestionType, &q.CorrectAnswer, &q.Points)
		questions = append(questions, q)
	}

	for _, q := range questions {
		switch q.QuestionType {
		case "ranged":
			// Get all team answers for this question
			answerRows, err := db.Query("SELECT team_id, answer_text FROM answers WHERE question_id = ?", q.ID)
			if err != nil {
				continue
			}
			teamAnswers := make(map[int64]string)
			for answerRows.Next() {
				var teamID int64
				var answerText string
				answerRows.Scan(&teamID, &answerText)
				teamAnswers[teamID] = answerText
			}
			answerRows.Close()

			results := scoring.ScoreRanged(q.CorrectAnswer, teamAnswers)
			tx, _ := db.Begin()
			for teamID, isCorrect := range results {
				isCorrectInt := 0
				if isCorrect {
					isCorrectInt = 1
				}
				tx.Exec("UPDATE answers SET is_correct = ?, scored_at = datetime('now') WHERE team_id = ? AND question_id = ?", isCorrectInt, teamID, q.ID)
			}
			tx.Commit()

		case "multiple_choice":
			answerRows, err := db.Query("SELECT team_id, answer_text FROM answers WHERE question_id = ?", q.ID)
			if err != nil {
				continue
			}
			teamAnswers := make(map[int64]string)
			for answerRows.Next() {
				var teamID int64
				var answerText string
				answerRows.Scan(&teamID, &answerText)
				teamAnswers[teamID] = answerText
			}
			answerRows.Close()

			results := scoring.ScoreMultipleChoice(q.CorrectAnswer, teamAnswers)
			tx, _ := db.Begin()
			for teamID, isCorrect := range results {
				isCorrectInt := 0
				if isCorrect {
					isCorrectInt = 1
				}
				tx.Exec("UPDATE answers SET is_correct = ?, scored_at = datetime('now') WHERE team_id = ? AND question_id = ?", isCorrectInt, teamID, q.ID)
			}
			tx.Commit()

		// Open questions are NOT auto-scored — host marks them manually
		}
	}

	// Update team scores
	recalculateAllScores(db, game.ID)
}

func recalculateAllScores(db *sql.DB, gameID int64) {
	// For each team, sum up all points from correctly answered questions
	rows, err := db.Query("SELECT id FROM teams WHERE game_id = ?", gameID)
	if err != nil {
		log.Printf("Error getting teams for score calc: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var teamID int64
		rows.Scan(&teamID)

		var totalScore int
		err := db.QueryRow(`
			SELECT COALESCE(SUM(q.points), 0) FROM answers a
			JOIN questions q ON a.question_id = q.id
			WHERE a.team_id = ? AND a.is_correct = 1
		`, teamID).Scan(&totalScore)
		if err != nil {
			log.Printf("Error calculating score for team %d: %v", teamID, err)
			continue
		}

		db.Exec("UPDATE teams SET score = ? WHERE id = ?", totalScore, teamID)
	}
}

func publishScoreUpdate(db *sql.DB, broker *sse.Broker, game *models.Game) {
	recalculateAllScores(db, game.ID)

	// Load all teams and their scores
	rows, err := db.Query("SELECT name, score FROM teams WHERE game_id = ? ORDER BY score DESC", game.ID)
	if err != nil {
		return
	}
	defer rows.Close()

	type teamScore struct {
		Name  string
		Score int
	}
	var scores []teamScore
	for rows.Next() {
		var ts teamScore
		rows.Scan(&ts.Name, &ts.Score)
		scores = append(scores, ts)
	}

	// Build JSON
	scoreJSON, _ := json.Marshal(scores)
	broker.Publish(game.RoomCode, sse.Event{Type: "score_update", Data: string(scoreJSON)})
	broker.Publish("admin:"+game.RoomCode, sse.Event{Type: "score_update", Data: string(scoreJSON)})
}

func publishRoundReveal(db *sql.DB, broker *sse.Broker, game *models.Game) {
	// Load all questions for the round
	rows, err := db.Query(`
		SELECT id, question_text, question_type, correct_answer, options, points
		FROM questions WHERE round_id = ? ORDER BY order_index
	`, game.CurrentRoundID.Int64)
	if err != nil {
		log.Printf("Error loading questions for round reveal: %v", err)
		return
	}
	defer rows.Close()

	type questionReveal struct {
		ID             int64  `json:"id"`
		QuestionText   string `json:"question_text"`
		QuestionType   string `json:"question_type"`
		CorrectAnswer  string `json:"correct_answer"`
		Options        string `json:"options"`
		Points          int    `json:"points"`
	}

	var questions []questionReveal
	for rows.Next() {
		var q questionReveal
		rows.Scan(&q.ID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.Points)
		questions = append(questions, q)
	}

	// For each question, load all team answers
	type teamAnswer struct {
		TeamName  string `json:"team_name"`
		Answer    string `json:"answer"`
		IsCorrect *int   `json:"is_correct"`
	}

	type questionWithAnswers struct {
		Question     questionReveal `json:"question"`
		TeamAnswers  []teamAnswer   `json:"team_answers"`
	}

	var revealData []questionWithAnswers
	for _, q := range questions {
		answerRows, err := db.Query(`
			SELECT t.name, a.answer_text, a.is_correct
			FROM answers a
			JOIN teams t ON a.team_id = t.id
			WHERE a.question_id = ?
		`, q.ID)
		if err != nil {
			revealData = append(revealData, questionWithAnswers{Question: q})
			continue
		}

		var answers []teamAnswer
		for answerRows.Next() {
			var ta teamAnswer
			var isCorrect sql.NullInt64
			answerRows.Scan(&ta.TeamName, &ta.Answer, &isCorrect)
			if isCorrect.Valid {
				v := int(isCorrect.Int64)
				ta.IsCorrect = &v
			}
			answers = append(answers, ta)
		}
		answerRows.Close()

		revealData = append(revealData, questionWithAnswers{Question: q, TeamAnswers: answers})
	}

	revealJSON, _ := json.Marshal(revealData)
	broker.Publish(game.RoomCode, sse.Event{Type: "round_reveal", Data: string(revealJSON)})
}

func buildStateChangeEventData(db *sql.DB, gameID int64, state string, question *models.Question, round *models.Round) string {
	data := map[string]interface{}{
		"state": state,
	}

	if question != nil {
		qData := map[string]interface{}{
			"id":             question.ID,
			"text":           question.QuestionText,
			"type":           question.QuestionType,
			"points":         question.Points,
		}
		if question.Options.Valid {
			qData["options"] = question.Options.String
		}
		if question.VideoFilename.Valid {
			qData["video_filename"] = question.VideoFilename.String
		}
		data["current_question"] = qData
	}

	if round != nil {
		rData := map[string]interface{}{
			"name": round.Name,
			"type": round.Type,
		}
		data["round"] = rData
	}

	jsonBytes, _ := json.Marshal(data)
	return string(jsonBytes)
}
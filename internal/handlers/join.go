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

	"github.com/go-chi/chi/v5"
	"github.com/mundi/popquiz/internal/models"
	"github.com/mundi/popquiz/internal/sse"
)

type JoinHandler struct {
	db            *sql.DB
	broker        *sse.Broker
	sessionSecret string
	templates     *template.Template
}

func NewJoinHandler(db *sql.DB, broker *sse.Broker, sessionSecret string) *JoinHandler {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"ne": func(a, b interface{}) bool { return a != b },
	}).ParseFiles(
		"templates/base.html",
		"templates/join.html",
		"templates/team_select.html",
	))
	return &JoinHandler{
		db:            db,
		broker:        broker,
		sessionSecret: sessionSecret,
		templates:     tmpl,
	}
}

// GetJoin renders the room code entry page (step 1).
func (h *JoinHandler) GetJoin(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	type pageData struct {
		Code  string
		Error string
	}
	data := pageData{Code: code}
	h.templates.ExecuteTemplate(w, "join.html", data)
}

// PostJoinRoom handles the room code form submission, redirects to team selection.
func (h *JoinHandler) PostJoinRoom(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))

	if code == "" || len(code) != 6 {
		type pageData struct {
			Code  string
			Error string
		}
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Room code must be 6 characters"})
		return
	}

	// Look up game by room code
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)

	if err == sql.ErrNoRows {
		type pageData struct {
			Code  string
			Error string
		}
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Game not found"})
		return
	}
	if err != nil {
		log.Printf("Error looking up game: %v", err)
		type pageData struct {
			Code  string
			Error string
		}
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "An error occurred"})
		return
	}

	if game.State == "ended" {
		type pageData struct {
			Code  string
			Error string
		}
		h.templates.ExecuteTemplate(w, "join.html", pageData{Code: code, Error: "Game has ended"})
		return
	}

	// Redirect to team selection
	http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
}

// TeamWithCount holds a team plus its player count for display.
type TeamWithCount struct {
	ID          int64
	Name        string
	PlayerCount int
}

// GetTeamSelect renders the team selection page (step 2).
func (h *JoinHandler) GetTeamSelect(w http.ResponseWriter, r *http.Request) {
	code := chiURLParam(r, "code")

	// Look up game
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)

	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}
	if err != nil {
		log.Printf("Error looking up game: %v", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if game.State == "ended" {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Load teams for this game
	rows, err := h.db.Query(`
		SELECT t.id, t.name, COUNT(p.id) as player_count
		FROM teams t
		LEFT JOIN players p ON p.team_id = t.id
		WHERE t.game_id = ?
		GROUP BY t.id
		ORDER BY t.name
	`, game.ID)
	if err != nil {
		log.Printf("Error loading teams: %v", err)
		http.Error(w, "Error loading teams", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var teams []TeamWithCount
	for rows.Next() {
		var t TeamWithCount
		rows.Scan(&t.ID, &t.Name, &t.PlayerCount)
		teams = append(teams, t)
	}

	type pageData struct {
		Code  string
		Teams []TeamWithCount
		Error string
	}

	data := pageData{Code: code, Teams: teams}
	h.templates.ExecuteTemplate(w, "team_select.html", data)
}

// PostJoinTeam handles joining an existing team.
func (h *JoinHandler) PostJoinTeam(w http.ResponseWriter, r *http.Request) {
	code := chiURLParam(r, "code")
	teamIDStr := chiURLParam(r, "teamId")
	teamID, err := strconv.ParseInt(teamIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}

	// Verify game exists and is not ended
	var game models.Game
	err = h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if game.State == "ended" {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Verify team belongs to this game
	var teamGameID int64
	err = h.db.QueryRow("SELECT game_id FROM teams WHERE id = ?", teamID).Scan(&teamGameID)
	if err != nil || teamGameID != game.ID {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	// Check if team already has members — first joiner becomes head
	isHead := 0
	var memberCount int
	h.db.QueryRow("SELECT COUNT(*) FROM players WHERE team_id = ?", teamID).Scan(&memberCount)
	if memberCount == 0 {
		isHead = 1
	}

	// Create player (auto-named)
	playerName := ""
	result, err := h.db.Exec(
		"INSERT INTO players (team_id, name, is_head, last_seen_at, joined_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))",
		teamID, playerName, isHead,
	)
	if err != nil {
		log.Printf("Error creating player: %v", err)
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
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
	teamNameForEvent := ""
	h.db.QueryRow("SELECT name FROM teams WHERE id = ?", teamID).Scan(&teamNameForEvent)
	isHeadStr := "0"
	if isHead == 1 {
		isHeadStr = "1"
	}
	eventData := fmt.Sprintf(`{"team_name":"%s","is_head":%s}`, teamNameForEvent, isHeadStr)
	h.broker.Publish(code, sse.Event{Type: "player_joined", Data: eventData})
	h.broker.Publish("admin:"+code, sse.Event{Type: "player_joined", Data: eventData})

	http.Redirect(w, r, "/game/"+code, http.StatusSeeOther)
}

// PostCreateTeam handles creating a new team and joining it.
func (h *JoinHandler) PostCreateTeam(w http.ResponseWriter, r *http.Request) {
	code := chiURLParam(r, "code")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	teamName := strings.TrimSpace(r.FormValue("team_name"))
	if teamName == "" {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	// Verify game exists and is not ended
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if game.State == "ended" {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Check if team name already exists for this game
	var existingID int64
	err = h.db.QueryRow("SELECT id FROM teams WHERE game_id = ? AND name = ?", game.ID, teamName).Scan(&existingID)
	if err == nil {
		// Team name already taken — redirect back to team select with error
		// Just redirect; the user can see the team and join it
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	// Create new team — player becomes head
	result, err := h.db.Exec("INSERT INTO teams (game_id, name, score) VALUES (?, ?, 0)", game.ID, teamName)
	if err != nil {
		log.Printf("Error creating team: %v", err)
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}
	teamID, _ := result.LastInsertId()

	// Create player as head of new team
	playerName := ""
	playerResult, err := h.db.Exec(
		"INSERT INTO players (team_id, name, is_head, last_seen_at, joined_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))",
		teamID, playerName, 1,
	)
	if err != nil {
		log.Printf("Error creating player: %v", err)
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}
	playerID, _ := playerResult.LastInsertId()

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
	eventData := fmt.Sprintf(`{"team_name":"%s","is_head":1}`, teamName)
	h.broker.Publish(code, sse.Event{Type: "player_joined", Data: eventData})
	h.broker.Publish("admin:"+code, sse.Event{Type: "player_joined", Data: eventData})

	http.Redirect(w, r, "/game/"+code, http.StatusSeeOther)
}

// Helper to extract URL param
func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
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
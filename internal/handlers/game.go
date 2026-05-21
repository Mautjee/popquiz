package handlers

import (
	"database/sql"
	"encoding/json"
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

type GameHandler struct {
	db            *sql.DB
	broker        *sse.Broker
	sessionSecret string
	templates     *template.Template
}

func NewGameHandler(db *sql.DB, broker *sse.Broker, sessionSecret string) *GameHandler {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).ParseFiles(
		"templates/base.html",
		"templates/game/player.html",
		"templates/game/results.html",
	))
	return &GameHandler{
		db:            db,
		broker:        broker,
		sessionSecret: sessionSecret,
		templates:     tmpl,
	}
}

func (h *GameHandler) getPlayerFromCookie(r *http.Request) (playerID, teamID int64, ok bool) {
	cookie, err := r.Cookie("player_session")
	if err != nil {
		return 0, 0, false
	}
	return parsePlayerSession(cookie.Value, h.sessionSecret)
}

func (h *GameHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	playerID, teamID, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Look up game
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)
	if err != nil {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Look up player
	var player models.Player
	err = h.db.QueryRow(`
		SELECT id, team_id, name, is_head, last_seen_at, joined_at
		FROM players WHERE id = ?
	`, playerID).Scan(&player.ID, &player.TeamID, &player.Name, &player.IsHead, &player.LastSeenAt, &player.JoinedAt)
	if err != nil {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Update last_seen_at
	h.db.Exec("UPDATE players SET last_seen_at = datetime('now') WHERE id = ?", playerID)

	// Look up team
	var team models.Team
	err = h.db.QueryRow("SELECT id, game_id, name, score FROM teams WHERE id = ?", teamID).Scan(&team.ID, &team.GameID, &team.Name, &team.Score)
	if err != nil {
		http.Redirect(w, r, "/?code="+code, http.StatusSeeOther)
		return
	}

	// Build template data
	data := map[string]interface{}{
		"Game":    game,
		"Player":  player,
		"Team":    team,
		"Code":    code,
		"IsHead":  player.IsHead == 1,
		"HeadIcon": "👑",
	}

	// Load round and question info if in question state
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var q models.Question
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, points, order_index
			FROM questions WHERE id = ?
		`, game.CurrentQuestionID.Int64).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
			&q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.Points, &q.OrderIndex)
		if err == nil {
			data["CurrentQuestion"] = q

			// Determine question number within round
			var orderIdx int
			h.db.QueryRow(`
				SELECT COUNT(*) FROM questions
				WHERE round_id = ? AND order_index <= (SELECT order_index FROM questions WHERE id = ?)
			`, q.RoundID, q.ID).Scan(&orderIdx)
			data["QuestionNumber"] = orderIdx

			// Check if team has already answered
			var answerCount int
			h.db.QueryRow("SELECT COUNT(*) FROM answers WHERE team_id = ? AND question_id = ?", teamID, q.ID).Scan(&answerCount)
			data["HasAnswered"] = answerCount > 0

			// Load previous answer if exists
			if answerCount > 0 {
				var answerText string
				h.db.QueryRow("SELECT answer_text FROM answers WHERE team_id = ? AND question_id = ?", teamID, q.ID).Scan(&answerText)
				data["PreviousAnswer"] = answerText
			}
		}

		// Load round info
		var r models.Round
		err = h.db.QueryRow("SELECT id, quiz_id, name, type, order_index FROM rounds WHERE id = ?",
			game.CurrentRoundID.Int64).Scan(&r.ID, &r.QuizID, &r.Name, &r.Type, &r.OrderIndex)
		if err == nil {
			data["CurrentRound"] = r
		}
	}

	if game.State == "round_reveal" && game.CurrentRoundID.Valid {
		// Load round info
		var r models.Round
		err = h.db.QueryRow("SELECT id, quiz_id, name, type, order_index FROM rounds WHERE id = ?",
			game.CurrentRoundID.Int64).Scan(&r.ID, &r.QuizID, &r.Name, &r.Type, &r.OrderIndex)
		if err == nil {
			data["CurrentRound"] = r
		}

		// Load all questions for this round
		rows, err := h.db.Query(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, points, order_index
			FROM questions WHERE round_id = ? ORDER BY order_index
		`, game.CurrentRoundID.Int64)
		if err == nil {
			var questions []models.Question
			for rows.Next() {
				var q models.Question
				rows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
					&q.CorrectAnswer, &q.Options, &q.Points, &q.OrderIndex)
				questions = append(questions, q)
			}
			rows.Close()
			data["RoundQuestions"] = questions

			// Load answers for this team for all questions in the round
			type TeamAnswer struct {
				QuestionID int64
				AnswerText string
				IsCorrect  sql.NullInt64
			}
			var teamAnswers []TeamAnswer
			for _, q := range questions {
				var ta TeamAnswer
				err := h.db.QueryRow("SELECT question_id, answer_text, is_correct FROM answers WHERE team_id = ? AND question_id = ?",
					teamID, q.ID).Scan(&ta.QuestionID, &ta.AnswerText, &ta.IsCorrect)
				if err == nil {
					teamAnswers = append(teamAnswers, ta)
				}
			}
			data["TeamAnswers"] = teamAnswers
		}

		// Load leaderboard (all teams with scores)
		rows, err = h.db.Query("SELECT id, game_id, name, score FROM teams WHERE game_id = ? ORDER BY score DESC", game.ID)
		if err == nil {
			var teams []models.Team
			for rows.Next() {
				var t models.Team
				rows.Scan(&t.ID, &t.GameID, &t.Name, &t.Score)
				teams = append(teams, t)
			}
			rows.Close()
			data["Leaderboard"] = teams
		}
	}

	h.templates.ExecuteTemplate(w, "player.html", data)
}

func (h *GameHandler) GetEvents(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	_, _, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Update last_seen on connect
	playerID, teamID, _ := h.getPlayerFromCookie(r)
	h.db.Exec("UPDATE players SET last_seen_at = datetime('now') WHERE id = ?", playerID)

	h.broker.ServeHTTP(code, w, r)

	// Update last_seen on disconnect
	h.db.Exec("UPDATE players SET last_seen_at = datetime('now') WHERE id = ?", playerID)
	_ = teamID
}

func (h *GameHandler) PostAnswer(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	playerID, teamID, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check if player is Team Head
	var isHead int
	err := h.db.QueryRow("SELECT is_head FROM players WHERE id = ?", playerID).Scan(&isHead)
	if err != nil || isHead != 1 {
		http.Error(w, "Only Team Head can submit answers", http.StatusForbidden)
		return
	}

	// Check game state
	var state string
	err = h.db.QueryRow("SELECT state FROM games WHERE room_code = ?", code).Scan(&state)
	if err != nil || state != "question" {
		http.Error(w, "Game is not in question state", http.StatusUnprocessableEntity)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	questionIDStr := r.FormValue("question_id")
	answerText := strings.TrimSpace(r.FormValue("answer_text"))

	if questionIDStr == "" || answerText == "" {
		http.Error(w, "Question ID and answer are required", http.StatusUnprocessableEntity)
		return
	}

	questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	// Upsert answer
	_, err = h.db.Exec(`
		INSERT INTO answers (team_id, question_id, answer_text) VALUES (?, ?, ?)
		ON CONFLICT(team_id, question_id) DO UPDATE SET answer_text = excluded.answer_text
	`, teamID, questionID, answerText)
	if err != nil {
		log.Printf("Error upserting answer: %v", err)
		http.Error(w, "Error submitting answer", http.StatusInternalServerError)
		return
	}

	// Publish answer_accepted event to the player
	h.broker.Publish(code, sse.Event{
		Type: "answer_accepted",
		Data: fmt.Sprintf(`{"question_id":%d}`, questionID),
	})

	// Count how many teams have answered
	var answersIn int
	var totalTeams int
	h.db.QueryRow("SELECT COUNT(DISTINCT team_id) FROM answers WHERE question_id = ?", questionID).Scan(&answersIn)
	h.db.QueryRow("SELECT COUNT(*) FROM teams WHERE game_id = (SELECT game_id FROM teams WHERE id = ?)", teamID).Scan(&totalTeams)

	// Notify admin
	eventData := fmt.Sprintf(`{"team_name":"","question_id":%d,"answers_in":%d,"total_teams":%d}`, questionID, answersIn, totalTeams)
	h.broker.Publish("admin:"+code, sse.Event{Type: "answer_submitted", Data: eventData})

	if answersIn >= totalTeams {
		h.broker.Publish("admin:"+code, sse.Event{Type: "all_answered", Data: fmt.Sprintf(`{"question_id":%d}`, questionID)})
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *GameHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.CreatedAt)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Load leaderboard
	rows, err := h.db.Query("SELECT id, game_id, name, score FROM teams WHERE game_id = ? ORDER BY score DESC", game.ID)
	if err != nil {
		http.Error(w, "Error loading results", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type TeamResult struct {
		Rank  int
		Name  string
		Score int
	}
	var results []TeamResult
	rank := 1
	for rows.Next() {
		var t models.Team
		rows.Scan(&t.ID, &t.GameID, &t.Name, &t.Score)
		results = append(results, TeamResult{Rank: rank, Name: t.Name, Score: t.Score})
		rank++
	}

	data := map[string]interface{}{
		"Game":    game,
		"Results": results,
		"Code":    code,
	}

	h.templates.ExecuteTemplate(w, "results.html", data)
}
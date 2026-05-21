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

// MCOpt represents a parsed multiple choice option for template rendering.
type MCOpt struct {
	Letter string
	Text   string
}

func parseMCOptions(jsonStr string) []MCOpt {
	var opts []string
	if err := json.Unmarshal([]byte(jsonStr), &opts); err != nil {
		return nil
	}
	letters := []string{"A", "B", "C", "D"}
	var result []MCOpt
	for i, opt := range opts {
		if i < len(letters) {
			result = append(result, MCOpt{Letter: letters[i], Text: opt})
		}
	}
	return result
}

type GameHandler struct {
	db            *sql.DB
	broker        *sse.Broker
	sessionSecret string
}

func NewGameHandler(db *sql.DB, broker *sse.Broker, sessionSecret string) *GameHandler {
	return &GameHandler{
		db:            db,
		broker:        broker,
		sessionSecret: sessionSecret,
	}
}

func (h *GameHandler) render(w http.ResponseWriter, data interface{}, name string, files ...string) {
	allFiles := append([]string{"templates/base.html"}, files...)
	tmpl := template.Must(template.New(".").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"ne": func(a, b interface{}) bool { return a != b },
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).ParseFiles(allFiles...))
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render error (%s): %v", name, err)
	}
}

func (h *GameHandler) renderPartial(w http.ResponseWriter, data interface{}, name string, files ...string) {
	tmpl := template.Must(template.New(".").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"ne": func(a, b interface{}) bool { return a != b },
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).ParseFiles(files...))
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("renderPartial error (%s): %v", name, err)
	}
}

func (h *GameHandler) getPlayerFromCookie(r *http.Request) (playerID, teamID int64, ok bool) {
	cookie, err := r.Cookie("player_session")
	if err != nil {
		return 0, 0, false
	}
	return parsePlayerSession(cookie.Value, h.sessionSecret)
}

// PlayerInfo holds basic team member data for the lobby display.
type PlayerInfo struct {
	Name   sql.NullString
	IsHead int
}

// buildPlayerData loads all data needed to render the player page.
func (h *GameHandler) buildPlayerData(code string, playerID, teamID int64) (map[string]interface{}, error) {
	// Look up game
	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
	if err != nil {
		return nil, err
	}

	// Look up player
	var player models.Player
	err = h.db.QueryRow(`
		SELECT id, team_id, name, is_head, last_seen_at, joined_at
		FROM players WHERE id = ?
	`, playerID).Scan(&player.ID, &player.TeamID, &player.Name, &player.IsHead, &player.LastSeenAt, &player.JoinedAt)
	if err != nil {
		return nil, err
	}

	// Auto-assign player name if null
	if !player.Name.Valid || player.Name.String == "" {
		var playerCount int
		h.db.QueryRow("SELECT COUNT(*) FROM players WHERE team_id = ?", teamID).Scan(&playerCount)
		player.Name = sql.NullString{String: fmt.Sprintf("Player %d", playerCount), Valid: true}
	}

	// Look up team
	var team models.Team
	err = h.db.QueryRow("SELECT id, game_id, name, score FROM teams WHERE id = ?", teamID).Scan(&team.ID, &team.GameID, &team.Name, &team.Score)
	if err != nil {
		return nil, err
	}

	// Update last_seen_at
	h.db.Exec("UPDATE players SET last_seen_at = datetime('now') WHERE id = ?", playerID)

	// Load team members
	var players []PlayerInfo
	pRows, err := h.db.Query("SELECT name, is_head FROM players WHERE team_id = ? ORDER BY joined_at", teamID)
	if err == nil {
		idx := 1
		for pRows.Next() {
			var p PlayerInfo
			pRows.Scan(&p.Name, &p.IsHead)
			if !p.Name.Valid || p.Name.String == "" {
				p.Name = sql.NullString{String: fmt.Sprintf("Player %d", idx), Valid: true}
			}
			players = append(players, p)
			idx++
		}
		pRows.Close()
	}
	if players == nil {
		players = []PlayerInfo{}
	}

	// Parse MC options for template rendering
	mcOptions := []MCOpt{}
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var q models.Question
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index
			FROM questions WHERE id = ?
		`, game.CurrentQuestionID.Int64).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
			&q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex)
		if err == nil && q.QuestionType == "multiple_choice" && q.Options.Valid {
			mcOptions = parseMCOptions(q.Options.String)
		}
	}

	// Load quiz mode
	var quizMode string
	h.db.QueryRow("SELECT mode FROM quizzes WHERE id = ?", game.QuizID).Scan(&quizMode)

	data := map[string]interface{}{
		"Game":         game,
		"Player":      player,
		"Team":        team,
		"Code":        code,
		"IsHead":      player.IsHead == 1,
		"HeadIcon":    "👑",
		"Players":     players,
		"ShowQuestion": game.ShowQuestion == 1,
		"MCOptions":   mcOptions,
		"QuizMode":     quizMode,
	}

	// Load round and question info if in question state
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var q models.Question
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index
			FROM questions WHERE id = ?
		`, game.CurrentQuestionID.Int64).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
			&q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex)
		if err == nil {
			data["CurrentQuestion"] = q

			// Parse MC options for this question
			if q.QuestionType == "multiple_choice" && q.Options.Valid {
				data["MCOptions"] = parseMCOptions(q.Options.String)
			} else {
				data["MCOptions"] = []MCOpt{}
			}

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

	return data, nil
}

func (h *GameHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	playerID, teamID, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	data, err := h.buildPlayerData(code, playerID, teamID)
	if err != nil {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	h.render(w, data, "player.html", "templates/game/player.html", "templates/game/partials/answer_area.html", "templates/game/partials/game_state_content.html", "templates/game/partials/team_header.html")
}

// GetGamePartial returns just the game state content fragment for HTMX updates.
func (h *GameHandler) GetGamePartial(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	playerID, teamID, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	data, err := h.buildPlayerData(code, playerID, teamID)
	if err != nil {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "game_state_content", "templates/game/partials/game_state_content.html", "templates/game/partials/answer_area.html")
}

// GetPlayerTeamInfo returns the team header fragment for HTMX updates.
func (h *GameHandler) GetPlayerTeamInfo(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	playerID, teamID, ok := h.getPlayerFromCookie(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	data, err := h.buildPlayerData(code, playerID, teamID)
	if err != nil {
		http.Error(w, "player not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "team_header", "templates/game/partials/team_header.html")
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

	// Auto-score multiple choice answers at submission time
	var qType string
	var correctAnswer string
	h.db.QueryRow("SELECT question_type, correct_answer FROM questions WHERE id = ?", questionID).Scan(&qType, &correctAnswer)
	if qType == "multiple_choice" {
		isCorrect := 0
		if strings.EqualFold(strings.TrimSpace(answerText), strings.TrimSpace(correctAnswer)) {
			isCorrect = 1
		}
		h.db.Exec("UPDATE answers SET is_correct = ? WHERE team_id = ? AND question_id = ?", isCorrect, teamID, questionID)
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

	// Return the answer area fragment showing "Answer submitted"
	data, err := h.buildPlayerData(code, playerID, teamID)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "answer_area", "templates/game/partials/answer_area.html")
}

func (h *GameHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	var game models.Game
	err := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Load quiz mode
	var quiz models.Quiz
	h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", game.QuizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)

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
		"QuizMode": quiz.Mode,
	}

	// For offline mode, load round/question summary
	if quiz.Mode == "offline" {
		type QuestionSummary struct {
			QuestionText  string
			CorrectAnswer string
		}
		type RoundSummary struct {
			RoundName  string
			Questions  []QuestionSummary
		}
		var roundSummary []RoundSummary
		rRows, err := h.db.Query("SELECT id, name FROM rounds WHERE quiz_id = ? ORDER BY order_index", game.QuizID)
		if err == nil {
			for rRows.Next() {
				var rID int64
				var rName string
				rRows.Scan(&rID, &rName)
				qRows, err := h.db.Query("SELECT question_text, correct_answer FROM questions WHERE round_id = ? ORDER BY order_index", rID)
				var questions []QuestionSummary
				if err == nil {
					for qRows.Next() {
						var qs QuestionSummary
						qRows.Scan(&qs.QuestionText, &qs.CorrectAnswer)
						questions = append(questions, qs)
					}
					qRows.Close()
				}
				roundSummary = append(roundSummary, RoundSummary{RoundName: rName, Questions: questions})
			}
			rRows.Close()
		}
		data["RoundSummary"] = roundSummary
	}

	h.render(w, data, "results.html", "templates/game/results.html")
}
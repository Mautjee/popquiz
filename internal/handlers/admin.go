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
		"ne": func(a, b interface{}) bool { return a != b },
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
		"ne": func(a, b interface{}) bool { return a != b },
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
	rows, err := h.db.Query("SELECT id, title, mode, created_at FROM quizzes ORDER BY created_at DESC")
	if err != nil {
		log.Printf("Error loading quizzes: %v", err)
		http.Error(w, "Error loading quizzes", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var quizzes []models.Quiz
	for rows.Next() {
		var q models.Quiz
		rows.Scan(&q.ID, &q.Title, &q.Mode, &q.CreatedAt)
		quizzes = append(quizzes, q)
	}

	data := map[string]interface{}{
		"Quizzes": quizzes,
	}
	h.render(w, data, "index.html", "templates/admin/index.html", "templates/admin/partials/quiz_row.html", "templates/admin/partials/quiz_delete_confirm.html")
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

	mode := r.FormValue("mode")
	if mode != "online" && mode != "offline" {
		mode = "online"
	}

	result, err := h.db.Exec("INSERT INTO quizzes (title, mode) VALUES (?, ?)", title, mode)
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
	err = h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", quizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)
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

	// Load questions and video groups for each round
	var roundsWithQuestions []models.RoundData
	for _, rd := range rounds {
		var roundData models.RoundData
		roundData.Round = rd

		if rd.Type == "video" {
			// Load video groups
			vgRows, err := h.db.Query(`
				SELECT id, round_id, title, video_filename, order_index
				FROM video_groups WHERE round_id = ? ORDER BY order_index
			`, rd.ID)
			if err == nil {
				for vgRows.Next() {
					var vg models.VideoGroup
					vgRows.Scan(&vg.ID, &vg.RoundID, &vg.Title, &vg.VideoFilename, &vg.OrderIndex)

					// Load questions for this group
					qRows, err := h.db.Query(`
						SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
						FROM questions WHERE round_id = ? AND video_group_id = ? ORDER BY order_index
					`, rd.ID, vg.ID)
					var gQuestions []models.Question
					if err == nil {
						for qRows.Next() {
							var q models.Question
							qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
							gQuestions = append(gQuestions, q)
						}
						qRows.Close()
					}
					if gQuestions == nil {
						gQuestions = []models.Question{}
					}
					roundData.VideoGroups = append(roundData.VideoGroups, models.VideoGroupWithQuestions{
						Group:     vg,
						Questions: gQuestions,
					})
				}
				vgRows.Close()
			}

			// Load ungrouped questions
			uRows, err := h.db.Query(`
				SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
				FROM questions WHERE round_id = ? AND video_group_id IS NULL ORDER BY order_index
			`, rd.ID)
			if err == nil {
				for uRows.Next() {
					var q models.Question
					uRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
					roundData.UngroupedQuestions = append(roundData.UngroupedQuestions, q)
				}
				uRows.Close()
			}
		}

		// Load ALL questions (for backward compat + text rounds)
		qRows, err := h.db.Query(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
			FROM questions WHERE round_id = ? ORDER BY order_index
		`, rd.ID)
		if err != nil {
			continue
		}
		var questions []models.Question
		for qRows.Next() {
			var q models.Question
			qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
			questions = append(questions, q)
		}
		qRows.Close()
		roundData.Questions = questions

		if roundData.UngroupedQuestions == nil {
			roundData.UngroupedQuestions = []models.Question{}
		}
		if roundData.VideoGroups == nil {
			roundData.VideoGroups = []models.VideoGroupWithQuestions{}
		}

		roundsWithQuestions = append(roundsWithQuestions, roundData)
	}

	// Check if a game already exists for this quiz
	var game models.Game
	gameErr := h.db.QueryRow(`
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE quiz_id = ? ORDER BY created_at DESC LIMIT 1
	`, quizID).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State, &game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)

	data := map[string]interface{}{
		"Quiz":                quiz,
		"RoundsWithQuestions": roundsWithQuestions,
		"IsNew":               false,
		"Game":                nil,
	}
	if gameErr == nil {
		data["Game"] = game
	}

	h.render(w, data, "quiz_editor.html", "templates/admin/quiz_editor.html", "templates/admin/partials/question_row.html", "templates/admin/partials/question_edit_form.html", "templates/admin/partials/question_fields.html")
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

	// For offline quizzes, force question_type = 'open'
	var quizMode string
	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id WHERE r.id = ?", roundID).Scan(&quizMode)
	if quizMode == "offline" {
		questionType = "open"
	}

	points := 1
	if pointsStr != "" {
		if p, err := strconv.Atoi(pointsStr); err == nil && p > 0 {
			points = p
		}
	}

	if questionText == "" {
		http.Error(w, "Question text required", http.StatusUnprocessableEntity)
		return
	}
	if quizMode != "offline" && correctAnswer == "" {
		http.Error(w, "Correct answer required", http.StatusUnprocessableEntity)
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

	// Handle image upload
	var imageFilename sql.NullString
	imgFile, imgHeader, err := r.FormFile("image_file")
	if err == nil {
		defer imgFile.Close()

		ext := filepath.Ext(imgHeader.Filename)
		if ext == "" {
			ext = ".png"
		}
		imgName := generateToken() + ext
		imgPath := filepath.Join(h.dataDir, "images", imgName)

		imgDst, err := os.Create(imgPath)
		if err != nil {
			log.Printf("Error creating image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}
		defer imgDst.Close()

		if _, err := io.Copy(imgDst, imgFile); err != nil {
			log.Printf("Error writing image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}

		imageFilename = sql.NullString{String: imgName, Valid: true}
	}

	// Handle options JSON for MC
	var optionsJSON sql.NullString
	if questionType == "multiple_choice" && options != "" {
		optionsJSON = sql.NullString{String: options, Valid: true}
	}

	_, err = h.db.Exec(`
		INSERT INTO questions (round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, roundID, questionText, questionType, correctAnswer, optionsJSON, videoFilename, imageFilename, points, orderIndex)

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

	// Delete video and image files if exists
	var videoFilename, imageFilename sql.NullString
	h.db.QueryRow("SELECT video_filename, image_filename FROM questions WHERE id = ?", questionID).Scan(&videoFilename, &imageFilename)
	if videoFilename.Valid && videoFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", videoFilename.String))
	}
	if imageFilename.Valid && imageFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "images", imageFilename.String))
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

// GetEditQuestion returns an inline edit form partial for a question.
func (h *AdminHandler) GetEditQuestion(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "qid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	var q models.Question
	err = h.db.QueryRow(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
		FROM questions WHERE id = ?
	`, questionID).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
	if err != nil {
		http.Error(w, "Question not found", http.StatusNotFound)
		return
	}

	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	// Parse MC options
	mcOpts := parseAdminMCOptions(q.Options)

	// Get quiz mode for conditional UI
	var quizMode string
	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id JOIN questions qq ON qq.round_id = r.id WHERE qq.id = ?", questionID).Scan(&quizMode)

	data := map[string]interface{}{
		"Question":  q,
		"QuizID":   quizID,
		"MCOptions": mcOpts,
		"QuizMode":  quizMode,
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, data, "question_edit_form", "templates/admin/partials/question_edit_form.html", "templates/admin/partials/question_fields.html")
}

// GetQuestionRow returns a question row partial (for HTMX cancel).
func (h *AdminHandler) GetQuestionRow(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "qid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	var q models.Question
	err = h.db.QueryRow(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
		FROM questions WHERE id = ?
	`, questionID).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
	if err != nil {
		http.Error(w, "Question not found", http.StatusNotFound)
		return
	}

	type RowData struct {
		Question models.Question
		QuizID   int64
		QuizMode string
	}

	// Get quiz mode for conditional UI
	var quizMode string
	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id JOIN questions qq ON qq.round_id = r.id WHERE qq.id = ?", questionID).Scan(&quizMode)

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, RowData{Question: q, QuizID: quizID, QuizMode: quizMode}, "question_row", "templates/admin/partials/question_row.html")
}

// PostUpdateQuestion updates a question and returns the updated row partial.
func (h *AdminHandler) PostUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "qid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	// Parse multipart form for file uploads
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
	}

	questionText := strings.TrimSpace(r.FormValue("question_text"))
	questionType := r.FormValue("question_type")
	correctAnswer := strings.TrimSpace(r.FormValue("correct_answer"))
	pointsStr := r.FormValue("points")
	options := r.FormValue("options")

	// For offline quizzes, force question_type = 'open'
	var quizMode string
	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id JOIN questions qq ON qq.round_id = r.id WHERE qq.id = ?", questionID).Scan(&quizMode)
	if quizMode == "offline" {
		questionType = "open"
	}

	// For MC questions, the correct_answer may come from correct_answer_mc
	if questionType == "multiple_choice" && correctAnswer == "" {
		correctAnswer = r.FormValue("correct_answer_mc")
	}

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

	var optionsJSON sql.NullString
	if questionType == "multiple_choice" && options != "" {
		optionsJSON = sql.NullString{String: options, Valid: true}
	}

	// Handle video replacement
	videoFile, videoHeader, err := r.FormFile("video_file")
	if err == nil {
		defer videoFile.Close()

		// Delete old video if exists
		var oldVideo sql.NullString
		h.db.QueryRow("SELECT video_filename FROM questions WHERE id = ?", questionID).Scan(&oldVideo)
		if oldVideo.Valid && oldVideo.String != "" {
			os.Remove(filepath.Join(h.dataDir, "videos", oldVideo.String))
		}

		ext := filepath.Ext(videoHeader.Filename)
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

		if _, err := io.Copy(dst, videoFile); err != nil {
			log.Printf("Error writing video file: %v", err)
			http.Error(w, "Error saving video", http.StatusInternalServerError)
			return
		}

		_, err = h.db.Exec("UPDATE questions SET video_filename = ? WHERE id = ?", filename, questionID)
		if err != nil {
			log.Printf("Error updating video_filename: %v", err)
		}
	}

	// Handle image replacement
	imgFile, imgHeader, err := r.FormFile("image_file")
	if err == nil {
		defer imgFile.Close()

		// Delete old image if exists
		var oldImage sql.NullString
		h.db.QueryRow("SELECT image_filename FROM questions WHERE id = ?", questionID).Scan(&oldImage)
		if oldImage.Valid && oldImage.String != "" {
			os.Remove(filepath.Join(h.dataDir, "images", oldImage.String))
		}

		ext := filepath.Ext(imgHeader.Filename)
		if ext == "" {
			ext = ".png"
		}
		imgName := generateToken() + ext
		imgPath := filepath.Join(h.dataDir, "images", imgName)

		imgDst, err := os.Create(imgPath)
		if err != nil {
			log.Printf("Error creating image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}
		defer imgDst.Close()

		if _, err := io.Copy(imgDst, imgFile); err != nil {
			log.Printf("Error writing image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}

		_, err = h.db.Exec("UPDATE questions SET image_filename = ? WHERE id = ?", imgName, questionID)
		if err != nil {
			log.Printf("Error updating image_filename: %v", err)
		}
	}

	_, err = h.db.Exec(`
		UPDATE questions SET question_text = ?, question_type = ?, correct_answer = ?, options = ?, points = ?
		WHERE id = ?
	`, questionText, questionType, correctAnswer, optionsJSON, points, questionID)
	if err != nil {
		log.Printf("Error updating question: %v", err)
		http.Error(w, "Error updating question", http.StatusInternalServerError)
		return
	}

	// Return the updated question row
	var q models.Question
	h.db.QueryRow(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
		FROM questions WHERE id = ?
	`, questionID).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)

	type RowData struct {
		Question models.Question
		QuizID   int64
		QuizMode string
	}

	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id JOIN questions qq ON qq.round_id = r.id WHERE qq.id = ?", questionID).Scan(&quizMode)

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, RowData{Question: q, QuizID: quizID, QuizMode: quizMode}, "question_row", "templates/admin/partials/question_row.html")
}

func (h *AdminHandler) DeleteQuestionRoute(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "qid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	// Delete video and image files if exists
	var videoFilename, imageFilename sql.NullString
	h.db.QueryRow("SELECT video_filename, image_filename FROM questions WHERE id = ?", questionID).Scan(&videoFilename, &imageFilename)
	if videoFilename.Valid && videoFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", videoFilename.String))
	}
	if imageFilename.Valid && imageFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "images", imageFilename.String))
	}

	_, err = h.db.Exec("DELETE FROM questions WHERE id = ?", questionID)
	if err != nil {
		log.Printf("Error deleting question: %v", err)
		http.Error(w, "Error deleting question", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// PostDeleteVideo removes a question's video file.
func (h *AdminHandler) PostDeleteVideo(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.ParseInt(chi.URLParam(r, "qid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid question ID", http.StatusBadRequest)
		return
	}

	var videoFilename sql.NullString
	err = h.db.QueryRow("SELECT video_filename FROM questions WHERE id = ?", questionID).Scan(&videoFilename)
	if err != nil {
		http.Error(w, "Question not found", http.StatusNotFound)
		return
	}

	if videoFilename.Valid && videoFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", videoFilename.String))
		_, err = h.db.Exec("UPDATE questions SET video_filename = NULL WHERE id = ?", questionID)
		if err != nil {
			log.Printf("Error clearing video_filename: %v", err)
			http.Error(w, "Error deleting video", http.StatusInternalServerError)
			return
		}
	}

	// Return just the video section (now showing upload form)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div id="video-section-%d"><div><label class="block text-sm font-medium text-gray-300 mb-1">Video Clip</label><input type="file" name="video_file" accept="video/*" class="w-full text-gray-300 text-sm"></div></div>`, questionID)
}

// PostReorderQuestions updates the order_index of questions within a round.
func (h *AdminHandler) PostReorderQuestions(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	questionIDs := r.Form["question_ids"]
	if len(questionIDs) == 0 {
		http.Error(w, "No question IDs provided", http.StatusBadRequest)
		return
	}

	// Validate all IDs belong to the round
	for _, qidStr := range questionIDs {
		qid, err := strconv.ParseInt(qidStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid question ID", http.StatusBadRequest)
			return
		}
		var rid int64
		if err := h.db.QueryRow("SELECT round_id FROM questions WHERE id = ?", qid).Scan(&rid); err != nil {
			http.Error(w, "Question not found", http.StatusNotFound)
			return
		}
		if rid != roundID {
			http.Error(w, "Question does not belong to this round", http.StatusForbidden)
			return
		}
	}

	// Update order_index for each question
	for i, qidStr := range questionIDs {
		qid, _ := strconv.ParseInt(qidStr, 10, 64)
		_, err := h.db.Exec("UPDATE questions SET order_index = ? WHERE id = ?", i, qid)
		if err != nil {
			log.Printf("Error updating question order: %v", err)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// PostDeleteQuiz deletes a quiz and all related data.
func (h *AdminHandler) PostDeleteQuiz(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	// Get all video and image filenames for questions in this quiz
	rows, err := h.db.Query(`
		SELECT q.video_filename, q.image_filename FROM questions q
		JOIN rounds r ON q.round_id = r.id
		WHERE r.quiz_id = ? AND (q.video_filename IS NOT NULL OR q.image_filename IS NOT NULL)
	`, quizID)
	if err != nil {
		log.Printf("Error querying files: %v", err)
	} else {
		for rows.Next() {
			var vf, imf sql.NullString
			rows.Scan(&vf, &imf)
			if vf.Valid && vf.String != "" {
				os.Remove(filepath.Join(h.dataDir, "videos", vf.String))
			}
			if imf.Valid && imf.String != "" {
				os.Remove(filepath.Join(h.dataDir, "images", imf.String))
			}
		}
		rows.Close()
	}

	// Also delete video group videos
	vgRows, err := h.db.Query(`
		SELECT vg.video_filename FROM video_groups vg
		JOIN rounds r ON vg.round_id = r.id
		WHERE r.quiz_id = ? AND vg.video_filename IS NOT NULL
	`, quizID)
	if err == nil {
		for vgRows.Next() {
			var vf sql.NullString
			vgRows.Scan(&vf)
			if vf.Valid && vf.String != "" {
				os.Remove(filepath.Join(h.dataDir, "videos", vf.String))
			}
		}
		vgRows.Close()
	}

	tx, err := h.db.Begin()
	if err != nil {
		log.Printf("Error starting transaction: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete answers for games of this quiz
	if _, err := tx.Exec(`DELETE FROM answers WHERE team_id IN (SELECT id FROM teams WHERE game_id IN (SELECT id FROM games WHERE quiz_id = ?))`, quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting answers: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete players for games of this quiz
	if _, err := tx.Exec(`DELETE FROM players WHERE team_id IN (SELECT id FROM teams WHERE game_id IN (SELECT id FROM games WHERE quiz_id = ?))`, quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting players: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete teams for games of this quiz
	if _, err := tx.Exec(`DELETE FROM teams WHERE game_id IN (SELECT id FROM games WHERE quiz_id = ?)`, quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting teams: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete games
	if _, err := tx.Exec("DELETE FROM games WHERE quiz_id = ?", quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting games: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete questions
	if _, err := tx.Exec("DELETE FROM questions WHERE round_id IN (SELECT id FROM rounds WHERE quiz_id = ?)", quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting questions: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete video groups
	if _, err := tx.Exec("DELETE FROM video_groups WHERE round_id IN (SELECT id FROM rounds WHERE quiz_id = ?)", quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting video groups: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete rounds
	if _, err := tx.Exec("DELETE FROM rounds WHERE quiz_id = ?", quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting rounds: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	// Delete quiz
	if _, err := tx.Exec("DELETE FROM quizzes WHERE id = ?", quizID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting quiz: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing quiz deletion: %v", err)
		http.Error(w, "Error deleting quiz", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/admin")
	w.WriteHeader(http.StatusOK)
}

// GetDeleteQuizConfirm returns a confirmation partial for quiz deletion.
func (h *AdminHandler) GetDeleteQuizConfirm(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	var quiz models.Quiz
	err = h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", quizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)
	if err != nil {
		http.Error(w, "Quiz not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, quiz, "quiz_delete_confirm", "templates/admin/partials/quiz_delete_confirm.html")
}

// GetQuizRow returns the normal quiz row partial (for cancel swap-back).
func (h *AdminHandler) GetQuizRow(w http.ResponseWriter, r *http.Request) {
	quizID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid quiz ID", http.StatusBadRequest)
		return
	}

	var quiz models.Quiz
	err = h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", quizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)
	if err != nil {
		http.Error(w, "Quiz not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, quiz, "quiz_row", "templates/admin/partials/quiz_row.html")
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
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
	if err != nil {
		return nil, err
	}

	// Load quiz
	var quiz models.Quiz
	h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", game.QuizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)

	data := map[string]interface{}{
		"Game": game,
		"Quiz": quiz,
		"Code": code,
	}

	// Load teams with player counts
	type TeamWithCount struct {
		Team        models.Team
		PlayerCount int
	}
	var teamsWithCounts []TeamWithCount
	tRows, err := h.db.Query("SELECT id, game_id, name, score FROM teams WHERE game_id = ? ORDER BY name", game.ID)
	if err != nil {
		return nil, err
	}
	for tRows.Next() {
		var t models.Team
		tRows.Scan(&t.ID, &t.GameID, &t.Name, &t.Score)
		var playerCount int
		h.db.QueryRow("SELECT COUNT(*) FROM players WHERE team_id = ?", t.ID).Scan(&playerCount)
		teamsWithCounts = append(teamsWithCounts, TeamWithCount{Team: t, PlayerCount: playerCount})
	}
	tRows.Close()
	if teamsWithCounts == nil {
		teamsWithCounts = []TeamWithCount{}
	}

	// Also load plain teams for backward compatibility
	var teams []models.Team
	for _, twc := range teamsWithCounts {
		teams = append(teams, twc.Team)
	}
	data["Teams"] = teams
	data["TeamsWithCounts"] = teamsWithCounts

	// Load current question info if in question state
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var q models.Question
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
			FROM questions WHERE id = ?
		`, game.CurrentQuestionID.Int64).Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType,
			&q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
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
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
			FROM questions WHERE round_id = ? ORDER BY order_index
		`, rd.ID)
		if err != nil {
			rwq = append(rwq, RoundWithQuestions{Round: rd})
			continue
		}
		var questions []models.Question
		for qRows.Next() {
			var q models.Question
			qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
			questions = append(questions, q)
		}
		qRows.Close()
		rwq = append(rwq, RoundWithQuestions{Round: rd, Questions: questions})
	}
	data["RoundsWithQuestions"] = rwq

	// ShowQuestion for greying out the button
	data["ShowQuestion"] = game.ShowQuestion == 1

	// Determine if current question has a video group and if it's the first question in the group
	if game.State == "question" && game.CurrentQuestionID.Valid {
		var vgID sql.NullInt64
		h.db.QueryRow("SELECT video_group_id FROM questions WHERE id = ?", game.CurrentQuestionID.Int64).Scan(&vgID)
		if vgID.Valid && vgID.Int64 != 0 {
			var vg models.VideoGroup
			err := h.db.QueryRow("SELECT id, round_id, title, video_filename, order_index FROM video_groups WHERE id = ?", vgID.Int64).Scan(&vg.ID, &vg.RoundID, &vg.Title, &vg.VideoFilename, &vg.OrderIndex)
			if err == nil {
				data["CurrentVideoGroup"] = vg
				// Check if this is the first question in the group
				var firstQuestionID int64
				err := h.db.QueryRow("SELECT id FROM questions WHERE video_group_id = ? ORDER BY order_index LIMIT 1", vg.ID).Scan(&firstQuestionID)
				if err == nil {
					data["IsFirstInGroup"] = firstQuestionID == game.CurrentQuestionID.Int64
				}
			}
		}
	}

	// HasNextQuestion for greying out the Next Question button
	// Uses group-aware ordering: grouped questions ordered by group then question,
	// then ungrouped questions by their order_index
	hasNextQuestion := false
	if game.State == "question" && game.CurrentQuestionID.Valid && game.CurrentRoundID.Valid {
		// Check current question's group
		var currentVGID sql.NullInt64
		h.db.QueryRow("SELECT video_group_id FROM questions WHERE id = ?", game.CurrentQuestionID.Int64).Scan(&currentVGID)

		if currentVGID.Valid && currentVGID.Int64 != 0 {
			// Question belongs to a group — check for next question in same group
			var nextQID int64
			err = h.db.QueryRow(`
				SELECT id FROM questions
				WHERE video_group_id = ? AND order_index > (
					SELECT order_index FROM questions WHERE id = ?
				) ORDER BY order_index LIMIT 1
			`, currentVGID.Int64, game.CurrentQuestionID.Int64).Scan(&nextQID)
			if err == nil {
				hasNextQuestion = true
			} else {
				// No more questions in this group — check for next group or ungrouped
				var currentVGOrder int
				var currentQOrder int
				h.db.QueryRow("SELECT order_index FROM video_groups WHERE id = ?", currentVGID.Int64).Scan(&currentVGOrder)
				h.db.QueryRow("SELECT order_index FROM questions WHERE id = ?", game.CurrentQuestionID.Int64).Scan(&currentQOrder)

				// Next question in the next group
				err = h.db.QueryRow(`
					SELECT q.id FROM questions q
					JOIN video_groups vg ON vg.id = q.video_group_id
					WHERE q.round_id = ? AND vg.order_index > ?
					ORDER BY vg.order_index, q.order_index LIMIT 1
				`, game.CurrentRoundID.Int64, currentVGOrder).Scan(&nextQID)
				if err == nil {
					hasNextQuestion = true
				} else {
					// Check ungrouped questions
					err = h.db.QueryRow(`
						SELECT id FROM questions
						WHERE round_id = ? AND video_group_id IS NULL
						ORDER BY order_index LIMIT 1
					`, game.CurrentRoundID.Int64).Scan(&nextQID)
					if err == nil {
						hasNextQuestion = true
					}
				}
			}
		} else {
			// Ungrouped question — check for next ungrouped question
			var nextQID int64
			err = h.db.QueryRow(`
				SELECT id FROM questions
				WHERE round_id = ? AND video_group_id IS NULL AND order_index > (
					SELECT order_index FROM questions WHERE id = ?
				) ORDER BY order_index LIMIT 1
			`, game.CurrentRoundID.Int64, game.CurrentQuestionID.Int64).Scan(&nextQID)
			if err == nil {
				hasNextQuestion = true
			} else {
				// No more ungrouped questions — check if there are grouped questions after
				err = h.db.QueryRow(`
					SELECT id FROM questions
					WHERE round_id = ? AND video_group_id IS NOT NULL
					ORDER BY order_index LIMIT 1
				`, game.CurrentRoundID.Int64).Scan(&nextQID)
				if err != nil {
					// Backward compat: check simple order_index ordering
					err = h.db.QueryRow(`
						SELECT id FROM questions WHERE round_id = ? AND order_index > (
							SELECT order_index FROM questions WHERE id = ?
						) ORDER BY order_index LIMIT 1
					`, game.CurrentRoundID.Int64, game.CurrentQuestionID.Int64).Scan(&nextQID)
					if err == nil {
						hasNextQuestion = true
					}
				}
			}
		}
	}
	data["HasNextQuestion"] = hasNextQuestion

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
		SELECT id, quiz_id, room_code, state, current_question_id, current_round_id, show_question, created_at
		FROM games WHERE room_code = ?
	`, code).Scan(&game.ID, &game.QuizID, &game.RoomCode, &game.State,
		&game.CurrentQuestionID, &game.CurrentRoundID, &game.ShowQuestion, &game.CreatedAt)
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

	// Find the first question in this round using group-aware ordering
	var firstQuestion models.Question
	err = h.db.QueryRow(`
		SELECT q.id, q.round_id, q.question_text, q.question_type, q.correct_answer, q.options, q.video_filename, q.image_filename, q.points, q.order_index, COALESCE(q.video_group_id, 0)
		FROM questions q
		LEFT JOIN video_groups vg ON vg.id = q.video_group_id
		WHERE q.round_id = ?
		ORDER BY COALESCE(vg.order_index, 999999), q.order_index
		LIMIT 1
	`, round.ID).Scan(&firstQuestion.ID, &firstQuestion.RoundID, &firstQuestion.QuestionText,
		&firstQuestion.QuestionType, &firstQuestion.CorrectAnswer, &firstQuestion.Options,
		&firstQuestion.VideoFilename, &firstQuestion.ImageFilename, &firstQuestion.Points, &firstQuestion.OrderIndex, &firstQuestion.VideoGroupID)

	if err != nil {
		http.Error(w, "No questions in this round", http.StatusUnprocessableEntity)
		return
	}

	// Update game state
	_, err = h.db.Exec(`
		UPDATE games SET state = 'question', current_question_id = ?, current_round_id = ?, show_question = 0
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

	// Find next question in the current round using group-aware ordering
	// 1. If current question is in a group, check for next question in same group
	// 2. If no more in same group, check next group
	// 3. If no more groups, check ungrouped questions
	var nextQuestion models.Question
	var nextFound bool

	var currentVGID sql.NullInt64
	h.db.QueryRow("SELECT video_group_id FROM questions WHERE id = ?", game.CurrentQuestionID.Int64).Scan(&currentVGID)

	if currentVGID.Valid && currentVGID.Int64 != 0 {
		// Current question is in a group — look for next question in same group
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
			FROM questions WHERE video_group_id = ? AND order_index > (
				SELECT order_index FROM questions WHERE id = ?
			) ORDER BY order_index LIMIT 1
		`, currentVGID.Int64, game.CurrentQuestionID.Int64).Scan(
			&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
			&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
			&nextQuestion.VideoFilename, &nextQuestion.ImageFilename, &nextQuestion.Points, &nextQuestion.OrderIndex, &nextQuestion.VideoGroupID)

		if err == nil {
			nextFound = true
		} else {
			// No more in this group — find first question in next group
			var currentVGOrder int
			h.db.QueryRow("SELECT order_index FROM video_groups WHERE id = ?", currentVGID.Int64).Scan(&currentVGOrder)
			err = h.db.QueryRow(`
				SELECT q.id, q.round_id, q.question_text, q.question_type, q.correct_answer, q.options, q.video_filename, q.image_filename, q.points, q.order_index, COALESCE(q.video_group_id, 0)
				FROM questions q
				JOIN video_groups vg ON vg.id = q.video_group_id
				WHERE q.round_id = ? AND vg.order_index > ?
				ORDER BY vg.order_index, q.order_index LIMIT 1
			`, game.CurrentRoundID.Int64, currentVGOrder).Scan(
				&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
				&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
				&nextQuestion.VideoFilename, &nextQuestion.ImageFilename, &nextQuestion.Points, &nextQuestion.OrderIndex, &nextQuestion.VideoGroupID)

			if err == nil {
				nextFound = true
			} else {
				// No more groups — check ungrouped questions
				err = h.db.QueryRow(`
					SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
					FROM questions
					WHERE round_id = ? AND video_group_id IS NULL
					ORDER BY order_index LIMIT 1
				`, game.CurrentRoundID.Int64).Scan(
					&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
					&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
					&nextQuestion.VideoFilename, &nextQuestion.ImageFilename, &nextQuestion.Points, &nextQuestion.OrderIndex, &nextQuestion.VideoGroupID)
				if err == nil {
					nextFound = true
				}
			}
		}
	} else {
		// Current question is ungrouped
		err = h.db.QueryRow(`
			SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
			FROM questions WHERE round_id = ? AND video_group_id IS NULL AND order_index > (
				SELECT order_index FROM questions WHERE id = ?
			) ORDER BY order_index LIMIT 1
		`, game.CurrentRoundID.Int64, game.CurrentQuestionID.Int64).Scan(
			&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
			&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
			&nextQuestion.VideoFilename, &nextQuestion.ImageFilename, &nextQuestion.Points, &nextQuestion.OrderIndex, &nextQuestion.VideoGroupID)

		if err == nil {
			nextFound = true
		} else {
			// Fallback: simple order_index ordering (backward compat)
			err = h.db.QueryRow(`
				SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
				FROM questions WHERE round_id = ? AND order_index > (
					SELECT order_index FROM questions WHERE id = ?
				) ORDER BY order_index LIMIT 1
			`, game.CurrentRoundID.Int64, game.CurrentQuestionID.Int64).Scan(
				&nextQuestion.ID, &nextQuestion.RoundID, &nextQuestion.QuestionText,
				&nextQuestion.QuestionType, &nextQuestion.CorrectAnswer, &nextQuestion.Options,
				&nextQuestion.VideoFilename, &nextQuestion.ImageFilename, &nextQuestion.Points, &nextQuestion.OrderIndex, &nextQuestion.VideoGroupID)
			if err == nil {
				nextFound = true
			}
		}
	}

	if !nextFound {
		http.Error(w, "No more questions in this round", http.StatusUnprocessableEntity)
		return
	}

	// Update game state
	_, err = h.db.Exec(`
		UPDATE games SET current_question_id = ?, show_question = 0 WHERE id = ?
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

	// Auto-score ranged and multiple choice questions for this round (online mode only)
	var quiz models.Quiz
	h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", game.QuizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)
	if quiz.Mode == "online" {
		autoScoreRound(h.db, h.broker, game)
	}

	// Update game state to round_reveal
	_, err = h.db.Exec("UPDATE games SET state = 'round_reveal', show_question = 0 WHERE id = ?", game.ID)
	if err != nil {
		http.Error(w, "Error updating game state", http.StatusInternalServerError)
		return
	}

	// Publish state_change event
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: `{"state":"round_reveal"}`})

	if quiz.Mode == "online" {
		// Publish round_reveal with all questions and answers
		publishRoundReveal(h.db, h.broker, game)

		// Publish score_update so players see updated scores
		publishScoreUpdate(h.db, h.broker, game)
	} else {
		// Publish round_reveal for offline mode (questions + correct answers only)
		publishRoundRevealOffline(h.db, h.broker, game)
	}

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

	// Build video_play event data, including video_url if the question belongs to a group
	eventData := fmt.Sprintf(`{"question_id":%d}`, game.CurrentQuestionID.Int64)

	var vgID sql.NullInt64
	h.db.QueryRow("SELECT video_group_id FROM questions WHERE id = ?", game.CurrentQuestionID.Int64).Scan(&vgID)
	if vgID.Valid && vgID.Int64 != 0 {
		var vgFilename sql.NullString
		h.db.QueryRow("SELECT video_filename FROM video_groups WHERE id = ?", vgID.Int64).Scan(&vgFilename)
		if vgFilename.Valid && vgFilename.String != "" {
			eventData = fmt.Sprintf(`{"question_id":%d,"video_url":"/static/videos/%s"}`, game.CurrentQuestionID.Int64, vgFilename.String)
		}
	}

	h.broker.Publish(code, sse.Event{
		Type: "video_play",
		Data: eventData,
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

	_, err = h.db.Exec("UPDATE games SET show_question = 1 WHERE id = ?", game.ID)
	if err != nil {
		log.Printf("Error updating show_question: %v", err)
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

	if game.State != "round_reveal" && game.State != "question" {
		http.Error(w, "Game is not in round_reveal or question state", http.StatusUnprocessableEntity)
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

	_, err = h.db.Exec("UPDATE games SET state = 'ended', show_question = 0 WHERE id = ?", game.ID)
	if err != nil {
		http.Error(w, "Error ending game", http.StatusInternalServerError)
		return
	}

	h.broker.Publish(code, sse.Event{Type: "game_ended", Data: `{}`})

	// Return updated game state panel
	h.renderGamePanelPartial(w, code)
}

func (h *AdminHandler) PostResetGame(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.State != "ended" {
		http.Error(w, "Can only reset an ended game", http.StatusUnprocessableEntity)
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		log.Printf("Error starting reset transaction: %v", err)
		http.Error(w, "Error resetting game", http.StatusInternalServerError)
		return
	}

	// Delete all answers for this game
	if _, err := tx.Exec(`
		DELETE FROM answers WHERE team_id IN (
			SELECT id FROM teams WHERE game_id = ?
		)
	`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting answers: %v", err)
		http.Error(w, "Error resetting game", http.StatusInternalServerError)
		return
	}

	// Reset all team scores
	if _, err := tx.Exec("UPDATE teams SET score = 0 WHERE game_id = ?", game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error resetting scores: %v", err)
		http.Error(w, "Error resetting game", http.StatusInternalServerError)
		return
	}

	// Reset game state
	if _, err := tx.Exec(`
		UPDATE games SET state = 'lobby', current_question_id = NULL, current_round_id = NULL, show_question = 0
		WHERE id = ?
	`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error resetting game state: %v", err)
		http.Error(w, "Error resetting game", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing reset transaction: %v", err)
		http.Error(w, "Error resetting game", http.StatusInternalServerError)
		return
	}

	// Broadcast state_change so all players refresh
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: `{"state":"lobby"}`})

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

// PostResetTeams deletes all teams, players and answers for a game so players
// can rejoin fresh. The game stays in lobby state.
func (h *AdminHandler) PostResetTeams(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}

	// Delete answers
	if _, err := tx.Exec(`DELETE FROM answers WHERE team_id IN (SELECT id FROM teams WHERE game_id = ?)`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting answers on team reset: %v", err)
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}
	// Delete players
	if _, err := tx.Exec(`DELETE FROM players WHERE team_id IN (SELECT id FROM teams WHERE game_id = ?)`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting players on team reset: %v", err)
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}
	// Delete teams
	if _, err := tx.Exec(`DELETE FROM teams WHERE game_id = ?`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error deleting teams on team reset: %v", err)
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}
	// Ensure game is in lobby state
	if _, err := tx.Exec(`UPDATE games SET state = 'lobby', current_question_id = NULL, current_round_id = NULL, show_question = 0 WHERE id = ?`, game.ID); err != nil {
		tx.Rollback()
		log.Printf("Error resetting game state on team reset: %v", err)
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Error resetting teams", http.StatusInternalServerError)
		return
	}

	// Kick all connected players back to the join screen
	h.broker.Publish(code, sse.Event{Type: "state_change", Data: `{"state":"lobby"}`})

	// Return refreshed game panel
	h.renderGamePanelPartial(w, code)
}



// QuestionAnswers holds a question and its answers for the review page.
type QuestionAnswers struct {
	Question models.Question
	Answers  []AnswerRow
}

// AnswerRow holds a single answer with team info for display.
type AnswerRow struct {
	AnswerID     int64
	TeamName     string
	AnswerText   string
	IsCorrect    sql.NullInt64
	HostApproved sql.NullInt64
	QuestionType string
	Points      int
	GameCode    string
}

// GetAnswerReview shows all answers for all questions in the current round.
func (h *AdminHandler) GetAnswerReview(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if !game.CurrentRoundID.Valid {
		http.Error(w, "No current round", http.StatusUnprocessableEntity)
		return
	}

	// Load questions for this round
	qRows, err := h.db.Query(`
		SELECT id, round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, COALESCE(video_group_id, 0)
		FROM questions WHERE round_id = ? ORDER BY order_index
	`, game.CurrentRoundID.Int64)
	if err != nil {
		http.Error(w, "Error loading questions", http.StatusInternalServerError)
		return
	}
	var questions []models.Question
	for qRows.Next() {
		var q models.Question
		qRows.Scan(&q.ID, &q.RoundID, &q.QuestionText, &q.QuestionType, &q.CorrectAnswer, &q.Options, &q.VideoFilename, &q.ImageFilename, &q.Points, &q.OrderIndex, &q.VideoGroupID)
		questions = append(questions, q)
	}
	qRows.Close()

	// Load answers for each question
	var qaList []QuestionAnswers
	for _, q := range questions {
		aRows, err := h.db.Query(`
			SELECT a.id, t.name, a.answer_text, a.is_correct, a.host_approved, q.question_type, q.points
			FROM answers a
			JOIN teams t ON a.team_id = t.id
			JOIN questions q ON a.question_id = q.id
			WHERE a.question_id = ?
			ORDER BY t.name
		`, q.ID)
		if err != nil {
			qaList = append(qaList, QuestionAnswers{Question: q})
			continue
		}
		var answers []AnswerRow
		for aRows.Next() {
			var ar AnswerRow
			aRows.Scan(&ar.AnswerID, &ar.TeamName, &ar.AnswerText, &ar.IsCorrect, &ar.HostApproved, &ar.QuestionType, &ar.Points)
			ar.GameCode = code
			answers = append(answers, ar)
		}
		aRows.Close()
		qaList = append(qaList, QuestionAnswers{Question: q, Answers: answers})
	}

	// Load round name
	var roundName string
	h.db.QueryRow("SELECT name FROM rounds WHERE id = ?", game.CurrentRoundID.Int64).Scan(&roundName)

	var quiz models.Quiz
	h.db.QueryRow("SELECT id, title, mode, created_at FROM quizzes WHERE id = ?", game.QuizID).Scan(&quiz.ID, &quiz.Title, &quiz.Mode, &quiz.CreatedAt)

	data := map[string]interface{}{
		"Game":           game,
		"Quiz":           quiz,
		"Code":           code,
		"RoundName":      roundName,
		"QuestionAnswers": qaList,
	}

	h.render(w, data, "answer_review.html", "templates/admin/answer_review.html", "templates/admin/partials/answer_row.html")
}

// PostApproveAnswer approves an open answer (sets is_correct=1, host_approved=1, scores it).
func (h *AdminHandler) PostApproveAnswer(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	game, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	answerID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid answer ID", http.StatusBadRequest)
		return
	}

	// Get question type and points
	var questionID int64
	var teamID int64
	var points int
	err = h.db.QueryRow(`
		SELECT a.question_id, a.team_id, q.points
		FROM answers a
		JOIN questions q ON a.question_id = q.id
		WHERE a.id = ?
	`, answerID).Scan(&questionID, &teamID, &points)
	if err != nil {
		http.Error(w, "Answer not found", http.StatusNotFound)
		return
	}

	// Mark answer as approved
	_, err = h.db.Exec(`
		UPDATE answers SET is_correct = 1, host_approved = 1, scored_at = datetime('now')
		WHERE id = ?
	`, answerID)
	if err != nil {
		http.Error(w, "Error approving answer", http.StatusInternalServerError)
		return
	}

	// Add points to team score
	_, err = h.db.Exec("UPDATE teams SET score = score + ? WHERE id = ?", points, teamID)
	if err != nil {
		log.Printf("Error updating team score: %v", err)
	}

	// Broadcast score_update
	publishScoreUpdate(h.db, h.broker, game)

	// Return updated answer row partial
	h.renderAnswerRow(w, answerID)
}

// PostDenyAnswer denies an open answer (sets is_correct=0, host_approved=0).
func (h *AdminHandler) PostDenyAnswer(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	_, err := h.getGame(code)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	answerID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid answer ID", http.StatusBadRequest)
		return
	}

	// Mark answer as denied
	_, err = h.db.Exec(`
		UPDATE answers SET is_correct = 0, host_approved = 0
		WHERE id = ?
	`, answerID)
	if err != nil {
		http.Error(w, "Error denying answer", http.StatusInternalServerError)
		return
	}

	// Return updated answer row partial
	h.renderAnswerRow(w, answerID)
}

// renderAnswerRow renders a single answer row for HTMX swap.
func (h *AdminHandler) renderAnswerRow(w http.ResponseWriter, answerID int64) {
	var ar AnswerRow
	err := h.db.QueryRow(`
		SELECT a.id, t.name, a.answer_text, a.is_correct, a.host_approved, q.question_type, q.points
		FROM answers a
		JOIN teams t ON a.team_id = t.id
		JOIN questions q ON a.question_id = q.id
		WHERE a.id = ?
	`, answerID).Scan(&ar.AnswerID, &ar.TeamName, &ar.AnswerText, &ar.IsCorrect, &ar.HostApproved, &ar.QuestionType, &ar.Points)
	if err != nil {
		http.Error(w, "Answer not found", http.StatusNotFound)
		return
	}

	// We need game code for HTMX URLs — look it up from the answer
	ar.GameCode = "" // Will be filled from game lookup
	var gameID int64
	h.db.QueryRow("SELECT game_id FROM teams WHERE id = (SELECT team_id FROM answers WHERE id = ?)", answerID).Scan(&gameID)
	var roomCode string
	h.db.QueryRow("SELECT room_code FROM games WHERE id = ?", gameID).Scan(&roomCode)
	ar.GameCode = roomCode

	w.Header().Set("Content-Type", "text/html")
	h.renderPartial(w, ar, "answer_row", "templates/admin/partials/answer_row.html")
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

func publishRoundRevealOffline(db *sql.DB, broker *sse.Broker, game *models.Game) {
	// Load all questions for the round (offline: just questions + answers, no team submissions)
	rows, err := db.Query(`
		SELECT id, question_text, question_type, correct_answer, options, points
		FROM questions WHERE round_id = ? ORDER BY order_index
	`, game.CurrentRoundID.Int64)
	if err != nil {
		log.Printf("Error loading questions for offline round reveal: %v", err)
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

	// Offline: just reveal questions without team answers
	revealJSON, _ := json.Marshal(questions)
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
		if question.ImageFilename.Valid {
			qData["image_filename"] = question.ImageFilename.String
		}
		// If the question belongs to a video group, include the group's video URL
		if question.VideoGroupID.Valid && question.VideoGroupID.Int64 != 0 {
			var vgFilename sql.NullString
			err := db.QueryRow("SELECT video_filename FROM video_groups WHERE id = ?", question.VideoGroupID.Int64).Scan(&vgFilename)
			if err == nil && vgFilename.Valid && vgFilename.String != "" {
				qData["video_url"] = "/static/videos/" + vgFilename.String
			}
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

// parseAdminMCOptions parses MC option JSON into a slice of strings for template rendering.
// Returns a slice of exactly 4 elements (empty string for missing options).
func parseAdminMCOptions(options sql.NullString) []string {
	result := []string{"", "", "", ""}
	if !options.Valid || options.String == "" {
		return result
	}
	var opts []string
	if err := json.Unmarshal([]byte(options.String), &opts); err != nil {
		return result
	}
	for i, opt := range opts {
		if i < 4 {
			result[i] = opt
		}
	}
	return result
}

// --- Video Group Handlers ---

// PostVideoGroup creates a new video group for a round.
func (h *AdminHandler) PostVideoGroup(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))

	// Get next order index for video groups in this round
	var maxOrder sql.NullInt64
	h.db.QueryRow("SELECT MAX(order_index) FROM video_groups WHERE round_id = ?", roundID).Scan(&maxOrder)
	orderIndex := 0
	if maxOrder.Valid {
		orderIndex = int(maxOrder.Int64) + 1
	}

	// Handle video upload
	var videoFilename sql.NullString
	file, header, err := r.FormFile("video_file")
	if err == nil {
		defer file.Close()

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

	result, err := h.db.Exec(`
		INSERT INTO video_groups (round_id, title, video_filename, order_index)
		VALUES (?, ?, ?, ?)
	`, roundID, title, videoFilename, orderIndex)
	if err != nil {
		log.Printf("Error creating video group: %v", err)
		http.Error(w, "Error creating video group", http.StatusInternalServerError)
		return
	}

	_ = result

	// Load the round to get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

// DeleteVideoGroup deletes a video group and nullifies its questions' video_group_id.
func (h *AdminHandler) DeleteVideoGroup(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}
	groupID, err := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	// Delete video file from disk if present
	var videoFilename sql.NullString
	h.db.QueryRow("SELECT video_filename FROM video_groups WHERE id = ?", groupID).Scan(&videoFilename)
	if videoFilename.Valid && videoFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", videoFilename.String))
	}

	// Nullify video_group_id on all questions in this group
	h.db.Exec("UPDATE questions SET video_group_id = NULL WHERE video_group_id = ?", groupID)

	// Delete the group
	h.db.Exec("DELETE FROM video_groups WHERE id = ?", groupID)

	// Get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

// PostVideoGroupVideo replaces the video file for a video group.
func (h *AdminHandler) PostVideoGroupVideo(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}
	groupID, err := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("video_file")
	if err != nil {
		http.Error(w, "No video file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Delete old video if exists
	var oldVideo sql.NullString
	h.db.QueryRow("SELECT video_filename FROM video_groups WHERE id = ?", groupID).Scan(&oldVideo)
	if oldVideo.Valid && oldVideo.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", oldVideo.String))
	}

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

	h.db.Exec("UPDATE video_groups SET video_filename = ? WHERE id = ?", filename, groupID)

	// Get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

// DeleteVideoGroupVideo removes the video file from a video group.
func (h *AdminHandler) DeleteVideoGroupVideo(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}
	groupID, err := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	var videoFilename sql.NullString
	err = h.db.QueryRow("SELECT video_filename FROM video_groups WHERE id = ?", groupID).Scan(&videoFilename)
	if err != nil {
		http.Error(w, "Video group not found", http.StatusNotFound)
		return
	}

	if videoFilename.Valid && videoFilename.String != "" {
		os.Remove(filepath.Join(h.dataDir, "videos", videoFilename.String))
		h.db.Exec("UPDATE video_groups SET video_filename = NULL WHERE id = ?", groupID)
	}

	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}

// PostQuestionInGroup creates a new question within a video group.
func (h *AdminHandler) PostQuestionInGroup(w http.ResponseWriter, r *http.Request) {
	roundID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid round ID", http.StatusBadRequest)
		return
	}
	groupID, err := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	questionText := strings.TrimSpace(r.FormValue("question_text"))
	questionType := r.FormValue("question_type")
	correctAnswer := strings.TrimSpace(r.FormValue("correct_answer"))
	pointsStr := r.FormValue("points")
	options := r.FormValue("options")

	// For offline quizzes, force question_type = 'open'
	var quizMode string
	h.db.QueryRow("SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id WHERE r.id = ?", roundID).Scan(&quizMode)
	if quizMode == "offline" {
		questionType = "open"
	}

	points := 1
	if pointsStr != "" {
		if p, err := strconv.Atoi(pointsStr); err == nil && p > 0 {
			points = p
		}
	}

	if questionText == "" {
		http.Error(w, "Question text required", http.StatusUnprocessableEntity)
		return
	}
	if quizMode != "offline" && correctAnswer == "" {
		http.Error(w, "Correct answer required", http.StatusUnprocessableEntity)
		return
	}

	// Get next order index for questions in this group
	var maxOrder sql.NullInt64
	h.db.QueryRow("SELECT MAX(order_index) FROM questions WHERE video_group_id = ?", groupID).Scan(&maxOrder)
	orderIndex := 0
	if maxOrder.Valid {
		orderIndex = int(maxOrder.Int64) + 1
	}

	// Handle image upload (video lives on the group, not the question)
	var imageFilename sql.NullString
	imgFile, imgHeader, err := r.FormFile("image_file")
	if err == nil {
		defer imgFile.Close()

		ext := filepath.Ext(imgHeader.Filename)
		if ext == "" {
			ext = ".png"
		}
		imgName := generateToken() + ext
		imgPath := filepath.Join(h.dataDir, "images", imgName)

		imgDst, err := os.Create(imgPath)
		if err != nil {
			log.Printf("Error creating image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}
		defer imgDst.Close()

		if _, err := io.Copy(imgDst, imgFile); err != nil {
			log.Printf("Error writing image file: %v", err)
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}

		imageFilename = sql.NullString{String: imgName, Valid: true}
	}

	// Handle options JSON for MC
	var optionsJSON sql.NullString
	if questionType == "multiple_choice" && options != "" {
		optionsJSON = sql.NullString{String: options, Valid: true}
	}

	result, err := h.db.Exec(`
		INSERT INTO questions (round_id, question_text, question_type, correct_answer, options, video_filename, image_filename, points, order_index, video_group_id)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)
	`, roundID, questionText, questionType, correctAnswer, optionsJSON, imageFilename, points, orderIndex, groupID)
	if err != nil {
		log.Printf("Error creating question in group: %v", err)
		http.Error(w, "Error creating question", http.StatusInternalServerError)
		return
	}

	_ = result

	// Get quiz_id for redirect
	var quizID int64
	h.db.QueryRow("SELECT quiz_id FROM rounds WHERE id = ?", roundID).Scan(&quizID)

	http.Redirect(w, r, fmt.Sprintf("/admin/quiz/%d", quizID), http.StatusSeeOther)
}
package models

import "database/sql"

type Quiz struct {
	ID        int64
	Title     string
	Mode      string
	CreatedAt string
}

type Round struct {
	ID         int64
	QuizID     int64
	Name       string
	Type       string // "text" or "video"
	OrderIndex int
}

type Question struct {
	ID            int64
	RoundID       int64
	QuestionText  string
	QuestionType  string // "open", "ranged", "multiple_choice"
	CorrectAnswer string
	Options       sql.NullString
	VideoFilename sql.NullString
	ImageFilename sql.NullString
	Points        int
	OrderIndex    int
	VideoGroupID  sql.NullInt64
}

type VideoGroup struct {
	ID            int64
	RoundID       int64
	Title         string
	VideoFilename sql.NullString
	OrderIndex    int
}

// VideoGroupWithQuestions is used in templates to render a group with its questions.
type VideoGroupWithQuestions struct {
	Group     VideoGroup
	Questions []Question
}

type Game struct {
	ID               int64
	QuizID           int64
	RoomCode         string
	State            string // "lobby", "question", "round_reveal", "ended"
	CurrentQuestionID sql.NullInt64
	CurrentRoundID   sql.NullInt64
	ShowQuestion     int
	CreatedAt        string
}

type Team struct {
	ID     int64
	GameID int64
	Name   string
	Score  int
}

type Player struct {
	ID         int64
	TeamID     int64
	Name       sql.NullString
	IsHead     int
	LastSeenAt string
	JoinedAt   string
}

type Answer struct {
	ID           int64
	TeamID       int64
	QuestionID   int64
	AnswerText   string
	IsCorrect    sql.NullInt64
	ScoredAt     sql.NullString
	HostApproved sql.NullInt64
}

type AdminSession struct {
	ID        int64
	Token     string
	CreatedAt string
	ExpiresAt string
}

// RoundData is used in templates to render a round with its video groups and questions.
type RoundData struct {
	Round               Round
	Questions           []Question
	VideoGroups         []VideoGroupWithQuestions
	UngroupedQuestions  []Question
}
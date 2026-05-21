package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens the SQLite database at dataDir/popquiz.db, enables WAL mode
// and foreign keys, and runs all migrations (CREATE TABLE IF NOT EXISTS).
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "videos"), 0755); err != nil {
		return nil, fmt.Errorf("creating videos directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "popquiz.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode and foreign keys
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS quizzes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS rounds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quiz_id INTEGER NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('text', 'video')),
			order_index INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS questions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			round_id INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
			question_text TEXT NOT NULL,
			question_type TEXT NOT NULL CHECK(question_type IN ('open', 'ranged', 'multiple_choice')),
			correct_answer TEXT NOT NULL,
			options TEXT,
			video_filename TEXT,
			points INTEGER NOT NULL DEFAULT 1,
			order_index INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS games (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quiz_id INTEGER NOT NULL REFERENCES quizzes(id),
			room_code TEXT NOT NULL UNIQUE,
			state TEXT NOT NULL DEFAULT 'lobby' CHECK(state IN ('lobby', 'question', 'round_reveal', 'ended')),
			current_question_id INTEGER REFERENCES questions(id),
			current_round_id INTEGER REFERENCES rounds(id),
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS teams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			score INTEGER NOT NULL DEFAULT 0,
			UNIQUE(game_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS players (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			is_head INTEGER NOT NULL DEFAULT 0 CHECK(is_head IN (0, 1)),
			last_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
			joined_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS answers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
			question_id INTEGER NOT NULL REFERENCES questions(id),
			answer_text TEXT NOT NULL,
			is_correct INTEGER CHECK(is_correct IN (0, 1)),
			scored_at TEXT,
			UNIQUE(team_id, question_id)
		)`,
		`CREATE TABLE IF NOT EXISTS admin_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at TEXT NOT NULL
		)`,
	}

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_rounds_quiz_id ON rounds(quiz_id)`,
		`CREATE INDEX IF NOT EXISTS idx_questions_round_id ON questions(round_id)`,
		`CREATE INDEX IF NOT EXISTS idx_games_room_code ON games(room_code)`,
		`CREATE INDEX IF NOT EXISTS idx_games_quiz_id ON games(quiz_id)`,
		`CREATE INDEX IF NOT EXISTS idx_teams_game_id ON teams(game_id)`,
		`CREATE INDEX IF NOT EXISTS idx_players_team_id ON players(team_id)`,
		`CREATE INDEX IF NOT EXISTS idx_players_is_head ON players(is_head)`,
		`CREATE INDEX IF NOT EXISTS idx_answers_team_question ON answers(team_id, question_id)`,
		`CREATE INDEX IF NOT EXISTS idx_answers_question_id ON answers(question_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_sessions_token ON admin_sessions(token)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires ON admin_sessions(expires_at)`,
	}

	for _, m := range migrations {
		if _, err := tx.Exec(m); err != nil {
			return fmt.Errorf("executing migration: %s: %w", m, err)
		}
	}
	for _, idx := range indexes {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("executing index: %s: %w", idx, err)
		}
	}

	return tx.Commit()
}
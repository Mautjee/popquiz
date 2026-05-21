# Data Model: PopQuiz Live Quiz Night

**Created**: 2026-05-21

## Entity-Relationship Diagram

```
quizzes 1──* rounds 1──* questions
  │
  └──* games 1──* teams 1──* players
                        │
                        └──* answers ──* questions

admin_sessions (standalone)
```

## Tables

### quizzes

| Column     | Type    | Constraints                          | Notes              |
|------------|---------|---------------------------------------|--------------------|
| id         | INTEGER | PK AUTOINCREMENT                      |                    |
| title      | TEXT    | NOT NULL                              |                    |
| created_at | TEXT    | NOT NULL DEFAULT (datetime('now'))    | ISO 8601 timestamp |

### rounds

| Column      | Type    | Constraints                          | Notes                         |
|-------------|---------|---------------------------------------|-------------------------------|
| id          | INTEGER | PK AUTOINCREMENT                      |                               |
| quiz_id     | INTEGER | NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE |                 |
| name        | TEXT    | NOT NULL                              | e.g. "Round 1 — Geography"    |
| type        | TEXT    | NOT NULL CHECK(type IN ('text','video')) |                                |
| order_index | INTEGER | NOT NULL DEFAULT 0                    | 0-based ordering within quiz   |

### questions

| Column         | Type    | Constraints                                      | Notes                                    |
|----------------|---------|--------------------------------------------------|------------------------------------------|
| id             | INTEGER | PK AUTOINCREMENT                                  |                                          |
| round_id       | INTEGER | NOT NULL REFERENCES rounds(id) ON DELETE CASCADE |                                          |
| question_text  | TEXT    | NOT NULL                                          |                                          |
| question_type  | TEXT    | NOT NULL CHECK(question_type IN ('open','ranged','multiple_choice')) |                  |
| correct_answer | TEXT    | NOT NULL                                          | For ranged: numeric string; for MC: "A"/"B"/"C"/"D" |
| options        | TEXT    | NULL                                              | JSON array, only for multiple_choice     |
| video_filename | TEXT    | NULL                                              | Only for video rounds                     |
| points         | INTEGER | NOT NULL DEFAULT 1                                |                                          |
| order_index    | INTEGER | NOT NULL DEFAULT 0                                | 0-based ordering within round             |

### games

| Column              | Type    | Constraints                                      | Notes                                         |
|---------------------|---------|--------------------------------------------------|-----------------------------------------------|
| id                  | INTEGER | PK AUTOINCREMENT                                  |                                               |
| quiz_id             | INTEGER | NOT NULL REFERENCES quizzes(id)                  |                                               |
| room_code           | TEXT    | NOT NULL UNIQUE                                   | 6-char uppercase alphanumeric                 |
| state               | TEXT    | NOT NULL DEFAULT 'lobby' CHECK(state IN ('lobby','question','round_reveal','ended')) | |
| current_question_id | INTEGER | NULL REFERENCES questions(id)                    | Currently active question                      |
| current_round_id    | INTEGER | NULL REFERENCES rounds(id)                       | Currently active round                         |
| created_at          | TEXT    | NOT NULL DEFAULT (datetime('now'))               | ISO 8601 timestamp                            |

### teams

| Column | Type    | Constraints                          | Notes                      |
|--------|---------|--------------------------------------|----------------------------|
| id     | INTEGER | PK AUTOINCREMENT                     |                            |
| game_id| INTEGER | NOT NULL REFERENCES games(id) ON DELETE CASCADE |                  |
| name   | TEXT    | NOT NULL                              | Unique per game            |
| score  | INTEGER | NOT NULL DEFAULT 0                    | Running total, updated per round_reveal |

**Unique constraint**: (game_id, name)

### players

| Column       | Type    | Constraints                          | Notes                      |
|--------------|---------|--------------------------------------|----------------------------|
| id           | INTEGER | PK AUTOINCREMENT                     |                            |
| team_id      | INTEGER | NOT NULL REFERENCES teams(id) ON DELETE CASCADE |                  |
| name         | TEXT    | NOT NULL                              | Display name               |
| is_head      | INTEGER | NOT NULL DEFAULT 0 CHECK(is_head IN (0,1)) | 1 = Team Head        |
| last_seen_at | TEXT    | NOT NULL DEFAULT (datetime('now'))    | Updated on each SSE ping/request |
| joined_at    | TEXT    | NOT NULL DEFAULT (datetime('now'))    | Used for head promotion order  |

### answers

| Column       | Type    | Constraints                                      | Notes                       |
|--------------|---------|--------------------------------------------------|----------------------------|
| id           | INTEGER | PK AUTOINCREMENT                                  |                             |
| team_id      | INTEGER | NOT NULL REFERENCES teams(id) ON DELETE CASCADE  |                             |
| question_id  | INTEGER | NOT NULL REFERENCES questions(id)                |                             |
| answer_text  | TEXT    | NOT NULL                                          | Always stored as string     |
| is_correct   | INTEGER | NULL CHECK(is_correct IN (0,1))                   | NULL until scored           |
| scored_at    | TEXT    | NULL                                              | ISO 8601, set when scored   |

**Unique constraint**: (team_id, question_id) — each team can only have one answer per question

### admin_sessions

| Column     | Type    | Constraints                          | Notes                  |
|------------|---------|--------------------------------------|------------------------|
| id         | INTEGER | PK AUTOINCREMENT                     |                        |
| token      | TEXT    | NOT NULL UNIQUE                      | Random secure token    |
| created_at | TEXT    | NOT NULL DEFAULT (datetime('now'))   |                        |
| expires_at | TEXT    | NOT NULL                              | 24h TTL from created_at |

## Indexes

```sql
CREATE INDEX idx_rounds_quiz_id ON rounds(quiz_id);
CREATE INDEX idx_questions_round_id ON questions(round_id);
CREATE INDEX idx_games_room_code ON games(room_code);
CREATE INDEX idx_games_quiz_id ON games(quiz_id);
CREATE INDEX idx_teams_game_id ON teams(game_id);
CREATE INDEX idx_teams_game_id_name ON teams(game_id, name);
CREATE INDEX idx_players_team_id ON players(team_id);
CREATE INDEX idx_players_is_head ON players(is_head);
CREATE INDEX idx_answers_team_question ON answers(team_id, question_id);
CREATE INDEX idx_answers_question_id ON answers(question_id);
CREATE INDEX idx_admin_sessions_token ON admin_sessions(token);
CREATE INDEX idx_admin_sessions_expires ON admin_sessions(expires_at);
```

## Relationships Summary

- **Quiz → Rounds**: One-to-many, CASCADE DELETE
- **Round → Questions**: One-to-many, CASCADE DELETE
- **Quiz → Games**: One-to-many (a quiz can be played multiple times)
- **Game → Teams**: One-to-many, CASCADE DELETE
- **Team → Players**: One-to-many, CASCADE DELETE
- **Team → Answers**: One-to-many, CASCADE DELETE
- **Question → Answers**: One-to-many (no cascade — answers reference questions)

## State Machine for Game

```
lobby → question → round_reveal → question (next round) → ... → round_reveal → ended
```

Transitions:
- `lobby` → `question`: Host clicks "Start Round"
- `question` → `question`: Host clicks "Next Question" (within same round)
- `question` → `round_reveal`: Host clicks "End Round"
- `round_reveal` → `question`: Host clicks "Start Next Round"
- `round_reveal` → `ended`: Host clicks "End Game"
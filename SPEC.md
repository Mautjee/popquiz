# PopQuiz — Application Specification

> Version: 0.1 (draft)
> Last updated: 2025-05-21
> Status: In review

---

## 1. Overview

PopQuiz is a self-hosted web app for running live quiz nights.
A host creates a quiz with multiple rounds, shares a room code, and players join on their own devices.
The host advances the quiz question by question; players submit answers in real time.
One special round type ("video") plays a short video clip per question before the player answers.

---

## 2. Roles

| Role   | Description |
|--------|-------------|
| Host   | Creates quizzes, manages rounds/questions, runs live games via the admin panel |
| Player | Joins a game via room code, submits answers during the game |

No authentication in v1 — the admin panel is unprotected (localhost deploy).

---

## 3. Data Model

### quizzes
| Column     | Type    | Notes |
|------------|---------|-------|
| id         | INTEGER | PK autoincrement |
| title      | TEXT    | required |
| created_at | DATETIME| default now |

### rounds
| Column      | Type    | Notes |
|-------------|---------|-------|
| id          | INTEGER | PK autoincrement |
| quiz_id     | INTEGER | FK quizzes.id |
| name        | TEXT    | e.g. "Round 1 — Geography" |
| type        | TEXT    | "text" or "video" |
| order_index | INTEGER | 0-based ordering within quiz |

### questions
| Column         | Type    | Notes |
|----------------|---------|-------|
| id             | INTEGER | PK autoincrement |
| round_id       | INTEGER | FK rounds.id |
| question_text  | TEXT    | required |
| correct_answer | TEXT    | required |
| video_filename | TEXT    | nullable — only for video rounds |
| points         | INTEGER | default 1 |
| order_index    | INTEGER | 0-based ordering within round |

### games
| Column              | Type    | Notes |
|---------------------|---------|-------|
| id                  | INTEGER | PK autoincrement |
| quiz_id             | INTEGER | FK quizzes.id |
| room_code           | TEXT    | 6-char uppercase, unique |
| state               | TEXT    | "lobby" / "question" / "answer_reveal" / "ended" |
| current_question_id | INTEGER | nullable FK questions.id |
| created_at          | DATETIME| default now |

### players
| Column  | Type    | Notes |
|---------|---------|-------|
| id      | INTEGER | PK autoincrement |
| game_id | INTEGER | FK games.id |
| name    | TEXT    | required |
| score   | INTEGER | default 0 |

### answers
| Column       | Type    | Notes |
|--------------|---------|-------|
| id           | INTEGER | PK autoincrement |
| player_id    | INTEGER | FK players.id |
| question_id  | INTEGER | FK questions.id |
| answer_text  | TEXT    | player's submitted answer |
| is_correct   | INTEGER | 0/1, set by host during reveal |
| submitted_at | DATETIME| default now |

---

## 4. Routes

### Public / Player

| Method | Path                    | Description |
|--------|-------------------------|-------------|
| GET    | /                       | Join page: enter room code + player name |
| POST   | /join                   | Creates player record, redirects to /game/:code |
| GET    | /game/:code             | Player game view — SSE-driven |
| POST   | /game/:code/answer      | Submit answer for current question |
| GET    | /game/:code/results     | Final leaderboard (accessible once game.state = "ended") |

### Admin / Host

| Method | Path                          | Description |
|--------|-------------------------------|-------------|
| GET    | /admin                        | List all quizzes |
| GET    | /admin/quiz/new               | New quiz form |
| POST   | /admin/quiz                   | Create quiz |
| GET    | /admin/quiz/:id               | Edit quiz — rounds + questions inline |
| POST   | /admin/quiz/:id/round         | Add round to quiz |
| DELETE | /admin/round/:id              | Delete round (and its questions) |
| POST   | /admin/round/:id/question     | Add question to round (multipart for video upload) |
| DELETE | /admin/question/:id           | Delete question |
| POST   | /admin/quiz/:id/game          | Create a new game session for this quiz |
| GET    | /admin/game/:code             | Host control panel |
| POST   | /admin/game/:code/next        | Advance to next question (or end game) |
| POST   | /admin/game/:code/reveal      | Reveal correct answer + mark answers correct/incorrect |
| GET    | /admin/game/:code/events      | SSE stream for admin panel (incoming answers) |

### Static

| Path                     | Description |
|--------------------------|-------------|
| /static/*                | CSS, JS assets |
| /static/videos/:filename | Uploaded video clips |

---

## 5. Page Specifications

### 5.1 Join Page (/)

- Single centered card
- Input: room code (auto-uppercase, max 6 chars)
- Input: player name (max 32 chars)
- Button: "Join Quiz"
- On submit → POST /join → redirect to /game/:code
- Error states: room not found, game already ended, name already taken in this game

### 5.2 Player Game View (/game/:code)

States driven by game.state via SSE:

**lobby**
- Shows "Waiting for host to start..." + player list

**question**
- Shows question number and round name at top
- If round.type = "video": renders `<video>` tag with autoplay, shows question text below
- If round.type = "text": shows question text prominently
- Answer input + submit button
- Once answer submitted: input disabled, shows "Answer submitted! Waiting..."
- Points value shown

**answer_reveal**
- Shows correct answer
- Shows whether player's answer was marked correct or incorrect
- Score update (animated)

**ended**
- Shows "Quiz over!" + link to /game/:code/results

### 5.3 Player Results (/game/:code/results)

- Leaderboard table sorted by score descending
- Columns: Rank, Name, Score
- Current player's row highlighted

### 5.4 Admin Quiz List (/admin)

- Table of quizzes: title, created date, # rounds, # questions, actions
- Actions: Edit, Start Game, Delete
- "New Quiz" button at top

### 5.5 Admin Quiz Editor (/admin/quiz/:id)

- Quiz title (editable inline via HTMX)
- Rounds listed in order, each expandable to show questions
- "Add Round" button → inline form (name, type: text/video)
- Per round: "Add Question" button → inline form
  - Question text (textarea)
  - Correct answer (text input)
  - Points (number, default 1)
  - If round.type = "video": file input for video clip (accept="video/*")
- Questions show: order, text, answer, points, delete button
- Drag-to-reorder (rounds and questions) — nice to have, skip for v1

### 5.6 Admin Host Panel (/admin/game/:code)

- Room code displayed prominently (for sharing)
- Player list with live join count (SSE)
- Current question displayed
- "Next Question" button (disabled until current state allows)
- "Reveal Answer" button (appears after question state)
- During answer_reveal: list of player answers with "Mark Correct" / "Mark Incorrect" toggles
- Score totals per player shown live

---

## 6. Video Handling

- Accepted formats: mp4, webm, mov
- Max file size: 200MB per clip (enforced server-side)
- Files stored at: ./data/videos/<uuid>_<original_filename>
- UUID prefix prevents collisions
- Served at: /static/videos/:filename
- HTML5 `<video controls autoplay>` tag used in player view
- No transcoding — clips must be web-compatible (h264 mp4 recommended)
- If video fails to load: show error message "Video unavailable" and still show question text

---

## 7. SSE (Server-Sent Events)

### Player stream: GET /game/:code/events

Events:
- `state_change` — payload: current game state JSON (state, question, round_type, video_filename, question_text, points)
- `score_update` — payload: player's current score
- `answer_reveal` — payload: correct answer, is_correct for this player

### Admin stream: GET /admin/game/:code/events

Events:
- `player_joined` — payload: player name + total count
- `answer_submitted` — payload: player name, answer text, question_id

Client reconnects automatically (EventSource handles this natively).

---

## 8. Tech Stack

| Layer      | Choice |
|------------|--------|
| Language   | Go 1.22+ |
| Router     | github.com/go-chi/chi/v5 |
| DB         | SQLite via github.com/mattn/go-sqlite3 |
| Templates  | html/template (Go stdlib) |
| Frontend   | HTMX 2.x via CDN |
| Styling    | Tailwind CSS via CDN play.tailwindcss.com |
| Video      | HTML5 native, no JS player library |

---

## 9. Project Structure

```
popquiz/
├── cmd/
│   └── server/
│       └── main.go          # Entry point, wires router + DB, listens on :8080
├── internal/
│   ├── db/
│   │   └── db.go            # SQLite open, schema migration (CREATE TABLE IF NOT EXISTS)
│   ├── models/
│   │   └── models.go        # Structs for all DB tables
│   ├── handlers/
│   │   ├── admin.go         # All /admin/* routes
│   │   ├── game.go          # All /game/* routes
│   │   └── join.go          # / and /join routes
│   └── sse/
│       └── sse.go           # SSE broker: subscribe/publish per room_code
├── templates/
│   ├── base.html            # Layout with nav, HTMX CDN, Tailwind CDN
│   ├── join.html
│   ├── game/
│   │   ├── player.html
│   │   └── results.html
│   └── admin/
│       ├── index.html
│       ├── quiz_editor.html
│       └── game_panel.html
├── static/                  # Any local static assets
│   └── app.js               # Minimal JS (SSE connection setup)
├── data/
│   └── videos/              # Uploaded video clips (gitignored)
├── .gitignore
├── go.mod
├── go.sum
├── AGENTS.md
└── README.md
```

---

## 10. Non-Functional Requirements

- Runs as a single binary on Linux (arm64 and amd64)
- No external services (no S3, no Redis, no message queue)
- Graceful shutdown on SIGINT/SIGTERM (drain SSE connections)
- All DB operations use transactions where multiple writes occur
- PORT env var overrides default :8080
- Data directory configurable via DATA_DIR env var (default: ./data)

---

## 11. Out of Scope (v1)

- Authentication / login for admin
- Multiple hosts per quiz
- Timer per question
- Drag-to-reorder questions
- Mobile-optimised video player (basic HTML5 is sufficient)
- Image-based questions
- Team mode
- Export results to CSV

---

## 12. Acceptance Criteria

- [ ] Host can create a quiz with at least 2 rounds (1 text, 1 video)
- [ ] Host can upload a video clip and preview it in the editor
- [ ] Player can join via room code on a separate device
- [ ] Video autoplay works on player view when question is shown
- [ ] Host can advance through all questions and end the game
- [ ] Leaderboard shows correct final scores
- [ ] App compiles with `go build ./...` with zero errors
- [ ] App starts and serves on :8080 with no panics

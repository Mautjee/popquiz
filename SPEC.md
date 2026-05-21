# PopQuiz — Application Specification

> Version: 0.2
> Last updated: 2025-05-21
> Status: In review

---

## 1. Overview

PopQuiz is a self-hosted web app for running live quiz nights with teams.
A host creates a quiz with multiple rounds, shares a room code, and players join on their own devices.
Players form teams — each team has one "Team Head" who submits answers on behalf of the team.
All team members can see the question and video on their own phone, but only the Team Head can submit.
The host advances the quiz question by question; scoring is automatic or manual depending on question type.

---

## 2. Roles

| Role        | Description |
|-------------|-------------|
| Host        | Creates quizzes, manages content, runs the live game via the admin panel |
| Team Head   | First member of a team (or promoted automatically). Submits answers for the team |
| Team Member | Joins an existing team. Sees all content, cannot submit answers |

No authentication in v1 — the admin panel is unprotected (localhost deploy).

---

## 3. Question Types

### 3.1 Open Question
- Players see a text input
- Team Head types a free-text answer and submits
- During answer reveal, Host sees all team answers and manually marks each correct/incorrect
- Points awarded: full points if marked correct, 0 if incorrect

### 3.2 Ranged Question
- Players see a number input
- Team Head submits a numeric guess
- Scoring is automatic: the team(s) closest to the correct number win full points
- Tie rule: if two or more teams are equally close, all tied teams receive full points
- All other teams receive 0 points

### 3.3 Multiple Choice Question
- Question has 2–4 options (A, B, C, D)
- Players see the options as buttons
- Team Head selects one option and submits
- Scoring is automatic: correct option = full points, wrong = 0

---

## 4. Teams & Group System

- A game has teams, not individual players
- Each player joins a game by entering: room code + team name + their own name
- If the team name doesn't exist yet, it is created and the joiner becomes Team Head
- If the team name already exists, the player joins as a Team Member
- Team Head is indicated by a crown/star marker next to their name on all screens
- If the Team Head leaves (closes tab / disconnects for >30s), the next member in join order is promoted to Team Head automatically
- If a team becomes empty and someone joins with that team name, they become the new Team Head
- Player state lives server-side — refreshing or reopening the page rejoins the game by re-entering team name

### Team size
- Expected: ~25 players across ~5–6 teams (~4–5 people per team)

---

## 5. Data Model

### quizzes
| Column     | Type     | Notes |
|------------|----------|-------|
| id         | INTEGER  | PK autoincrement |
| title      | TEXT     | required |
| created_at | DATETIME | default now |

### rounds
| Column      | Type    | Notes |
|-------------|---------|-------|
| id          | INTEGER | PK autoincrement |
| quiz_id     | INTEGER | FK quizzes.id |
| name        | TEXT    | e.g. "Round 1 — Geography" |
| type        | TEXT    | "text" or "video" |
| order_index | INTEGER | 0-based ordering within quiz |

### questions
| Column          | Type    | Notes |
|-----------------|---------|-------|
| id              | INTEGER | PK autoincrement |
| round_id        | INTEGER | FK rounds.id |
| question_text   | TEXT    | required |
| question_type   | TEXT    | "open" / "ranged" / "multiple_choice" |
| correct_answer  | TEXT    | required — for ranged: numeric string; for multiple_choice: "A"/"B"/"C"/"D" |
| options         | TEXT    | JSON array of strings, nullable — only for multiple_choice (e.g. ["Paris","London","Berlin","Madrid"]) |
| video_filename  | TEXT    | nullable — only for video rounds |
| points          | INTEGER | default 1 |
| order_index     | INTEGER | 0-based ordering within round |

### games
| Column              | Type    | Notes |
|---------------------|---------|-------|
| id                  | INTEGER | PK autoincrement |
| quiz_id             | INTEGER | FK quizzes.id |
| room_code           | TEXT    | 6-char uppercase, unique |
| state               | TEXT    | "lobby" / "question" / "answer_reveal" / "ended" |
| current_question_id | INTEGER | nullable FK questions.id |
| created_at          | DATETIME| default now |

### teams
| Column  | Type    | Notes |
|---------|---------|-------|
| id      | INTEGER | PK autoincrement |
| game_id | INTEGER | FK games.id |
| name    | TEXT    | required, unique per game |
| score   | INTEGER | default 0 |

### players
| Column       | Type    | Notes |
|--------------|---------|-------|
| id           | INTEGER | PK autoincrement |
| team_id      | INTEGER | FK teams.id |
| name         | TEXT    | display name |
| is_head      | INTEGER | 0/1 — whether this player is Team Head |
| last_seen_at | DATETIME| updated on each request/SSE ping — used for disconnect detection |
| joined_at    | DATETIME| default now — used for head promotion order |

### answers
| Column       | Type    | Notes |
|--------------|---------|-------|
| id           | INTEGER | PK autoincrement |
| team_id      | INTEGER | FK teams.id |
| question_id  | INTEGER | FK questions.id |
| answer_text  | TEXT    | submitted answer (always stored as text) |
| is_correct   | INTEGER | 0/1, NULL until scored |
| scored_at    | DATETIME| nullable |

---

## 6. Routes

### Public / Player

| Method | Path                    | Description |
|--------|-------------------------|-------------|
| GET    | /                       | Join page: enter room code, team name, player name |
| POST   | /join                   | Find/create team, create player, set session cookie, redirect to /game/:code |
| GET    | /game/:code             | Player/team view — SSE driven |
| POST   | /game/:code/answer      | Team Head submits answer for current question |
| GET    | /game/:code/results     | Final leaderboard (accessible once game.state = "ended") |

### Admin / Host

| Method | Path                           | Description |
|--------|--------------------------------|-------------|
| GET    | /admin                         | List all quizzes |
| GET    | /admin/quiz/new                | New quiz form |
| POST   | /admin/quiz                    | Create quiz |
| GET    | /admin/quiz/:id                | Edit quiz — rounds + questions inline |
| POST   | /admin/quiz/:id/round          | Add round to quiz |
| DELETE | /admin/round/:id               | Delete round (cascades to questions) |
| POST   | /admin/round/:id/question      | Add question (multipart for video upload) |
| DELETE | /admin/question/:id            | Delete question |
| POST   | /admin/quiz/:id/game           | Create new game session |
| GET    | /admin/game/:code              | Host control panel |
| POST   | /admin/game/:code/next         | Advance to next question or end game |
| POST   | /admin/game/:code/reveal       | Reveal correct answer (triggers auto-scoring for ranged/mc) |
| POST   | /admin/game/:code/mark         | Manually mark a team's open answer correct/incorrect |
| GET    | /admin/game/:code/events       | SSE stream for admin panel |

### Static

| Path                      | Description |
|---------------------------|-------------|
| /static/*                 | CSS, JS assets |
| /static/videos/:filename  | Uploaded video clips |

---

## 7. Page Specifications

### 7.1 Join Page (/)

- Single centered card
- Input: room code (auto-uppercase, 6 chars)
- Input: team name (max 32 chars) — with hint "Create a new team or join an existing one"
- Input: your name (max 32 chars)
- Button: "Join Quiz"
- On submit: POST /join → set session cookie → redirect to /game/:code
- Error states: room not found, game already ended, name already taken within that team

### 7.2 Player Game View (/game/:code)

Header (always visible):
- Team name + crown icon if player is Team Head
- Current team score
- Round name

States driven by SSE (game.state):

**lobby**
- "Waiting for host to start..." message
- Live list of all teams and their members, with head marker

**question**
- Round name + question number at top
- If round.type = "video":
  - `<video>` tag with controls, NO autoplay, replay disabled (controlsList="nodownload nofullscreen" + JS to prevent seeking back after end)
  - Question text shown below video
- If round.type = "text":
  - Question text shown prominently
- Question type determines input shown (Team Head only):
  - open: textarea
  - ranged: number input
  - multiple_choice: A/B/C/D buttons
- Team Members see the question/video but the answer area shows "Waiting for your Team Head to answer..."
- Once Team Head submits: answer input replaced with "Answer submitted! Waiting for other teams..."
- Points value shown

**answer_reveal**
- Correct answer displayed prominently
- Per team: their submitted answer + correct/incorrect indicator
- For open questions: host marks each team live, indicator updates via SSE
- Score delta shown (e.g. "+1 point")
- Running leaderboard shown below

**ended**
- "Quiz over!" + link to /game/:code/results

### 7.3 Player Results (/game/:code/results)

- Leaderboard table sorted by score descending
- Columns: Rank, Team Name, Members, Score
- Current player's team row highlighted

### 7.4 Admin Quiz List (/admin)

- Table: title, created date, # rounds, # questions, actions
- Actions: Edit, Start Game
- "New Quiz" button

### 7.5 Admin Quiz Editor (/admin/quiz/:id)

- Quiz title (editable inline)
- Rounds listed in order with questions inside
- "Add Round" → inline form: name, type (text/video)
- "Add Question" → inline form:
  - Question text
  - Question type: open / ranged / multiple choice
  - Correct answer
  - If multiple_choice: 2–4 option inputs (Option A, B, C, D)
  - Points (default 1)
  - If round.type = video: file input (accept="video/*")
- Delete buttons for rounds and questions

### 7.6 Admin Host Panel (/admin/game/:code)

- Room code shown prominently (for sharing)
- Live team list with member count and head names (SSE)
- Current question + type displayed
- Answer submissions streaming in live (SSE) — team name + answer
- "Reveal Answer" button — reveals correct answer, auto-scores ranged/mc
- For open questions during reveal: each team's answer with "Correct" / "Incorrect" toggle buttons
- "Next Question" button — advances game
- Running scores per team

---

## 8. Video Handling

- Accepted formats: mp4, webm, mov
- Max file size: 200MB per clip (enforced server-side)
- Files stored at: ./data/videos/<uuid>_<original_filename>
- Served at: /static/videos/:filename
- Player controls: play button only — no autoplay, replay disabled via JS after video ends
- If video fails to load: show "Video unavailable" message, question text still shown

---

## 9. Scoring Rules

### Multiple Choice
- Automatic on reveal
- Correct team: +points, wrong team: +0

### Ranged
- Automatic on reveal
- Find minimum absolute difference between all submitted answers and correct_answer
- All teams with that minimum difference: +points
- All others: +0

### Open
- Manual — host marks each team correct/incorrect during answer_reveal state
- Correct: +points, Incorrect: +0
- Score updates pushed via SSE to all players immediately when host marks

---

## 10. SSE Streams

### Player stream: GET /game/:code/events

| Event          | Payload |
|----------------|---------|
| state_change   | game state, current question (text, type, options, video_filename, points, round_type) |
| answer_reveal  | correct answer, each team's answer, is_correct per team |
| score_update   | team scores map |
| head_change    | new Team Head player_id for a given team |
| player_joined  | team name, player name, is_head |

### Admin stream: GET /admin/game/:code/events

| Event            | Payload |
|------------------|---------|
| player_joined    | player name, team name, total team count |
| answer_submitted | team name, answer_text, question_id |
| all_answered     | fires when every team has submitted (prompt host to reveal) |

---

## 11. Team Head Promotion

- Server runs a background goroutine (or checks on each request) for disconnected heads
- A player is considered disconnected if last_seen_at < now - 30s
- On disconnect of Team Head: promote player with lowest joined_at in same team who is still active
- Push head_change SSE event to all players in that game
- If team becomes empty: no action — next joiner to that team name becomes head

---

## 12. Tech Stack

| Layer     | Choice |
|-----------|--------|
| Language  | Go 1.22+ |
| Router    | github.com/go-chi/chi/v5 |
| DB        | SQLite via github.com/mattn/go-sqlite3 |
| Templates | html/template (Go stdlib) |
| Frontend  | HTMX 2.x via CDN |
| Styling   | Tailwind CSS via CDN |
| Video     | HTML5 native, no JS player library |
| Session   | Cookie-based (team_id + player_id stored server-side in sessions table or signed cookie) |

---

## 13. Project Structure

```
popquiz/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── db/
│   │   └── db.go              # SQLite open, schema, migrations
│   ├── models/
│   │   └── models.go          # All DB structs
│   ├── handlers/
│   │   ├── admin.go
│   │   ├── game.go
│   │   └── join.go
│   ├── scoring/
│   │   └── scoring.go         # Ranged + MC auto-scoring logic
│   └── sse/
│       └── sse.go             # SSE broker per room_code
├── templates/
│   ├── base.html
│   ├── join.html
│   ├── game/
│   │   ├── player.html
│   │   └── results.html
│   └── admin/
│       ├── index.html
│       ├── quiz_editor.html
│       └── game_panel.html
├── static/
│   └── app.js                 # SSE setup, video replay prevention
├── data/
│   └── videos/                # gitignored
├── SPEC.md
├── AGENTS.md
├── .gitignore
├── go.mod
└── go.sum
```

---

## 14. Non-Functional Requirements

- Single binary, no external services
- Graceful shutdown on SIGINT/SIGTERM
- PORT env var (default :8080)
- DATA_DIR env var (default ./data)
- All multi-write DB operations use transactions

---

## 15. Out of Scope (v1)

- Admin authentication
- Timer per question
- Drag-to-reorder questions/rounds
- Image-based questions
- Export results to CSV
- Team chat

---

## 16. Acceptance Criteria

- [ ] Host can create a quiz with open, ranged, and multiple choice questions
- [ ] Host can create a video round and upload a clip
- [ ] ~25 players across ~6 teams can join simultaneously without issues
- [ ] Video plays only when player presses play; cannot be replayed after it ends
- [ ] Team Head submission is the only answer recorded per team
- [ ] Team Head is auto-promoted when current head disconnects
- [ ] Ranged scoring correctly handles ties (both teams get points)
- [ ] Open question scoring works via host marking during reveal
- [ ] Multiple choice auto-scores on reveal
- [ ] Leaderboard shows correct final team scores
- [ ] Rejoining by team name restores player to their team
- [ ] App compiles with `go build ./...` with zero errors
- [ ] App starts and serves on :8080 with no panics

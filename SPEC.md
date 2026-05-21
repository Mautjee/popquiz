# PopQuiz — Application Specification

> Version: 0.3
> Last updated: 2025-05-21
> Status: In review

---

## 1. Overview

PopQuiz is a self-hosted web app for running live quiz nights with teams.
A host creates a quiz with multiple rounds, shares a room code, and players join on their own devices.
Players form teams — each team has one "Team Head" who submits answers on behalf of the team.
All team members see every question and video on their own phone, but only the Team Head submits.
Answers and scoring are revealed at the END of each round, not after each question.
The host controls all pacing — no timers anywhere.

---

## 2. Roles

| Role        | Description |
|-------------|-------------|
| Host        | Password-protected. Creates quizzes, runs live games, controls pacing, manages teams |
| Team Head   | First member to join a team (or auto-promoted). Submits answers for the team |
| Team Member | Joins an existing team by knowing the team name. Sees all content, cannot submit |

### Admin Password
- Set via ADMIN_PASSWORD environment variable
- If not set: admin is open (dev/local mode)
- Simple login form at /admin/login — sets a session cookie
- Multiple people can hold the admin password and host simultaneously

---

## 3. Question Types

### 3.1 Open Question
- Team Head types a free-text answer
- Correct answer and all team answers revealed at round end
- Host manually marks each team correct/incorrect during round reveal
- Points: full if marked correct, 0 otherwise

### 3.2 Ranged Question
- Team Head submits a numeric guess (integers or decimals, negatives allowed)
- Correct answer and all guesses revealed at round end
- Auto-scored: team(s) with smallest absolute difference from correct value get full points
- Tie rule: all equally-closest teams get full points
- Points: full for winner(s), 0 for all others

### 3.3 Multiple Choice Question
- 2–4 options labelled A, B, C, D
- Team Head selects one option
- Auto-scored at round end
- Points: full for correct, 0 otherwise

---

## 4. Game Flow (State Machine)

```
lobby
  │  Host clicks "Start Round N"
  ▼
question  ◄─────────────────────────────────────────────┐
  │  Host clicks "Next Question"                         │
  │  (can see which teams haven't answered yet)          │
  │  Last question in round:                             │
  │  Host clicks "End Round"                             │
  ▼                                                      │
round_reveal                                             │
  │  Host marks open questions, auto-scoring runs        │
  │  Leaderboard shown                                   │
  │  Host clicks "Start Next Round" ───────────────────►─┘
  │  No more rounds:
  │  Host clicks "End Game"
  ▼
ended
```

### State details

**lobby**
- Players join, teams form
- Host sees all teams, can reject/remove a team
- Host starts the game when ready

**question**
- Current question shown on all devices
- No correct answer shown to players
- No score updates shown to players during a round
- Host sees: question, which teams have submitted, which haven't (live via SSE)
- Host decides when to move on — no timer

**round_reveal**
- All questions from the completed round shown in order
- Each question shows: correct answer + every team's submitted answer
- Auto-scoring runs immediately for ranged and multiple_choice
- Host marks open questions manually (correct/incorrect per team)
- After all open questions marked: leaderboard shown with round scores + running total
- Host moves to next round when ready

**ended**
- Final leaderboard shown to all
- /game/:code/results accessible

---

## 5. Teams & Joining

### Joining
- Join page asks: room code + team name + player name
- Team names are NOT shown on the join page — players agree on a team name out-of-band
- Team name acts as a shared password to join a team
- If team name doesn't exist: new team created, player becomes Team Head
- If team name exists: player joins as Team Member

### Mid-game joining
- Players CAN join mid-game if they know the correct team name
- Exception: if game is currently in **question** state AND the current question's round is a **video** round → joining is blocked with message "A video question is in progress. Please wait for the next question."
- In all other states (lobby, round_reveal, ended, or non-video question): joining is allowed
- Late joiners who join an existing team see the current question immediately (no catch-up on previous questions)
- Late joiners who create a new team mid-game: team has no answers for previous questions (score 0 for those)

### Team Head rules
- Indicated by a crown (👑) marker next to their name on all screens
- If Team Head disconnects (last_seen_at > 30s ago): next player by join order in team is promoted
- New head starts fresh — no partial answer state carried over
- If entire team is empty and someone joins: they become the new head
- Players reconnecting after disconnect re-enter team via join page — they rejoin as Team Member unless they are the only one left (auto-promoted)

### Host team management (admin panel)
- Host sees all teams and their members in real time
- Host can remove/reject a team (deletes team + all its players + their answers)
- Removed players see a "You have been removed from the game" message and are redirected to join page

---

## 6. Video Sync

- Video rounds: each question has a short video clip
- Player view shows the video with play button (no autoplay)
- **Host triggers playback**: Host clicks "Play Video" button in the admin panel
- SSE event `video_play` is sent to all connected players in the game
- On receiving `video_play`: all player devices start playing the video simultaneously via JS
- Replay prevention: once the video ends, the play button is disabled (JS enforces, `ended` event)
- If a player's device didn't receive the SSE trigger (connection blip): they still see the play button and can press it manually as fallback
- Video is shown BEFORE the question text and answer input appear — answer input is hidden until host clicks "Show Question" (separate button, after video)

### Video question flow on player device
1. `video_play` SSE received → video plays
2. Video ends → play button disabled
3. `show_question` SSE received → question text + answer input appear
4. Team Head submits answer

### Video question flow on admin panel
1. Host clicks "Play Video" → triggers `video_play` SSE
2. Host clicks "Show Question" → triggers `show_question` SSE (can do this while video is still playing if they want)
3. Host watches answer submission count
4. Host clicks "Next Question" or "End Round"

---

## 7. Data Model

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
| Column         | Type    | Notes |
|----------------|---------|-------|
| id             | INTEGER | PK autoincrement |
| round_id       | INTEGER | FK rounds.id |
| question_text  | TEXT    | required |
| question_type  | TEXT    | "open" / "ranged" / "multiple_choice" |
| correct_answer | TEXT    | for ranged: numeric string; for mc: "A"/"B"/"C"/"D" |
| options        | TEXT    | JSON array, nullable — only for multiple_choice e.g. ["Paris","London","Berlin","Madrid"] |
| video_filename | TEXT    | nullable — only for video rounds |
| points         | INTEGER | default 1 |
| order_index    | INTEGER | 0-based ordering within round |

### games
| Column              | Type     | Notes |
|---------------------|----------|-------|
| id                  | INTEGER  | PK autoincrement |
| quiz_id             | INTEGER  | FK quizzes.id |
| room_code           | TEXT     | 6-char uppercase, unique |
| state               | TEXT     | "lobby" / "question" / "round_reveal" / "ended" |
| current_question_id | INTEGER  | nullable FK questions.id |
| current_round_id    | INTEGER  | nullable FK rounds.id |
| created_at          | DATETIME | default now |

### teams
| Column  | Type    | Notes |
|---------|---------|-------|
| id      | INTEGER | PK autoincrement |
| game_id | INTEGER | FK games.id |
| name    | TEXT    | required, unique per game |
| score   | INTEGER | running total, updated at each round_reveal |

### players
| Column       | Type     | Notes |
|--------------|----------|-------|
| id           | INTEGER  | PK autoincrement |
| team_id      | INTEGER  | FK teams.id |
| name         | TEXT     | display name |
| is_head      | INTEGER  | 0/1 |
| last_seen_at | DATETIME | updated on each SSE ping / request |
| joined_at    | DATETIME | default now — used for head promotion order |

### answers
| Column       | Type     | Notes |
|--------------|----------|-------|
| id           | INTEGER  | PK autoincrement |
| team_id      | INTEGER  | FK teams.id |
| question_id  | INTEGER  | FK questions.id |
| answer_text  | TEXT     | submitted value (always string) |
| is_correct   | INTEGER  | NULL until scored, then 0/1 |
| scored_at    | DATETIME | nullable |

### admin_sessions
| Column     | Type     | Notes |
|------------|----------|-------|
| id         | INTEGER  | PK autoincrement |
| token      | TEXT     | random secure token |
| created_at | DATETIME | |
| expires_at | DATETIME | 24h TTL |

---

## 8. Routes

### Public

| Method | Path                  | Description |
|--------|-----------------------|-------------|
| GET    | /                     | Join page |
| POST   | /join                 | Create/join team, set session cookie, redirect |
| GET    | /game/:code           | Player game view (SSE driven) |
| GET    | /game/:code/events    | SSE stream for player |
| POST   | /game/:code/answer    | Team Head submits answer |
| GET    | /game/:code/results   | Final leaderboard |

### Admin

| Method | Path                           | Description |
|--------|--------------------------------|-------------|
| GET    | /admin/login                   | Login form |
| POST   | /admin/login                   | Authenticate, set admin session cookie |
| GET    | /admin                         | Quiz list |
| GET    | /admin/quiz/new                | New quiz form |
| POST   | /admin/quiz                    | Create quiz |
| GET    | /admin/quiz/:id                | Edit quiz |
| POST   | /admin/quiz/:id/round          | Add round |
| DELETE | /admin/round/:id               | Delete round |
| POST   | /admin/round/:id/question      | Add question (multipart) |
| DELETE | /admin/question/:id            | Delete question |
| POST   | /admin/quiz/:id/game           | Create game session |
| GET    | /admin/game/:code              | Host control panel |
| GET    | /admin/game/:code/events       | SSE stream for admin |
| POST   | /admin/game/:code/start-round  | Start next round (lobby → question) |
| POST   | /admin/game/:code/next         | Next question within round |
| POST   | /admin/game/:code/end-round    | End round → round_reveal |
| POST   | /admin/game/:code/video-play   | Trigger video_play SSE to all players |
| POST   | /admin/game/:code/show-question| Trigger show_question SSE to all players |
| POST   | /admin/game/:code/mark         | Mark a team's open answer correct/incorrect |
| POST   | /admin/game/:code/end-game     | End game → ended |
| DELETE | /admin/game/:code/team/:id     | Remove a team from the game |

### Static

| Path                     | Description |
|--------------------------|-------------|
| /static/*                | Assets |
| /static/videos/:filename | Video clips |

---

## 9. SSE Events

### Player stream (/game/:code/events)

| Event           | Payload |
|-----------------|---------|
| state_change    | state, current_question (text, type, options, points), round (name, type), video_filename |
| video_play      | question_id — triggers JS video.play() |
| show_question   | question_id — reveals question text + answer input |
| answer_accepted | confirmation to Team Head that answer was received |
| round_reveal    | array of {question, correct_answer, options, team_answers: [{team_name, answer, is_correct}]} |
| score_update    | array of {team_name, round_score, total_score} — sent after all scoring done for round |
| head_change     | team_id, new_head_player_id, new_head_name |
| player_joined   | team_name, player_name, is_head |
| removed         | sent to players whose team was deleted by host |
| game_ended      | triggers redirect to /game/:code/results |

### Admin stream (/admin/game/:code/events)

| Event            | Payload |
|------------------|---------|
| player_joined    | player_name, team_name, is_head, team_total |
| answer_submitted | team_name, question_id, answers_in / total_teams (e.g. "3/6 teams answered") |
| all_answered     | all teams have answered current question |
| head_changed     | team_name, new_head_name |
| team_removed     | team_id (echo confirmation) |

---

## 10. Scoring Details

### When scoring runs
- Scoring for ALL questions in a round runs when host clicks "End Round"
- Scores are NOT updated during the question phase
- After scoring: score_update SSE sent to all players
- Running total shown at round_reveal, not during questions

### Ranged scoring algorithm
```
correct = parseFloat(correct_answer)
min_diff = min(abs(team.answer - correct)) for all teams that submitted
winners = teams where abs(team.answer - correct) == min_diff
winners get +points, all others +0
teams that did not submit get 0
```

### Open scoring
- Host marks each team's answer during round_reveal
- score_update SSE fires after each marking action (live leaderboard update)
- Unmarked answers at "Start Next Round": auto-scored as 0 with a warning shown to host

### Multiple choice scoring
- Fully automatic, no host action needed

---

## 11. Page Specifications (key screens)

### Join Page (/)
- Room code input (auto-uppercase)
- Team name input — no autocomplete, no existing team list shown
- Player name input
- "Join" button
- Error: "Video question in progress — please wait" (if blocked by video state)
- Error: "Game not found", "Game has ended"

### Player Game View (/game/:code)

Header: team name + 👑 if head + round name

**lobby**: team list with head markers, "Waiting for host..."

**question (text round)**:
- Question number + text
- Team Head: answer input (type depends on question_type) + submit button
- Team Member: "Your Team Head is answering..."
- After Head submits: "Answer submitted ✓" — input locked
- No correct answer shown, no other teams' answers shown

**question (video round)**:
- Video player (play button, no autoplay)
- Question text + answer input HIDDEN until show_question SSE received
- On video_play SSE: JS calls video.play()
- On video end: play button disabled
- On show_question SSE: question + input revealed

**round_reveal**:
- "Round X complete!" header
- For each question in the round:
  - Question text
  - Correct answer (shown in green)
  - This team's answer + correct/incorrect indicator
  - All other teams' answers listed (for fun/banter)
- Round score for this team + running total
- Full leaderboard table

**ended**: "Quiz over!" + link to results

### Admin Host Panel (/admin/game/:code)
- Room code (large, copyable)
- Team list with member count, head name, submitted/pending status per question
- "X/Y teams answered" counter (live)
- "Play Video" button (video rounds only, state=question)
- "Show Question" button (video rounds only, after Play Video)
- "Next Question" / "End Round" buttons
- During round_reveal: open question marking UI
- "Start Next Round" / "End Game" buttons
- Remove team button per team

---

## 12. Persistence Strategy

SQLite with a persistent Docker volume. Rationale:
- Expected load (~25 players) is well within SQLite's capability
- Single binary — no second service to manage or fail
- A named Docker volume mounted at /app/data survives container restarts and redeploys
- Data backed up by copying a single file
- Upgrade to Postgres only if multi-instance horizontal scaling is ever needed (not planned for v1)

Volume mount in Dokploy: named volume → /app/data inside the container.

---

## 13. Tech Stack

| Layer    | Choice |
|----------|--------|
| Language | Go 1.22+ |
| Router   | github.com/go-chi/chi/v5 |
| DB       | SQLite via github.com/mattn/go-sqlite3 |
| Templates| html/template |
| Frontend | HTMX 2.x via CDN |
| Styling  | Tailwind CSS via CDN |
| Video    | HTML5 native |
| Session  | Signed cookie (player_id + team_id) |
| Auth     | ADMIN_PASSWORD env var, simple session cookie |

---

## 13. Project Structure

```
popquiz/
├── cmd/server/main.go
├── internal/
│   ├── db/db.go
│   ├── models/models.go
│   ├── handlers/
│   │   ├── admin.go
│   │   ├── game.go
│   │   └── join.go
│   ├── scoring/scoring.go
│   └── sse/sse.go
├── templates/
│   ├── base.html
│   ├── join.html
│   ├── game/
│   │   ├── player.html
│   │   └── results.html
│   └── admin/
│       ├── login.html
│       ├── index.html
│       ├── quiz_editor.html
│       └── game_panel.html
├── static/app.js
├── data/videos/
├── SPEC.md
├── AGENTS.md
├── .gitignore
├── go.mod
└── go.sum
```

---

## 14. Non-Functional Requirements

- Single binary, no external services
- Graceful shutdown (drain SSE connections)
- PORT env var (default :8080)
- DATA_DIR env var (default ./data)
- ADMIN_PASSWORD env var (default: empty = open)
- All multi-write DB operations in transactions
- Background goroutine checks head disconnects every 10s

---

## 15. Out of Scope (v1)

- Per-question timer
- Drag-to-reorder
- Image questions
- CSV export
- Team chat
- Spectator mode

---

## 16. Open Questions (resolved)

| Question | Decision |
|----------|----------|
| Video sync | Host triggers play via SSE; JS autoplay on all devices |
| Late joiners during video | Blocked with "please wait" message |
| Mid-game join | Allowed — team name is the password |
| Unanswered questions | Host sees pending teams live; no timer; 0 on reveal if not submitted |
| Reveal timing | End of round only, not per question |
| Head disconnect mid-answer | New head starts fresh |
| Negative ranged answers | Allowed |
| Team name visibility | Hidden from join page; host manages via admin panel |
| Admin access | Password via env var; multiple hosts supported |
| Host refresh | All state server-side; re-opening /admin/game/:code works fine |

---

## 17. Acceptance Criteria

- [ ] Host can create a quiz with open, ranged, and multiple choice questions
- [ ] Host can create a video round and upload clips
- [ ] ~25 players across ~6 teams can join simultaneously
- [ ] Video plays on all devices simultaneously when host triggers it
- [ ] Replay disabled after video ends
- [ ] Question + answer input hidden until host clicks "Show Question"
- [ ] Only Team Head can submit answers
- [ ] Team Head auto-promoted on disconnect (30s timeout)
- [ ] New head starts fresh with empty answer
- [ ] Answers and correct answers only shown at round end
- [ ] Ranged tie scoring works correctly
- [ ] Open questions manually scored by host during round reveal
- [ ] Unmarked open answers at round end auto-score 0 with host warning
- [ ] Host can remove a team; removed players see a message
- [ ] Joining blocked during video question
- [ ] Mid-game joining works via team name
- [ ] Admin panel protected by ADMIN_PASSWORD env var
- [ ] Multiple hosts can access admin panel simultaneously
- [ ] Leaderboard shows correct final scores
- [ ] App compiles: go build ./... zero errors
- [ ] App starts on :8080 with no panics

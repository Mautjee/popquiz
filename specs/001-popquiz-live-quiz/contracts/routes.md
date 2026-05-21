# API Contracts: PopQuiz Live Quiz Night

**Created**: 2026-05-21

## Public Routes

### GET /

Renders the join page.

**Response**: HTML page with form (room code, team name, player name inputs)

---

### POST /join

Creates or joins a team, sets player session cookie, redirects to game view.

**Request**: `application/x-www-form-urlencoded`
| Field     | Type   | Validation                          |
|-----------|--------|--------------------------------------|
| code      | string | Required, 6 chars, uppercase         |
| team_name | string | Required, non-empty, trimmed        |
| player_name | string | Required, non-empty, trimmed       |

**Responses**:
- `302 Redirect → /game/:code` — success, sets `player_session` cookie
- `422 Unprocessable` — validation error, re-renders join page with error message
- `422 Unprocessable` — "Game not found" for invalid room code
- `422 Unprocessable` — "Game has ended" for ended games
- `422 Unprocessable` — "A video question is in progress. Please wait." for video question active state

**Cookie**: `player_session` — HMAC-SHA256 signed, contains `player_id` and `team_id`, HttpOnly, Path=/

---

### GET /game/:code

Renders the player game view. Validates player session cookie; redirects to `/?code=:code` if missing/invalid.

**Path Params**: `code` — 6-char room code

**Response**: HTML page (lobby/question/round_reveal/ended state, driven by SSE)

---

### GET /game/:code/events

SSE endpoint for player events. Keeps connection alive, pushes state changes.

**Path Params**: `code` — 6-char room code

**Query Params**: `player_id` — identifies the player (from session cookie)

**Response**: `text/event-stream`

**Events**:
| Event           | Payload (JSON)                                                                 |
|-----------------|-------------------------------------------------------------------------------|
| state_change    | `{state, current_question: {text, type, options, points, id}, round: {name, type}, video_filename}` |
| video_play      | `{question_id}`                                                                |
| show_question   | `{question_id}`                                                               |
| answer_accepted | `{question_id}`                                                               |
| round_reveal    | `[{question, correct_answer, options, team_answers: [{team_name, answer, is_correct}]}]` |
| score_update    | `[{team_name, round_score, total_score}]`                                     |
| head_change     | `{team_id, new_head_player_id, new_head_name}`                                |
| player_joined   | `{team_name, player_name, is_head}`                                           |
| removed         | `{}` (connection close after sending)                                         |
| game_ended      | `{}` (triggers redirect to /game/:code/results)                              |

**Heartbeat**: `: keepalive\n\n` every 30 seconds

---

### POST /game/:code/answer

Team Head submits an answer for the current question.

**Path Params**: `code` — 6-char room code

**Request**: `application/x-www-form-urlencoded`
| Field       | Type   | Validation                     |
|-------------|--------|--------------------------------|
| question_id | int    | Required, must match current   |
| answer_text | string | Required, non-empty (trimmed)  |

**Responses**:
- `204 No Content` — answer accepted, SSE `answer_accepted` event pushed
- `422 Unprocessable` — validation error (not Team Head, wrong state, etc.)

**Side Effects**: Upserts answer record (new or overwrite), pushes SSE events

---

### GET /game/:code/results

Renders the final leaderboard page.

**Path Params**: `code` — 6-char room code

**Response**: HTML page with final scores table

---

## Admin Routes

### GET /admin/login

Renders the admin login form.

**Response**: HTML login form (if ADMIN_PASSWORD set) or redirects to /admin (if open)

---

### POST /admin/login

Authenticates admin, sets session cookie.

**Request**: `application/x-www-form-urlencoded`
| Field    | Type   | Validation          |
|----------|--------|----------------------|
| password | string | Must match ADMIN_PASSWORD |

**Responses**:
- `302 Redirect → /admin` — success, sets `admin_session` cookie
- `401 Unauthorized` — wrong password, re-renders login with error

**Cookie**: `admin_session` — token, HttpOnly, Path=/, Max-Age=86400

---

### GET /admin

Renders quiz list (all quizzes with links to edit or create game).

**Auth**: Requires valid admin session cookie

**Response**: HTML page listing all quizzes with "New Quiz" and "Create Game" buttons

---

### GET /admin/quiz/new

Renders new quiz form.

**Auth**: Required

**Response**: HTML form for quiz title

---

### POST /admin/quiz

Creates a new quiz.

**Auth**: Required

**Request**: `application/x-www-form-urlencoded`
| Field | Type   | Validation      |
|-------|--------|------------------|
| title | string | Required, trimmed |

**Responses**:
- `302 Redirect → /admin/quiz/:id` — success
- `422 Unprocessable` — validation error

---

### GET /admin/quiz/:id

Renders quiz editor (rounds, questions, game creation).

**Auth**: Required

**Path Params**: `id` — quiz ID

**Response**: HTML page with quiz details, rounds, questions, and action buttons

---

### POST /admin/quiz/:id/round

Adds a round to the quiz.

**Auth**: Required

**Path Params**: `id` — quiz ID

**Request**: `application/x-www-form-urlencoded`
| Field  | Type   | Validation                        |
|--------|--------|------------------------------------|
| name   | string | Required, trimmed                 |
| type   | string | Required, "text" or "video"        |

**Response**: `302 Redirect → /admin/quiz/:id`

---

### DELETE /admin/round/:id

Deletes a round and all its questions (cascading).

**Auth**: Required

**Path Params**: `id` — round ID

**Response**: `302 Redirect → /admin/quiz/:id` (of parent quiz)

---

### POST /admin/round/:id/question

Adds a question to a round. Multipart form for video uploads.

**Auth**: Required

**Path Params**: `id` — round ID

**Request**: `multipart/form-data`
| Field           | Type   | Validation                                      |
|-----------------|--------|--------------------------------------------------|
| question_text   | string | Required, trimmed                                |
| question_type   | string | Required, "open" / "ranged" / "multiple_choice"  |
| correct_answer  | string | Required                                          |
| options          | string | JSON array, required if question_type=mc          |
| video_file       | file   | Optional, for video rounds                        |
| points           | int    | Default 1                                         |

**Response**: `302 Redirect → /admin/quiz/:id` (of parent quiz)

---

### DELETE /admin/question/:id

Deletes a question (and its video file if applicable).

**Auth**: Required

**Path Params**: `id` — question ID

**Response**: `302 Redirect → /admin/quiz/:id` (of parent quiz)

---

### POST /admin/quiz/:id/game

Creates a game session from a quiz. Generates unique room code.

**Auth**: Required

**Path Params**: `id` — quiz ID

**Response**: `302 Redirect → /admin/game/:code`

**Side Effects**: Creates game record with unique 6-char room_code

---

### GET /admin/game/:code

Renders the host control panel with team list, SSE-driven updates, and game controls.

**Auth**: Required

**Path Params**: `code` — room code

**Response**: HTML page (game panel)

---

### GET /admin/game/:code/events

SSE endpoint for admin events.

**Auth**: Required (validated on initial connection)

**Path Params**: `code` — room code

**Response**: `text/event-stream`

**Events**:
| Event            | Payload (JSON)                                                      |
|------------------|--------------------------------------------------------------------|
| player_joined    | `{player_name, team_name, is_head, team_total}`                    |
| answer_submitted | `{team_name, question_id, answers_in, total_teams}`                |
| all_answered     | `{question_id}`                                                     |
| head_changed     | `{team_name, new_head_name}`                                       |
| team_removed     | `{team_id}`                                                         |

**Heartbeat**: `: keepalive\n\n` every 30 seconds

---

### POST /admin/game/:code/start-round

Transitions game from lobby → question (first question of current/next round).

**Auth**: Required

**Path Params**: `code` — room code

**Responses**:
- `204 No Content` — success, pushes `state_change` to all players
- `422 Unprocessable` — invalid state transition

---

### POST /admin/game/:code/next

Advances to next question within current round.

**Auth**: Required

**Path Params**: `code` — room code

**Responses**:
- `204 No Content` — success, pushes `state_change` to all players
- `422 Unprocessable` — no more questions in round (host should end round)

---

### POST /admin/game/:code/end-round

Ends current round, transitions to round_reveal, runs auto-scoring.

**Auth**: Required

**Path Params**: `code` — room code

**Side Effects**: Auto-scores ranged and MC questions, pushes `round_reveal` and `score_update` events

**Responses**:
- `204 No Content` — success
- `422 Unprocessable` — not in question state

---

### POST /admin/game/:code/video-play

Triggers video playback on all player devices.

**Auth**: Required

**Path Params**: `code` — room code

**Responses**:
- `204 No Content` — success, pushes `video_play` event to all player SSE streams
- `422 Unprocessable` — not in question state or not a video question

---

### POST /admin/game/:code/show-question

Reveals question text and answer input on player devices (video rounds).

**Auth**: Required

**Path Params**: `code` — room code

**Responses**:
- `204 No Content` — success, pushes `show_question` event
- `422 Unprocessable` — not in question state

---

### POST /admin/game/:code/mark

Marks an open answer as correct or incorrect during round_reveal.

**Auth**: Required

**Path Params**: `code` — room code

**Request**: `application/x-www-form-urlencoded`
| Field       | Type | Validation                  |
|-------------|------|-----------------------------|
| answer_id   | int  | Required, valid answer ID   |
| is_correct  | int  | Required, 0 or 1            |

**Side Effects**: Updates answer, recalculates team score, pushes `score_update` event

**Responses**:
- `204 No Content` — success
- `422 Unprocessable` — invalid answer_id or not in round_reveal state

---

### POST /admin/game/:code/end-game

Ends the game, transitions to ended state.

**Auth**: Required

**Path Params**: `code` — room code

**Side Effects**: Auto-scores unmarked open answers as 0, pushes `game_ended` event

**Responses**:
- `204 No Content` — success, pushes `game_ended`
- `422 Unprocessable` — invalid state

---

### DELETE /admin/game/:code/team/:id

Removes a team and all its players/answers from the game.

**Auth**: Required

**Path Params**: `code` — room code, `id` — team ID

**Side Effects**: Deletes team + players + answers (CASCADE), pushes `removed` to affected players, `team_removed` to admin

**Responses**:
- `204 No Content` — success
- `404 Not Found` — team not found

---

## Static Routes

### GET /static/*

Serves static files (CSS, JS).

---

### GET /static/videos/:filename

Serves uploaded video clips from `$DATA_DIR/videos/`.
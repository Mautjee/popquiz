# Feature Specification: PopQuiz Live Quiz Night

**Feature Branch**: `001-popquiz-live-quiz`

**Created**: 2026-05-21

**Status**: Draft

**Input**: User description: "PopQuiz is a self-hosted Go + HTMX web app for running live quiz nights with teams. Players join via a 6-char room code and team name (which acts as a shared password). Each team has one Team Head who submits answers. There are three question types: open (host marks correct), ranged (closest guess wins, ties both score), and multiple choice (auto-scored). Rounds have a type of 'text' or 'video'. In video rounds, the host triggers video playback via SSE and all player devices play simultaneously. Answers and scores are revealed at the END of each round, not per question. The host controls all pacing — no timers. Admin panel is password-protected via ADMIN_PASSWORD env var. Data is stored in SQLite with a Docker volume at /app/data. Full details in SPEC.md — treat that as the authoritative source of truth."

## Cross-References

- **Canonical specification**: `SPEC.md` — all data models, routes, SSE events, game flow, and acceptance criteria are defined there
- **Implementation conventions**: `AGENTS.md` — project structure, tech stack, build instructions, and coding rules

## User Scenarios & Testing

### User Story 1 — Project Scaffold and DB Foundation (Priority: P1)

A developer can clone the repo, build the Go binary, and run the server. On startup, the app creates the SQLite database with all required tables (quizzes, rounds, questions, games, teams, players, answers, admin_sessions) using WAL mode and foreign keys. The server listens on PORT (default :8080) and serves a basic join page at `/`.

**Why this priority**: Without the scaffold and database, nothing else can exist. This is the foundation every other story depends on.

**Independent Test**: Run `CGO_ENABLED=1 go build ./...` with zero errors. Start the server with `PORT=8080 DATA_DIR=./data go run ./cmd/server/` and verify it creates `$DATA_DIR/popquiz.db` with all 8 tables and WAL mode enabled.

**Acceptance Scenarios**:

1. **Given** a fresh clone, **When** `CGO_ENABLED=1 go build ./...` is run, **Then** the build succeeds with zero errors
2. **Given** no existing data directory, **When** the server starts, **Then** `$DATA_DIR/popquiz.db` is created with all 8 tables and WAL mode enabled
3. **Given** the server is running, **When** a GET request to `/` is made, **Then** the join page is served with a 200 status

---

### User Story 2 — Admin Quiz Builder (Priority: P2)

A host can log into the admin panel, create a quiz with a title, add rounds (text or video type), add questions of all three types (open, ranged, multiple choice) to rounds, and upload video clips for video-round questions. The host can delete rounds and questions. From the quiz editor, the host can create a game session that generates a unique 6-character room code.

**Why this priority**: Without a quiz, there is no game. The host must be able to build content before any live game can run.

**Independent Test**: Log in to admin, create a quiz, add a video round with a ranged question and upload a video file, add a text round with an open question and a multiple-choice question, then delete one question. Verify the quiz structure in the database matches expectations.

**Acceptance Scenarios**:

1. **Given** the admin panel, **When** the host creates a quiz with title "Trivia Night", **Then** a quiz record is created in the database
2. **Given** a quiz exists, **When** the host adds a video round named "Round 1 — Music", **Then** the round is created with type "video" and order_index 0
3. **Given** a video round, **When** the host adds a ranged question with correct_answer "1969" and uploads a clip, **Then** the question record has video_filename set and question_type "ranged"
4. **Given** a text round, **When** the host adds a multiple choice question with options ["Paris","London","Berlin","Madrid"] and correct_answer "A", **Then** the question is stored with options as JSON and question_type "multiple_choice"
5. **Given** a game session creation request, **When** the host clicks "Create Game", **Then** a unique 6-char uppercase room_code is generated

---

### User Story 3 — Join Flow and Team System (Priority: P3)

A player can navigate to `/`, enter a room code (auto-uppercased), team name, and player name. If the team name is new, the player becomes Team Head (is_head=1). If the team name already exists, they join as a Team Member. A signed cookie is set with player_id and team_id. The player is redirected to `/game/:code` where they see the game view driven by SSE. Late joiners can join mid-game unless a video question is in progress.

**Why this priority**: Players need to be in teams before answers can be submitted. This must work before the live game engine.

**Independent Test**: Open the join page, enter a room code and a new team name. Verify you're redirected to the game view and see a crown marker. Open another browser, join the same team name. Verify the second player is a Team Member (no crown).

**Acceptance Scenarios**:

1. **Given** a game with room code "ABC123", **When** a player submits room code + team name "Alpha" + player name "Alice", **Then** a team "Alpha" is created, Alice is Team Head (is_head=1), and a signed cookie is set
2. **Given** team "Alpha" already has Alice as Head, **When** Bob joins with the same team name, **Then** Bob is a Team Member (is_head=0) and sees "Your Team Head is answering..."
3. **Given** a game currently in "question" state for a video round, **When** a player tries to join, **Then** they see "A video question is in progress. Please wait."
4. **Given** an invalid room code, **When** a player tries to join, **Then** they see "Game not found"

---

### User Story 4 — Live Game Engine (Text Rounds) (Priority: P4)

A host can start a round from the lobby, pushing a "state_change" SSE event to all players. Players see the current question. The Team Head can submit an answer. The host sees which teams have/haven't answered (live via SSE). When all teams have answered or the host decides to proceed, the host clicks "Next Question" or "End Round". Ending a round triggers round_reveal: all answers and correct answers are shown, auto-scoring runs for ranged and MC questions, and the host manually marks open answers.

**Why this priority**: This is the core game loop. Without it, the app has no interactive value.

**Independent Test**: Create a quiz with a text round containing an open question and a ranged question. Start the game, have two teams join, submit answers, end the round, mark the open answer, and verify scores update correctly in the leaderboard.

**Acceptance Scenarios**:

1. **Given** a game in "lobby" state, **When** the host clicks "Start Round", **Then** the game transitions to "question" state and all connected players receive a state_change SSE event
2. **Given** a question is active, **When** a Team Head submits an answer, **Then** the answer is stored in the DB and an "answer_accepted" SSE event confirms receipt to the player
3. **Given** the host is viewing the game panel, **When** teams submit answers, **Then** the host sees "2/4 teams answered" updated live via SSE
4. **Given** the host clicks "End Round", **Then** the game transitions to "round_reveal" state, auto-scoring runs for ranged and MC questions, and player devices receive round_reveal + score_update events
5. **Given** an open question during round_reveal, **When** the host marks a team's answer correct, **Then** the answer's is_correct is set to 1 and a score_update SSE fires

---

### User Story 5 — Video Round Support (Priority: P5)

The host can trigger video playback on all player devices simultaneously via SSE. Player devices start playing when they receive the `video_play` event. After the video ends, the play button is disabled. The host then clicks "Show Question" to reveal the question text and answer input (hidden until then).

**Why this priority**: Video sync is a key differentiator. It depends on the game engine (US4) and SSE being in place.

**Independent Test**: Create a video round question, start the game, reach the question, have the host click "Play Video" and verify all connected player devices start playing simultaneously. Verify the play button is disabled after the video ends.

**Acceptance Scenarios**:

1. **Given** a video question is active, **When** the host clicks "Play Video", **Then** a video_play SSE event is sent to all players in the game and their video elements start playing
2. **Given** the video has finished playing, **When** the ended event fires, **Then** the play button is disabled
3. **Given** the video is playing, **When** the host clicks "Show Question", **Then** the question text and answer input appear on player devices via show_question SSE event
4. **Given** a player's device missed the SSE trigger, **When** they see the play button, **Then** they can press it manually as a fallback

---

### User Story 6 — Scoring and Leaderboard (Priority: P6)

All three scoring types work correctly: open (host-marked), ranged (closest absolute difference wins, ties both score), and multiple choice (exact match). The leaderboard shows round scores and running totals. At game end, final scores are displayed on `/game/:code/results`.

**Why this priority**: Scoring is essential for the game but depends on the game engine and round reveal being functional.

**Independent Test**: Create a quiz with one open, one ranged (correct=100), and one MC question. Have three teams submit answers. For ranged: team A guesses 95, team B guesses 98, team C guesses 110. Verify team B gets full points (diff=2) and teams A (diff=5) and C (diff=10) get 0. For MC: verify only correct answers score. Mark open answers and verify leaderboard updates.

**Acceptance Scenarios**:

1. **Given** an open question during round_reveal, **When** the host marks Team A's answer as correct, **Then** Team A receives the question's points and score_update SSE fires with updated leaderboard
2. **Given** a ranged question with correct_answer=100, **When** teams submit 95, 98, 110, **Then** the team with 98 gets full points (diff=2) and others get 0
3. **Given** a ranged question with correct_answer=100, **When** two teams both submit 98, **Then** both teams get full points (tie rule)
4. **Given** a multiple choice question with correct_answer="B", **When** teams submit answers, **Then** teams with "B" get full points and others get 0
5. **Given** the game ends, **When** all rounds are complete, **Then** the final leaderboard is accessible at `/game/:code/results`

---

### User Story 7 — Team Head Auto-Promotion (Priority: P7)

When a Team Head disconnects (last_seen_at > 30s ago), a background goroutine promotes the next player by joined_at order in the same team. The SSE event `head_change` fires to all players in the game. The new head starts with a fresh state (no partial answer carried over).

**Why this priority**: Important for reliability during live games, but depends on the full join flow (US3) and SSE infrastructure.

**Independent Test**: Join a team with 3 players, simulate the Team Head's last_seen_at being 60 seconds old. Wait for the background goroutine to run. Verify the next player by joined_at is promoted to Head and all players receive a head_change SSE event.

**Acceptance Scenarios**:

1. **Given** a team with Head last_seen > 30s ago and other active members, **When** the background goroutine runs, **Then** the next player by joined_at is promoted to is_head=1 and the old head is set to is_head=0
2. **Given** a head promotion occurs, **When** the new head visits their game page, **Then** they see the crown marker and can submit answers
3. **Given** a team where all members are disconnected, **When** a new player joins, **Then** they are promoted to Head immediately

---

### User Story 8 — Admin Auth and Docker (Priority: P8)

The admin panel is protected by ADMIN_PASSWORD. Login sets a signed session cookie with a 24h TTL stored in the admin_sessions table. All /admin/* routes (except /admin/login) require a valid session. The Dockerfile builds a single binary with CGO_ENABLED=1 and works with a Docker volume at /app/data.

**Why this priority**: Auth is essential for production but can be developed alongside or just before deployment.

**Independent Test**: Set ADMIN_PASSWORD=testpass. Attempt to access /admin without a session — verify redirect to /admin/login. Submit the login form with the correct password — verify cookie is set and admin pages are accessible. Verify the Dockerfile builds and the resulting container starts successfully with PORT=8080.

**Acceptance Scenarios**:

1. **Given** ADMIN_PASSWORD is set, **When** an unauthenticated user visits /admin, **Then** they are redirected to /admin/login
2. **Given** ADMIN_PASSWORD is set, **When** correct credentials are submitted, **Then** a session cookie is set and admin pages are accessible
3. **Given** a valid session, **When** the session is older than 24 hours, **Then** the session is invalidated and the user is redirected to login
4. **Given** ADMIN_PASSWORD is empty, **When** any user visits /admin, **Then** they bypass login (dev mode)
5. **Given** the Dockerfile, **When** `docker build` is run, **Then** the image builds successfully and runs with PORT=8080 and DATA_DIR=/app/data

---

### Edge Cases

- What happens when a player submits an answer for a question they already answered? → The new answer overwrites the previous one.
- What happens if the host starts the next round with unmarked open answers? → They are auto-scored as 0 with a warning shown to the host.
- What happens if a player tries to join a game that has ended? → They see "Game has ended" and are redirected.
- What happens if a team submits no answer for a question? → They score 0 for that question.
- What happens if the host refreshes the admin page mid-game? → All state is server-side; re-opening /admin/game/:code works fine thanks to SSE reconnection.
- What happens if two teams submit the exact same ranged guess? → Both get full points (tie rule).

## Requirements

### Functional Requirements

- **FR-001**: The system MUST create all 8 database tables on startup with WAL mode and foreign keys enabled
- **FR-002**: The system MUST allow hosts to create quizzes with title, add rounds (text/video), add questions (open/ranged/multiple_choice), and upload video clips
- **FR-003**: The system MUST generate a unique 6-character uppercase room code when creating a game session
- **FR-004**: The system MUST allow players to join via room code + team name + player name, with team name acting as a shared password
- **FR-005**: The system MUST assign the first player of a team as Team Head (is_head=1) and subsequent players as Team Members (is_head=0)
- **FR-006**: The system MUST set a signed player session cookie containing player_id and team_id upon joining
- **FR-007**: The system MUST prevent joining during an active video question with an appropriate message
- **FR-008**: The system MUST implement SSE (Server-Sent Events) for real-time updates, keyed by room_code, with events: state_change, video_play, show_question, answer_accepted, round_reveal, score_update, head_change, player_joined, removed, game_ended
- **FR-009**: The system MUST implement the game state machine: lobby → question → round_reveal → (next round or ended)
- **FR-010**: Only Team Heads MUST be able to submit answers; Team Members see "Your Team Head is answering..."
- **FR-011**: The system MUST auto-score ranged questions (closest absolute difference wins, ties both score) and multiple choice questions (exact match)
- **FR-012**: The system MUST allow the host to manually mark open answers as correct/incorrect during round_reveal
- **FR-013**: The system MUST auto-score unmarked open answers as 0 when the host starts the next round, with a warning
- **FR-014**: The system MUST reveal answers and scores only at end of round, not per question
- **FR-015**: The system MUST synchronize video playback across all devices when the host triggers "Play Video" via SSE
- **FR-016**: The system MUST disable the video play button after the video ends (JS enforced)
- **FR-017**: The system MUST hide question text and answer input until the host triggers "Show Question" (for video rounds)
- **FR-018**: The system MUST auto-promote Team Heads when the current head's last_seen_at exceeds 30 seconds, running the check every 10 seconds
- **FR-019**: The system MUST protect the admin panel with ADMIN_PASSWORD env var; if empty, admin is open (dev mode)
- **FR-020**: The system MUST use signed session cookies for admin auth with 24h TTL stored in admin_sessions table
- **FR-021**: The system MUST allow the host to remove/reject a team, cascading deletion of team, players, and answers
- **FR-022**: The system MUST support graceful shutdown draining SSE connections
- **FR-023**: The system MUST build as a single Go binary with CGO_ENABLED=1 and serve on PORT env var (default :8080)

### Key Entities

- **Quiz**: Title, created_at. Contains multiple rounds. Authored by host.
- **Round**: Belongs to quiz. Has name, type (text/video), order_index. Contains multiple questions.
- **Question**: Belongs to round. Has question_text, question_type (open/ranged/multiple_choice), correct_answer, options (JSON), video_filename, points, order_index.
- **Game**: Instance of a quiz. Has room_code (6-char unique), state (lobby/question/round_reveal/ended), current_question_id, current_round_id.
- **Team**: Belongs to game. Has name (unique per game), score (running total). Contains multiple players.
- **Player**: Belongs to team. Has name, is_head (0/1), last_seen_at, joined_at.
- **Answer**: Belongs to team + question. Has answer_text, is_correct (null until scored), scored_at.
- **AdminSession**: Has token, created_at, expires_at (24h TTL). Used for host authentication.

## Success Criteria

### Measurable Outcomes

- **SC-001**: A host can create a quiz with all three question types and video uploads, and start a game session, all within 5 minutes
- **SC-002**: 25 players across 6 teams can join and play simultaneously with SSE updates appearing within 2 seconds
- **SC-003**: Video playback starts on all connected devices within 1 second of host trigger
- **SC-004**: Team Head auto-promotion occurs within 40 seconds of disconnect (10s check interval + 30s timeout)
- **SC-005**: Scoring is 100% accurate for all three question types including ranged ties
- **SC-006**: The app compiles with zero errors (`go build ./...`) and starts without panics
- **SC-007**: All player and admin pages render correctly on mobile devices (responsive)

## Assumptions

- About 25 players across ~6 teams is the expected load; SQLite is sufficient
- Video clips are short (under 60 seconds) and uploaded by the host
- All connections are over HTTP (no TLS requirement for the app itself; Dokploy handles TLS termination)
- Session secrets (ADMIN_SESSION_SECRET, PLAYER_SESSION_SECRET) are set via env vars or auto-generated
- No per-question timer is needed; the host controls all pacing
- Players agree on team names out-of-band (not listed on the join page)
- The app runs as a single instance; no horizontal scaling in v1
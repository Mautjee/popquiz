# Tasks: PopQuiz Live Quiz Night

**Input**: Design documents from `/specs/001-popquiz-live-quiz/`

**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/

**Tests**: Not explicitly requested; scoring package will have unit tests per AGENTS.md requirement.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description with file path`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization, Go module, directory structure, gitignore

- [X] T001 Create project directory structure per AGENTS.md: cmd/server/, internal/db/, internal/models/, internal/handlers/, internal/scoring/, internal/sse/, templates/, templates/game/, templates/admin/, static/, data/videos/
- [X] T002 Initialize Go module with `github.com/go-chi/chi/v5` and `github.com/mattn/go-sqlite3` dependencies in go.mod
- [X] T003 Create .gitignore with data/*.db, data/*.db-shm, data/*.db-wal, data/videos/* patterns

**Checkpoint**: Directory structure exists, go.mod initialised

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [X] T004 Implement DB initialisation and schema migrations in internal/db/db.go — open SQLite at $DATA_DIR/popquiz.db, enable WAL mode and foreign keys, create all 8 tables (quizzes, rounds, questions, games, teams, players, answers, admin_sessions) and indexes per data-model.md, all in a transaction with CREATE TABLE IF NOT EXISTS
- [X] T005 [P] Implement all model structs in internal/models/models.go — Quiz, Round, Question, Game, Team, Player, Answer, AdminSession with DB scan/insert tags
- [X] T006 [P] Implement SSE broker in internal/sse/sse.go — Broker struct with Subscribe/Unsubscribe/Publish methods keyed by room_code, goroutine-safe with sync.Mutex, HTTP handler for SSE connections, keepalive every 30s, client disconnect cleanup
- [X] T007 Implement application struct and main.go in cmd/server/main.go — wire chi router, inject DB and Broker as handler dependencies, read PORT/DATA_DIR/ADMIN_PASSWORD/ADMIN_SESSION_SECRET/PLAYER_SESSION_SECRET env vars, start background goroutine for head promotion, graceful shutdown

**Done when**: `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...` succeeds with zero errors and `go vet ./...` passes

**Checkpoint**: Foundation ready — user story implementation can now begin in parallel

---

## Phase 3: User Story 1 — Project Scaffold and DB Foundation (Priority: P1) 🎯 MVP

**Goal**: App compiles and DB schema is created on startup

**Independent Test**: Run the server, verify it creates the database file with all 8 tables, WAL mode enabled, and foreign keys ON

### Implementation for User Story 1

- [X] T008 [US1] Create base HTML template in templates/base.html — HTML5 boilerplate with HTMX + Tailwind CDN, `{% block content %}` layout block, SSE script include
- [X] T009 [US1] Create join page template in templates/join.html — form with room code (auto-uppercase), team name, player name inputs, error display
- [X] T010 [US1] Create join handler in internal/handlers/join.go — GET / renders join.html, POST /join validates input, creates/joins team, sets signed player cookie, redirects to /game/:code or re-renders with errors

**Done when**: Server starts, GET / returns the join page, DB file is created with all tables

---

## Phase 4: User Story 2 — Admin Quiz Builder (Priority: P2)

**Goal**: Host can create quizzes, rounds, and all 3 question types with video upload

### Implementation for User Story 2

- [X] T011 [US2] Create admin login template in templates/admin/login.html — simple password form, error display
- [X] T012 [US2] Implement admin auth middleware and login handler in internal/handlers/admin.go — GET/POST /admin/login, session cookie creation and validation, middleware to check admin_session on all /admin/* routes except /admin/login, dev mode when ADMIN_PASSWORD is empty
- [X] T013 [US2] Create admin index template in templates/admin/index.html — quiz list with "New Quiz" button, link to edit each quiz
- [X] T014 [US2] Implement admin index (quiz list) handler in internal/handlers/admin.go — GET /admin lists all quizzes from DB
- [X] T015 [US2] Create quiz editor template in templates/admin/quiz_editor.html — quiz title, add round form, round list with delete buttons, add question form (open/ranged/multiple_choice types, video upload for video rounds), question list with delete buttons, "Create Game" button
- [X] T016 [US2] Implement quiz CRUD handlers in internal/handlers/admin.go — GET /admin/quiz/new, POST /admin/quiz, GET /admin/quiz/:id (editor), POST /admin/quiz/:id/round, DELETE /admin/round/:id, POST /admin/round/:id/question (with video upload), DELETE /admin/question/:id, POST /admin/quiz/:id/game (generate room code)
- [X] T017 [US2] Implement video file upload and serving in internal/handlers/admin.go — multipart form handling, save to $DATA_DIR/videos/, serve via /static/videos/:filename using chi FileServer

**Done when**: Host can create a quiz, add text/video rounds, add all 3 question types with video upload, create a game session with room code

---

## Phase 5: User Story 3 — Join Flow and Team System (Priority: P3)

**Goal**: Player can join, team is created or joined, Team Head assigned, session cookie set

### Implementation for User Story 3

- [X] T018 [US3] Enhance join handler in internal/handlers/join.go — validate room code exists, check game state (block join during video question), create team if new name or join existing, assign is_head=1 for first player, set signed player_session cookie with player_id + team_id, redirect to /game/:code
- [X] T019 [US3] Implement player session cookie helpers — sign/verify player_session cookie with PLAYER_SESSION_SECRET, extract player_id and team_id from cookie
- [X] T020 [US3] Create player game view template in templates/game/player.html — SSE-driven page that renders differently based on game state (lobby/waiting, question with answer form for Head, round_reveal, ended), show crown 👑 for Team Head, hide answer input for Members

**Done when**: Player can join with room code and team name, first player becomes Head, subsequent players become Members, player gets a signed cookie, redirect to game view works

---

## Phase 6: User Story 4 — Live Game Engine (Text Rounds) (Priority: P4)

**Goal**: Host can run a full text round — questions shown, answers submitted, round revealed, scored

### Implementation for User Story 4

- [X] T021 [US4] Create game panel template in templates/admin/game_panel.html — room code display, team list with member count and head markers, live answer status per team, "Start Round" / "Next Question" / "End Round" / "End Game" buttons, open question marking UI during round_reveal, "Start Next Round" button
- [X] T022 [US4] Implement game state handlers in internal/handlers/admin.go — POST /admin/game/:code/start-round (lobby → question), POST /admin/game/:code/next (next question), POST /admin/game/:code/end-round (question → round_reveal with auto-scoring), POST /admin/game/:code/end-game (round_reveal/lobby → ended), each with state validation and SSE event publishing
- [X] T023 [US4] Implement game view handler in internal/handlers/game.go — GET /game/:code reads player session, loads game state, renders player.html with current state (lobby, question, round_reveal, ended), GET /game/:code/events SSE handler for player events, POST /game/:code/answer handler (Team Head only, validates question and game state, upserts answer)
- [X] T024 [US4] Implement round reveal and scoring logic — on End Round: auto-score ranged and MC answers using internal/scoring/scoring.go, calculate team round scores, update teams.score in DB, publish round_reveal and score_update SSE events; implement POST /admin/game/:code/mark for open answer marking with live score_update SSE
- [X] T025 [US4] Implement scoring pure functions in internal/scoring/scoring.go — ScoreRanged(correctAnswer string, teamAnswers map[int]string) map[int]bool (returns which teams win), ScoreMultipleChoice(correctAnswer string, teamAnswers map[int]string) map[int]bool; write unit tests for both functions including tie cases

**Done when**: Host can start a round, players see questions, Team Heads submit answers, host sees answer counts, host ends round, auto-scoring runs, host marks open answers, leaderboard updates

---

## Phase 7: User Story 5 — Video Round Support (Priority: P5)

**Goal**: Host triggers video play via SSE, all devices play simultaneously, replay blocked

### Implementation for User Story 5

- [X] T026 [US5] Implement video trigger handlers in internal/handlers/admin.go — POST /admin/game/:code/video-play validates game is in question state with a video question, publishes video_play SSE event; POST /admin/game/:code/show-question publishes show_question SSE event
- [X] T027 [US5] Implement video sync in static/app.js — SSE event listeners for video_play (call videoEl.play()), show_question (reveal question+answer div), ended event on video element to disable play button, fallback manual play button for missed SSE events
- [X] T028 [US5] Enhance player.html template for video rounds — conditionally show video player when round type is "video", hide question text + answer input by default, show after show_question SSE event

**Done when**: Host clicks "Play Video" → all player devices play simultaneously, play button disabled after video ends, question revealed on "Show Question"

---

## Phase 8: User Story 6 — Scoring and Leaderboard (Priority: P6)

**Goal**: All 3 scoring types work correctly including ranged ties, final leaderboard correct

### Implementation for User Story 6

- [X] T029 [US6] Complete scoring integration in handlers — ensure round_reveal handler calls ScoreRanged and ScoreMultipleChoice for all questions in the round, writes is_correct to answers, computes round scores per team, updates teams.score running total, publishes score_update SSE with full leaderboard data
- [X] T030 [US6] Implement open answer marking with live updates — POST /admin/game/:code/mark updates answer is_correct, recalculates team score, publishes score_update SSE; on "Start Next Round" warn about unmarked open answers and auto-score them as 0
- [X] T031 [US6] Create results template in templates/game/results.html — final leaderboard table showing team names, total scores, and rank; accessible at GET /game/:code/results

**Done when**: Ranged ties score correctly, MC exact match works, open marking updates leaderboard live, unmarked answers auto-zero on next round, final results page shows correct rankings

---

## Phase 9: User Story 7 — Team Head Auto-Promotion (Priority: P7)

**Goal**: Disconnected head is replaced within 30s, SSE event fires, new head starts fresh

### Implementation for User Story 7

- [X] T032 [US7] Implement head promotion background goroutine in cmd/server/main.go — every 10 seconds query players WHERE is_head=1 AND last_seen_at < now-30s, for each disconnected head find next player by joined_at ASC in same team where last_seen_at > now-30s, promote them (UPDATE is_head), publish head_change SSE event
- [X] T033 [US7] Implement last_seen_at updates — update players.last_seen_at on each SSE ping and each HTTP request from the player
- [X] T034 [US7] Handle reconnecting players — on GET /game/:code, if player's team has no other members, promote reconnecting player to Head regardless of is_head status

**Done when**: Disconnect a Head for 30+ seconds, verify next player is promoted within 40 seconds, head_change SSE fires, new Head can submit answers

---

## Phase 10: User Story 8 — Admin Auth and Docker (Priority: P8)

**Goal**: ADMIN_PASSWORD env var protects admin panel, Dockerfile builds and runs correctly

### Implementation for User Story 8

- [X] T035 [US8] Finalize admin session management — implement admin_sessions table cleanup (delete expired sessions on startup), generate secure random token for admin_session, store in DB with 24h TTL, validate on every /admin/* request
- [X] T036 [US8] Implement team removal handler — DELETE /admin/game/:code/team/:id cascades delete of team + players + answers, publishes removed SSE event to affected players, publishes team_removed to admin SSE
- [X] T037 [US8] Create Dockerfile — single-stage, golang:1.22-bookworm, CGO_ENABLED=1, copy source, build, expose 8080, CMD ["./popquiz"]
- [X] T038 [US8] Implement graceful shutdown — on SIGINT/SIGTERM, stop SSE broker, drain connections, close DB

**Done when**: Admin panel requires password when ADMIN_PASSWORD is set, bypasses when empty, sessions expire after 24h, Docker build succeeds, container starts on :8080

---

## Phase 11: Polish & Cross-Cutting Concerns

**Purpose**: Improvements that affect multiple user stories

- [X] T039 [P] Add SSE reconnection handling in static/app.js — auto-reconnect on connection loss, re-request state on reconnect
- [X] T040 [P] Add error handling and user-facing error messages in all templates
- [X] T041 [P] Verify mobile responsiveness with Tailwind classes across all templates
- [X] T042 Run full build: `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...` and fix any errors
- [X] T043 Run all tests: `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go test ./...` and fix any failures

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Setup completion — BLOCKS all user stories
- **US1 (Phase 3)**: Depends on Foundational phase
- **US2 (Phase 4)**: Depends on US1 (needs base template and join page for testing)
- **US3 (Phase 5)**: Depends on US2 (needs game creation for join flow)
- **US4 (Phase 6)**: Depends on US3 (needs players and teams for game engine)
- **US5 (Phase 7)**: Depends on US4 (needs game engine for video integration)
- **US6 (Phase 8)**: Depends on US4 (needs scoring to be integrated into round reveal)
- **US7 (Phase 9)**: Depends on US3 + US4 (needs players and SSE for head promotion)
- **US8 (Phase 10)**: Depends on Foundational phase (auth is cross-cutting)
- **Polish (Phase 11)**: Depends on all desired user stories being complete

### Within Each User Story

- Models before services
- Services before handlers
- Handlers before templates
- Core implementation before integration

### Parallel Opportunities

- T005 (models) and T006 (SSE) can run in parallel
- T008, T009, T010 can run in parallel after US1 foundational work
- T026, T027, T028 can mostly run in parallel (different files)

---

## Implementation Strategy

### MVP First (User Stories 1-4)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (CRITICAL — blocks all stories)
3. Complete Phase 3: US1 — Scaffold + DB
4. Complete Phase 4: US2 — Quiz builder
5. Complete Phase 5: US3 — Join flow
6. Complete Phase 6: US4 — Live game engine
7. **STOP and VALIDATE**: Test the full text-round game loop end-to-end

### Incremental Delivery

8. Add US5 — Video round support
9. Add US6 — Scoring and leaderboard polish
10. Add US7 — Team Head auto-promotion
11. Add US8 — Admin auth and Docker
12. Polish — SSE reconnection, error handling, mobile

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Each user story should be independently completable and testable
- After every user story: run `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...`
- After all stories: run `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go test ./...`
- The .gitignore must include data/*.db, data/*.db-shm, data/*.db-wal, data/videos/* before first commit
- Never commit to main — always use feature branch
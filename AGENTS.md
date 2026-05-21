# PopQuiz — AGENTS.md
<!-- SPECKIT START -->
For additional context about technologies, project structure, shell commands,
and other important information, read SPEC.md before writing any code.
<!-- SPECKIT END -->

## Project Overview

PopQuiz is a self-hosted live quiz night web app built in Go + HTMX.
Teams of players join via a room code, a Team Head submits answers,
and the host controls pacing from an admin panel.
Full spec: SPEC.md

---

## Environment

- Go binary: /home/mundi/go-sdk/go/bin/go (NOT in PATH — always use full path)
- Node: available via `node`
- OS: Ubuntu 24.04 Linux

---

## Tech Stack

- Go 1.22+ with github.com/go-chi/chi/v5 router
- SQLite via github.com/mattn/go-sqlite3 (requires CGO — always build with CGO_ENABLED=1)
- html/template for server-side rendering
- HTMX 2.x via CDN (no npm build step)
- Tailwind CSS via CDN (no npm build step)
- HTML5 native video (no JS player library)
- SSE (Server-Sent Events) for real-time updates — no WebSockets

---

## Database

SQLite with a persistent Docker volume mounted at /app/data inside the container.
The data directory is configurable via DATA_DIR env var (default: ./data).
DB file lives at $DATA_DIR/popquiz.db
Video uploads live at $DATA_DIR/videos/

Never use an in-memory SQLite DB. Always open the file-based DB.
Always use WAL mode: PRAGMA journal_mode=WAL;
Always use foreign keys: PRAGMA foreign_keys=ON;
All multi-write operations must use transactions.

---

## Git Conventions

- Never commit to main — always use feature branches
- Branch naming: feat/<name>, fix/<name>, chore/<name>
- Conventional commits: feat:, fix:, chore:, docs:
- PR: gh pr create --fill
- Add to .gitignore before first commit:
  - data/*.db
  - data/*.db-shm
  - data/*.db-wal
  - data/videos/*

---

## Project Structure

Follow this layout exactly — do not invent new top-level directories:

```
popquiz/
├── cmd/server/main.go          # Entry point, wires router + DB, PORT env var
├── internal/
│   ├── db/db.go                # Open DB, run migrations (CREATE TABLE IF NOT EXISTS)
│   ├── models/models.go        # Structs for all DB tables
│   ├── handlers/
│   │   ├── admin.go            # All /admin/* routes
│   │   ├── game.go             # All /game/* routes
│   │   └── join.go             # / and /join
│   ├── scoring/scoring.go      # Ranged + MC auto-scoring logic (pure functions, testable)
│   └── sse/sse.go              # SSE broker: subscribe/publish per room_code
├── templates/
│   ├── base.html
│   ├── join.html
│   ├── game/player.html
│   ├── game/results.html
│   └── admin/
│       ├── login.html
│       ├── index.html
│       ├── quiz_editor.html
│       └── game_panel.html
├── static/app.js               # SSE connection setup, video sync logic
├── data/videos/                # Uploaded clips — gitignored
├── Dockerfile
├── SPEC.md
├── AGENTS.md
├── .gitignore
├── go.mod
└── go.sum
```

---

## Key Implementation Notes

### SSE Broker
- One broker instance per running server (singleton, passed via context or handler struct)
- Keyed by room_code
- On client disconnect: remove from subscriber list immediately
- Events: state_change, video_play, show_question, answer_accepted, round_reveal,
  score_update, head_change, player_joined, removed, game_ended

### Video Sync (critical)
- No autoplay on player devices
- Host clicks "Play Video" → POST /admin/game/:code/video-play → SSE video_play to all players
- JS on player page: eventSource.addEventListener('video_play', () => videoEl.play())
- After video ends: JS disables play button (video.addEventListener('ended', ...))
- Question text + answer input hidden by default, revealed only on show_question SSE

### Admin Auth
- ADMIN_PASSWORD env var — if empty, admin is open (dev mode)
- Login sets a signed session cookie (use a random secret, store in ADMIN_SESSION_SECRET env var)
- All /admin/* routes (except /admin/login) require valid admin session cookie
- admin_sessions table in DB with token + expires_at (24h TTL)

### Team Head Promotion
- Background goroutine runs every 10 seconds
- Queries players WHERE is_head=1 AND last_seen_at < now-30s
- For each disconnected head: find next player by joined_at ASC in same team where last_seen_at > now-30s
- Promote them: UPDATE players SET is_head=1 WHERE id=?; UPDATE players SET is_head=0 WHERE id=old_head
- Push head_change SSE event to all players in the game

### Scoring (round_reveal)
- Runs when host clicks "End Round"
- Multiple choice + ranged: auto-scored immediately, is_correct set in DB
- Open: host marks manually via /admin/game/:code/mark, score_update SSE fires after each mark
- Unmarked open answers when host clicks "Start Next Round": auto-set is_correct=0, warn host

### Session Cookies (players)
- Signed cookie containing player_id + team_id
- Use a random PLAYER_SESSION_SECRET env var for signing
- On join: set cookie, redirect to /game/:code
- On /game/:code: read cookie to identify player and team
- If cookie missing/invalid: redirect to / with ?code=:code pre-filled

---

## Build & Test

```bash
# Build
CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...

# Test
CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go test ./...

# Run locally
PORT=8080 DATA_DIR=./data /home/mundi/go-sdk/go/bin/go run ./cmd/server/
```

---

## Docker

Build a single-stage Docker image. CGO requires gcc in the image.

```dockerfile
FROM golang:1.22-bookworm
WORKDIR /app
COPY . .
RUN CGO_ENABLED=1 go build -o popquiz ./cmd/server/
EXPOSE 8080
CMD ["./popquiz"]
```

Data volume must be mounted at /app/data in production (Dokploy config).
Set environment variables in Dokploy dashboard:
- PORT=8080
- DATA_DIR=/app/data
- ADMIN_PASSWORD=<set by host>
- ADMIN_SESSION_SECRET=<random string>
- PLAYER_SESSION_SECRET=<random string>

---

## Deployment (Dokploy)

Project: Random-projects
Application ID: will be set after first deploy
Push to main branch triggers auto-deploy.
Volume: mount a named Docker volume at /app/data to persist SQLite DB and video files.

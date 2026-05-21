# Research: PopQuiz Live Quiz Night

**Created**: 2026-05-21

## Tech Stack Decisions

### Decision: Go 1.22+ with chi router
**Rationale**: SPEC.md and AGENTS.md mandate this. Chi is lightweight, idiomatic Go, supports middleware and URL params. No alternative considered.
**Alternatives**: None — this is specified.

### Decision: SQLite with go-sqlite3 (CGO)
**Rationale**: Specified in stack. For ~25 concurrent users, SQLite is more than sufficient. WAL mode enables concurrent reads. Single-file DB simplifies Docker volume persistence and backup.
**Alternatives**: PostgreSQL — rejected because it adds a separate service, increases deployment complexity, and is not needed at v1 scale.

### Decision: html/template (server-side rendering)
**Rationale**: Specified. Avoids SPA complexity. HTMX provides dynamic updates without a JS framework.
**Alternatives**: React/Vue frontend — rejected per AGENTS.md ("no npm build step").

### Decision: HTMX 2.x via CDN
**Rationale**: Provides partial page updates, form submissions, and SSE integration directly in HTML attributes. No build step needed.
**Alternatives**: Vanilla JS fetch calls — HTMX is cleaner for this use case and handles SSE natively.

### Decision: SSE (Server-Sent Events) over WebSockets
**Rationale**: Specified in AGENTS.md. SSE is simpler (HTTP-based), auto-reconnects, works through proxies. Quiz app is server→client push (clients don't need to send arbitrary messages over the channel — they use POST for answers).
**Alternatives**: WebSockets — explicitly rejected in AGENTS.md.

### Decision: Signed cookies for session management
**Rationale**: Avoids server-side session store complexity. Player cookies contain player_id + team_id signed with PLAYER_SESSION_SECRET. Admin cookies contain session token signed with ADMIN_SESSION_SECRET. Go's `net/http` `SecureCookie` pattern or gorilla/securecookie can be used. However, to keep dependencies minimal, we'll use `crypto/hmac` with SHA-256 for cookie signing (simple, no external dependency needed).
**Alternatives**: gorilla/sessions — adds a dependency, overkill for this use case.

### Decision: gorilla/csrf or custom middleware for CSRF
**Rationale**: For v1, we'll use a simple CSRF token embedded in forms via a hidden field. The token will be a HMAC of the session cookie value. No external CSRF library needed.
**Alternatives**: gorilla/csrf — adds dependency, custom is simpler.

### Decision: Video files stored on filesystem
**Rationale**: Short clips (under 60s) stored in $DATA_DIR/videos/. Served via /static/videos/:filename. No CDN needed at this scale.
**Alternatives**: S3/cloud storage — rejected, adds deployment complexity for v1.

### Decision: Room code generation
**Rationale**: 6-character uppercase alphanumeric (A-Z, 0-9 = 36 chars). Generate randomly and retry on collision. With 36^6 = ~2.2 billion combinations, collisions are extremely unlikely.
**Alternatives**: UUID — too long for users to type. 4-char code — collision risk too high.

### Decision: Background goroutine for Team Head promotion
**Rationale**: A single goroutine with `time.NewTicker(10 * time.Second)` queries the database for disconnected heads. Simple, decoupled from the HTTP handlers.
**Alternatives**: Per-request check on each SSE ping — adds latency to the read path. Database trigger — SQLite doesn't support triggers calling Go code.

### Decision: SSE broker as singleton struct
**Rationale**: AGENTS.md mandates SSE broker passed via handler struct, not globals. The broker will be a struct with methods to subscribe/unsubscribe by room_code, and publish events to all subscribers of a room.
**Alternatives**: Global map — explicitly rejected per AGENTS.md.

### Decision: Scoring as pure functions in internal/scoring/scoring.go
**Rationale**: AGENTS.md mandates pure functions with no DB access. This makes scoring testable in isolation.
**Alternatives**: Scoring in handlers —耦合 too much business logic to HTTP handlers.

## Database Conventions

- WAL mode enabled on first connection: `PRAGMA journal_mode=WAL;`
- Foreign keys enabled: `PRAGMA foreign_keys=ON;`
- All multi-write operations wrapped in transactions
- IDs are INTEGER PRIMARY KEY (autoincrement in SQLite)
- Timestamps stored as TEXT in ISO 8601 format (Go's `time.RFC3339`)
- `ON DELETE CASCADE` on foreign keys where appropriate (team removal cascades to players and answers)

## Key Integration Points

1. **SSE Broker ↔ Handlers**: Broker is instantiated in main.go, passed to handler structs
2. **SSE Broker ↔ Background Goroutine**: Head promotion goroutine uses broker to push `head_change` events
3. **Handlers ↔ Scoring Package**: Handlers call pure scoring functions, then write results to DB
4. **Templates ↔ HTMX**: Templates use `hx-get`, `hx-post`, `hx-sse` attributes for dynamic updates
5. **Video Upload ↔ Filesystem**: Multipart form upload handler saves to `$DATA_DIR/videos/`, stores filename in DB
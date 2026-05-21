# PopQuiz — Pi Kickoff Prompt

Paste everything between the --- markers as your FIRST message to pi
after running: cd ~/Dev/popquiz && pi

---

You are building PopQuiz — a self-hosted live quiz night web app.
The full specification is in SPEC.md and all implementation conventions
are in AGENTS.md. Read both files completely before doing anything else.

Work through the full Spec-Driven Development workflow in order.
Do NOT skip phases. Do NOT start writing application code until
/speckit.plan and /speckit.tasks are complete.

---

## Phase 1 — Specify

Run the speckit.specify workflow as defined in .pi/prompts/speckit.specify.md.

Use this as the feature description:

> PopQuiz is a self-hosted Go + HTMX web app for running live quiz nights
> with teams. Players join via a 6-char room code and team name (which acts
> as a shared password). Each team has one Team Head who submits answers.
> There are three question types: open (host marks correct), ranged
> (closest guess wins, ties both score), and multiple choice (auto-scored).
> Rounds have a type of "text" or "video". In video rounds, the host
> triggers video playback via SSE and all player devices play simultaneously.
> Answers and scores are revealed at the END of each round, not per question.
> The host controls all pacing — no timers. Admin panel is password-protected
> via ADMIN_PASSWORD env var. Data is stored in SQLite with a Docker volume
> at /app/data. Full details in SPEC.md — treat that as the authoritative
> source of truth.

Rules for this phase:
- Create the spec feature directory under specs/ (sequential numbering)
- The spec file should reference SPEC.md as the canonical source — do not
  duplicate all content, instead summarise and cross-reference
- Resolve all NEEDS CLARIFICATION markers yourself using SPEC.md
- Do not ask the user any questions during this phase

---

## Phase 2 — Plan

Run the speckit.plan workflow as defined in .pi/prompts/speckit.plan.md.

Additional context for the plan:
- Go binary is at /home/mundi/go-sdk/go/bin/go — use this exact path always
- All Go builds require CGO_ENABLED=1 (sqlite3 needs it)
- Follow the project structure in AGENTS.md exactly
- SSE broker must be a singleton passed through handler structs, not globals
- The scoring package (internal/scoring/scoring.go) must contain pure
  functions only — no DB access, fully testable in isolation
- Background goroutine for Team Head promotion runs every 10 seconds
- Use WAL mode and foreign keys on SQLite from the first connection

Generate all Phase 1 design artifacts:
- research.md — tech decisions with rationale
- data-model.md — all 8 tables with fields, types, constraints, relationships
- contracts/ — HTTP route contracts for all routes in SPEC.md section 8
- quickstart.md — key integration scenarios for testing

---

## Phase 3 — Tasks

Run the speckit.tasks workflow as defined in .pi/prompts/speckit.tasks.md.

Organise tasks into these user stories in priority order:

US1 — Project scaffold and DB foundation
  Goal: app compiles and DB schema is created on startup

US2 — Admin quiz builder
  Goal: host can create quizzes, rounds, and all 3 question types with video upload

US3 — Join flow and team system
  Goal: player can join, team is created or joined, Team Head assigned, session cookie set

US4 — Live game engine (text rounds)
  Goal: host runs a full text round — questions shown, answers submitted, round revealed, scored

US5 — Video round support
  Goal: host triggers video play via SSE, all devices play simultaneously, replay blocked

US6 — Scoring and leaderboard
  Goal: all 3 scoring types work correctly including ranged ties, final leaderboard correct

US7 — Team Head auto-promotion
  Goal: disconnected head is replaced within 30s, SSE event fires, new head starts fresh

US8 — Admin auth and Docker
  Goal: ADMIN_PASSWORD env var protects admin panel, Dockerfile builds and runs correctly

Mark parallelisable tasks with [P]. Each story phase must be independently
testable — include a clear "done when" criteria per phase.

---

## Phase 4 — Implement

Run the speckit.implement workflow as defined in .pi/prompts/speckit.implement.md.

Critical rules during implementation:
- NEVER commit to main — always use a feat/ branch
- Conventional commits: feat:, fix:, chore:, docs:
- After every user story is complete: run
    CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...
  and fix all errors before moving to the next story
- After all stories done: run
    CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go test ./...
- The .gitignore must include before first commit:
    data/*.db
    data/*.db-shm
    data/*.db-wal
    data/videos/*
- Mark each task as [X] in tasks.md when complete
- The Dockerfile must use golang:1.22-bookworm and CGO_ENABLED=1

When implementation is fully complete, open a PR:
  git push origin <branch>
  gh pr create --fill

---

Begin now with Phase 1. Read SPEC.md and AGENTS.md first, then proceed.

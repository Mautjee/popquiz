# Pi Task: Fix HTMX Usage

## Context

The initial implementation included HTMX via CDN but never used any `hx-` attributes.
All interactions currently use plain form POSTs + `location.reload()` on SSE events.
This task replaces those with proper HTMX partial swaps so the UI updates without full reloads.

## Your job

Replace all `location.reload()` calls and full-page form submissions with HTMX partial swaps.
Do NOT change any Go handler logic — only update templates and add small JSON/fragment responses where needed.

## The 5 specific fixes

### 1. player.html — Answer submission form
- Currently: plain form POST → full page reload
- Fix: add `hx-post="/answer" hx-target="#answer-area" hx-swap="outerHTML"` to the form
- The handler should return only the `#answer-area` fragment (confirmation or locked state)

### 2. game_panel.html — Admin control buttons (next question, reveal answers, end round, etc.)
- Currently: plain form POSTs → full reload
- Fix: add `hx-post="<action-url>" hx-target="#game-state" hx-swap="outerHTML"` to each button/form
- Handler returns the updated `#game-state` fragment

### 3. game_panel.html — Answer count display
- Currently: full reload on SSE event
- Fix: use `hx-swap-oob="true"` on the answer count element in SSE fragments
  so it updates in place when the SSE event fires, without touching the rest of the page

### 4. game_panel.html — Remove team button
- Currently: plain POST → full reload
- Fix: `hx-delete="/admin/team/{id}" hx-target="closest .team-row" hx-swap="outerHTML swap:0.3s"`
  Handler returns empty string (or nothing) to remove the row

### 5. player.html — Lobby team list
- Currently: SSE event triggers `location.reload()` to refresh team list
- Fix: SSE event sends an `hx-swap-oob` fragment with the updated team list
  Player page listens via `hx-ext="sse"` and swaps only the team list div

## Rules

- Stay on branch `001-popquiz-live-quiz`
- Conventional commits: `fix: replace location.reload with htmx partial swaps`
- Run `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...` before committing — must compile clean
- Do NOT open a new PR — push to existing branch, PR #1 is already open
- Do NOT change scoring logic, SSE broker, or DB layer
- Keep Tailwind classes intact

## Done when

- Zero `location.reload()` calls remain in any template
- All 5 locations above use proper hx- attributes
- Build passes
- Commits pushed to branch `001-popquiz-live-quiz`

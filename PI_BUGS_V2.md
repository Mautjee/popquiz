# Pi Task: Bug Fixes & UX Improvements (Round 2)

Work on branch `001-popquiz-live-quiz`. Create it fresh from main:
```
git checkout main && git pull && git checkout -b 002-bug-fixes-ux
```

Build command: `CGO_ENABLED=1 /home/mundi/go-sdk/go/bin/go build ./...`
Must compile clean before committing. Conventional commits. Do NOT open a PR yet.

---

## Bug 1: Allow joining during video rounds

**File:** `internal/handlers/join.go`

Remove the block that prevents joining during video rounds. Just let the player in.
Delete this entire block (roughly lines 95–108):

```go
// Check if joining is blocked during video question
if game.State == "question" && game.CurrentQuestionID.Valid {
    var roundType string
    err := h.db.QueryRow(`...`).Scan(&roundType)
    if err == nil && roundType == "video" {
        h.templates.ExecuteTemplate(w, "join.html", ...)
        return
    }
}
```

Players joining mid-video will land in the game and see the video playing (or already played).

---

## Bug 2: Video round — question text and answer input not showing

**File:** `templates/game/partials/game_state_content.html`

In the `{{if eq .Game.State "question"}}` block, for video questions:
- The question text IS in the template but it's inside `{{define "content"}}` alongside the video
- The `#question-reveal` div is hidden and only shown via SSE `show_question` event
- BUT: when a player reloads or joins mid-question, the game partial is re-fetched and the reveal state is lost

Fix:
1. Add a `ShowQuestion` bool field to the game data struct (set it true when `game.VideoShowQuestion == 1` or equivalent DB field)
2. If `ShowQuestion` is true, render `#question-reveal` WITHOUT the `hidden` class on initial load
3. Also ensure the answer input has a proper `<input>` or `<textarea>` — right now for video questions there's no answer input visible. The answer_area partial must always be included inside question-reveal.

Check `internal/handlers/game.go` `buildGameData` or equivalent to see what data is passed to the template.
Check the DB schema for how `show_question` state is tracked (look in `internal/db/db.go` or the games/questions table).

---

## Bug 3: Multiple choice questions — replace JSON input with UI

**File:** `templates/admin/quiz_editor.html`

Currently the options field for multiple choice questions is a plain text input expecting a JSON array like `["Option A","Option B","Option C","Option D"]`.

Replace it with a proper UI:
- Show 4 labeled input fields: Option A, Option B, Option C, Option D
- Only show these fields when question type = "multiple_choice" (use JS to toggle visibility)
- On form submit, serialize the 4 inputs into a JSON array string and put it in a hidden input named `options`
- The correct_answer field for multiple choice should be a dropdown: A, B, C, D (not a free text input)
  - Also only show when type = "multiple_choice"
- For open and ranged questions, correct_answer stays as a plain text input

The server already receives `options` as a JSON string and `correct_answer` as a letter — no backend changes needed.

---

## Bug 4: No way to restart or reset a game after it ends

**File:** `templates/admin/game_panel.html` and `internal/handlers/admin.go`

When `game.State == "ended"`, show a "Start New Game" button and a "Reset This Game" button.

**Reset game** (`POST /admin/game/{code}/reset`):
- Sets game state back to `lobby`
- Deletes all answers for this game
- Resets scores to 0 for all teams
- Resets `current_question_id`, `current_round_id` to NULL
- Players stay joined — they don't need to re-enter

**Start new game** (`POST /admin/quiz/{id}/game` already exists — just link to it):
- Show a link/button that goes back to the quiz editor page so the host can create a fresh game session

Add the reset handler in `internal/handlers/admin.go` and register the route in `cmd/server/main.go`:
```
r.Post("/admin/game/{code}/reset", adminHandler.PostResetGame)
```

After reset, broadcast SSE event `state_change` so all connected players refresh to the lobby screen.

---

## Bug 5: Rework player onboarding — team-first, no display names

This is the biggest change. The current flow: enter name + team name + room code on one page.

**New flow:**

### Step 1 — Room code entry (`GET /`, `POST /join/room`)
Simple page: just a room code input. On submit → redirect to `/join/{code}`.

### Step 2 — Team selection (`GET /join/{code}`)
Show:
- The room code prominently
- A list of existing teams with a "Join" button next to each
- A "Create new team" input + button at the bottom
- No name input anywhere — players are anonymous within teams

On "Join [TeamName]" → `POST /join/{code}/team/{teamId}` → set cookie → redirect to `/game/{code}`
On "Create new team" → `POST /join/{code}/team` with `team_name` body → create team, set cookie → redirect to `/game/{code}`

### Model changes
- Remove the `name` column requirement from the `players` table (or make it optional / auto-generate as "Player 1", "Player 2" etc.)
- Team head is still the first joiner per team — no change to that logic
- Only the team head can submit answers — no change

### Display changes
- In `templates/game/player.html`, remove any display of individual player names
- The team header shows only the team name + "You are submitting" (if head) or "Watching" (if member)
- In `templates/game/partials/game_state_content.html` lobby section, show team members as "Player 1, Player 2..." or just show the count: "3 players in your team"

### Admin panel changes
- In `templates/admin/partials/game_panel_teams.html`, show teams with player count instead of player names

Keep `internal/handlers/join.go` clean — you may need to add new handler methods and new routes in `cmd/server/main.go`.

---

## Summary checklist

- [ ] Bug 1: Video join block removed
- [ ] Bug 2: Video question shows text + answer input correctly on load/reload
- [ ] Bug 3: Multiple choice UI with 4 option inputs + dropdown for correct answer
- [ ] Bug 4: Reset game button + handler; start new game link
- [ ] Bug 5: Two-step join (code → team select), no player names, team-based only

Build must pass. Push all commits to branch `002-bug-fixes-ux`.

# Pi Task: Bug Fixes & Quiz Mode Feature — Round 4

Branch from main:
```bash
git checkout main && git pull && git checkout -b 004-quiz-modes
```

Build check after every change: `CGO_ENABLED=0 /home/mundi/go-sdk/go/bin/go build ./...`
Read `AGENTS.md`, `SPEC.md`, `internal/db/db.go` before starting.

---

## Bug 1 — Players stuck on /results after host resets or ends game

**Root cause:** `game_ended` SSE event fires → players redirect to `/game/{code}/results`. But when the host then resets the game (state → `lobby`), the `state_change` SSE is published — but players are now on `/results`, a static page with no SSE listener. They're stuck.

**Fix — two parts:**

### Part A: Add Rejoin / Leave buttons to results.html

In `templates/game/results.html`, add two buttons at the bottom:
- **"Rejoin Game"** → `href="/game/{code}"` — sends player back to the player page with their existing cookie session, which will show lobby state if the game was reset
- **"Leave Game"** → clears the `player_session` cookie via JS (`document.cookie = 'player_session=; Path=/; Max-Age=0'`) then redirects to `/`

### Part B: SSE listener on results page

Add a small `<script>` to `results.html` that opens an SSE connection to `/game/{code}/events` and listens for `state_change`. If `state_change` fires with `state=lobby`, auto-redirect the player back to `/game/{code}` (the rejoin URL). This way if the host resets, players are automatically sent back to the lobby without having to click.

```html
<script>
const roomCode = "{{.Code}}";
const es = new EventSource('/game/' + roomCode + '/events');
es.addEventListener('state_change', function(e) {
    try {
        const d = JSON.parse(e.data);
        if (d.state === 'lobby') window.location.href = '/game/' + roomCode;
    } catch {}
});
</script>
```

---

## Bug 2 — Player count increases on page refresh (duplicate player rows)

**Root cause:** `GET /game/{code}` calls `buildPlayerData` which reads the `player_session` cookie. If the cookie is valid the player's `last_seen_at` is updated. But on the join flow (`POST /join/{code}/team`) a **new player row is inserted** each time — so if a player refreshes the join page or resubmits the form, a second player row is created for the same browser.

**Fix — upsert on join, not insert:**

In `internal/handlers/join.go`, in the handler that inserts a new player row:

1. Before inserting, check if there's already a valid `player_session` cookie. If the cookie is valid and the player row still exists in DB, skip insertion and reuse the existing `playerID`.
2. If no valid cookie, do the insert as normal.

Concretely:
```go
// In PostJoinTeam (or equivalent):
// 1. Try to read existing cookie
playerID, teamID, ok := h.getPlayerFromCookie(r)
if ok {
    // verify row still exists
    var exists int
    h.db.QueryRow("SELECT COUNT(*) FROM players WHERE id = ? AND team_id = ?", playerID, teamID).Scan(&exists)
    if exists == 1 {
        // player already registered, just redirect
        http.Redirect(w, r, "/game/"+code, http.StatusSeeOther)
        return
    }
}
// 2. Otherwise insert new player row as before
```

Also add a `getPlayerFromCookie` helper to `JoinHandler` (same pattern as in `GameHandler`) using the shared `parsePlayerSession` function and the session secret.

---

## Bug 3 — Host should be able to review + approve answers BEFORE ending the round

**Current flow:** `PostEndRound` → auto-scores MC/ranged → state = `round_reveal` → host visits `/admin/game/{code}/answers` to approve open answers.

**New flow:** Review happens WHILE state = `question`, before ending the round. Points are applied immediately on approval.

**Changes:**

1. In `PostApproveAnswer` and `PostDenyAnswer` handlers: remove any guard that restricts them to `round_reveal` state only. They must work when `game.State == "question"` too.

2. In `templates/admin/partials/game_panel_game_state.html`: add a **"📋 Review Answers"** link/button that is visible during `question` state (not only `round_reveal`). Link to `/admin/game/{code}/answers`.

3. In `GetAnswerReview`: remove any state guard — it must work for both `question` and `round_reveal` states.

4. The "End Round" button should still exist and work — it just now auto-scores the remaining MC/ranged answers that haven't been scored yet (already done by `autoScoreRound`). Open answers that haven't been approved remain unscored (correct, don't change this).

---

## Feature — Two quiz modes: Offline and Online

This is the biggest change. When creating a new quiz, the host picks one of two modes:

### Mode definitions

**Offline Quiz ("Paper Round")**
- Players write answers on paper — no digital answer submission
- Host controls the flow: Start Round → Play Video (if video round) → Show Question → Next Question → End Round
- No answer input shown to players — they just see the question/video
- No scoring in the app
- Round reveal shows the correct answer only — no team scores
- No "Review Answers" panel needed

**Online Quiz ("Live Quiz")**
- Current behaviour — players submit answers digitally
- Host approves open answers
- Scores tracked per team
- All existing features apply

### DB migration

Add `mode` column to `quizzes` table:
```sql
ALTER TABLE quizzes ADD COLUMN mode TEXT NOT NULL DEFAULT 'online' CHECK(mode IN ('online', 'offline'));
```
Use the same pragma-check pattern as before (check `pragma_table_info('quizzes')` for 'mode' column before running ALTER).

### Quiz creation UI changes (`templates/admin/index.html` or wherever "New Quiz" form lives)

Add a radio/toggle to the new quiz form for mode selection:
- 🖊️ **Paper Round** (offline) — "Players write answers on paper. Host controls the video/question flow. No digital scoring."
- 📱 **Live Quiz** (online) — "Players submit answers on their phones. Scores tracked automatically."

Pass `mode` field in the POST body for quiz creation. Default: `online`.

### Model change

Add `Mode string` field to `models.Quiz`.

### Pass quiz mode through to game/player templates

When `buildPlayerData` is called in `game.go`, load the quiz associated with the game and pass `QuizMode string` (either `"online"` or `"offline"`) into the template data map.

Similarly, pass `QuizMode` into admin game panel data.

### Player template changes (`game_state_content.html`, `answer_area.html`)

In `answer_area.html`: wrap the entire answer form in `{{if eq .QuizMode "online"}}...{{end}}`. In offline mode, players see NO answer input — just the question text (if `ShowQuestion = true`) or the video (if not).

### Admin game panel changes

In `game_panel_game_state.html`:
- Show "📋 Review Answers" button only when `QuizMode == "online"`
- In `round_reveal` state for offline mode: show the correct answers for each question in the round instead of team scores

### Round reveal for offline mode

Add a new partial `templates/admin/partials/offline_round_reveal.html`:
- Lists each question in the current round with its correct answer
- No team scores shown
- A "Next Round" or "End Game" button

In `PostEndRound`: if `quiz.mode == 'offline'`, skip `autoScoreRound` entirely.

### Results page for offline mode

In `results.html`: if `QuizMode == "offline"`, show "Thanks for playing! 🎉" and a summary of rounds + questions. No leaderboard.

---

## Done-When Criteria

- [ ] Results page has "Rejoin Game" and "Leave Game" buttons
- [ ] SSE listener on results page auto-redirects on host reset
- [ ] Refreshing /game/{code} does NOT create a duplicate player row if cookie is valid
- [ ] "📋 Review Answers" visible during `question` state (not only `round_reveal`)
- [ ] Approve/deny works during `question` state
- [ ] New quiz form has Offline / Online mode selector
- [ ] `quizzes.mode` column added via safe migration
- [ ] In offline mode: no answer form shown to players
- [ ] In offline mode: `autoScoreRound` skipped in `PostEndRound`
- [ ] In online mode: all existing behaviour unchanged
- [ ] `QuizMode` passed to all relevant templates
- [ ] `go build ./...` passes with CGO_ENABLED=0
- [ ] Open PR against main when done

---

## Constraints

- Go binary: `/home/mundi/go-sdk/go/bin/go`
- CGO_ENABLED=0 for local builds
- Per-page template render pattern — never load multiple page templates into one `*template.Template`
- Branch: `004-quiz-modes`
- Conventional commits, one commit per logical change

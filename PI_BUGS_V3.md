# Pi Task: Bug Fixes & Features — Round 3

Work on a new branch from main:
```bash
git checkout main && git pull && git checkout -b 003-bugs-and-features
```

Build check after EVERY change: `CGO_ENABLED=0 /home/mundi/go-sdk/go/bin/go build ./...`

Read `AGENTS.md`, `SPEC.md`, and `internal/db/db.go` (schema) before starting.

---

## Bug 1 — HTML tags rendered as text in player.html

**File:** `templates/game/player.html` line 10

The `{{if .IsHead}}` block sits inside a raw `<p>` tag that gets rendered by the HTMX swap into a context that double-escapes it. The actual fix is simpler: the text "👑 You are submitting" and "Watching" need to be plain text inside a styled container, not a `<p>` tag rendered via Go template string interpolation that a browser misreads.

**Fix:** Change the template so the IsHead block renders clean HTML without any escaping issues. Use `{{if .IsHead}}` blocks at the template level, not inside a string attribute. The current line:

```html
{{if .IsHead}}<p class="text-yellow-400 text-sm">👑 You are submitting</p>{{else}}<p class="text-gray-400 text-sm">Watching</p>{{end}}
```

Should be split into proper conditional blocks so the browser receives clean HTML. Also verify `game_state_content.html` does not re-render this same element via SSE in a way that injects raw HTML strings.

---

## Bug 2 — Player cannot select or submit an answer

**File:** `templates/game/partials/game_state_content.html`

When `ShowQuestion = true`, players see the question but the answer form inputs are not interactive / not being rendered correctly. Two issues:

1. For `multiple_choice` questions the option buttons are likely rendered but clicking them does not populate a hidden input — fix the JS so clicking an option sets a hidden `<input name="answer">` value and visually highlights the selected option.
2. The submit button POSTs to `/game/{code}/answer` via `hx-post`. Verify the form has `hx-post`, `hx-target`, and `hx-swap` attributes set. If it uses a plain `<form method="POST">` it will navigate away — it must use HTMX.
3. For `open` and `ranged` question types, render a plain `<input type="text">` or `<input type="number">` respectively, inside the same HTMX form.

Only the team head (`.IsHead == true`) should be able to submit. Non-heads see "Waiting for your team head to answer."

---

## Bug 3 — Show Question button not greyed out; Next Question not greyed out when no more questions

**File:** `templates/admin/partials/game_panel_game_state.html`

Two distinct grey-out conditions:

1. **Show Question already clicked** — when `game.show_question = 1`, the "Show Question" button should be `disabled` and visually greyed (Tailwind: `opacity-50 cursor-not-allowed`). It reactivates when the host moves to the next question.
2. **No more questions in round** — the "Next Question" button should be `disabled` when there is no next question after the current one. The handler `PostNextQuestion` already queries for the next question — pass a `HasNextQuestion bool` field in the template data and use it to conditionally disable the button.

Pass `ShowQuestion bool` and `HasNextQuestion bool` in the `GamePanelData` struct rendered by `GetGamePanel` and `GetGamePanelState`.

---

## Bug 4 — Scores not updated at end of round

**File:** `internal/handlers/admin.go` → `PostEndRound`

Currently `PostEndRound` likely sets game state to `round_reveal` and broadcasts SSE but does **not** apply scores to `teams.score`. 

**Fix:** In `PostEndRound`, for all `multiple_choice` and `ranged` answers that already have `is_correct` set (from auto-scoring at submit time), run:

```sql
UPDATE teams SET score = score + (
  SELECT COALESCE(SUM(q.points), 0)
  FROM answers a
  JOIN questions q ON a.question_id = q.id
  JOIN rounds r ON q.round_id = r.id
  WHERE a.team_id = teams.id
    AND a.is_correct = 1
    AND r.id = ?   -- current round id
    AND a.scored_at IS NULL
)
WHERE game_id = ?
```

Then mark those answers `scored_at = datetime('now')` so they are not double-counted on reset.

For `open` questions: scores are applied later by host approval (Feature 1 below) — do NOT auto-score open answers at round end.

Also verify that `PostSubmitAnswer` (in `game.go`) auto-scores `multiple_choice` (exact match) and `ranged` (closest value wins — compare after all teams have answered or at round end) at submission or round-end time, setting `is_correct` correctly before `PostEndRound` runs.

---

## Bug 5 — Video: play button shown to players; show question hides video and shows Q+answers

**Files:** `templates/game/partials/game_state_content.html`, `templates/admin/partials/game_panel_game_state.html`

Two requirements:

1. **Play button players-only hide:** The "▶ Play Video" button in `game_state_content.html` is for players. Keep it — players need it because iPhone Safari can't autoplay. But only show it **once**: after the player clicks it, hide the button (use JS `onclick` to hide the button element after first click, or an HTMX swap).

2. **Show Question transitions:** When host clicks "Show Question" (`show_question = 1`), the SSE update pushes new state to players. In `game_state_content.html`, when `ShowQuestion = true`:
   - **Hide** the video element and play button entirely
   - **Show** the question text prominently
   - **Show** the answer input form (see Bug 2)

   When `ShowQuestion = false` (video playing state): show the video player + play button, hide the question/answer form.

---

## Feature 1 — Host answer review panel (open questions only)

This is the core scoring feature. The host needs to see all team answers for every question in the current round, approve or deny open-answer submissions, and have scores applied only on approval.

### DB migration needed

Add a column to `answers`:
```sql
ALTER TABLE answers ADD COLUMN host_approved INTEGER CHECK(host_approved IN (0, 1));
```
Add this as a new migration in `db.go`'s migration list (use `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` pattern via a separate `db.Exec` after the CREATE TABLE block, wrapped in a check so it doesn't fail if the column already exists — SQLite doesn't support `ADD COLUMN IF NOT EXISTS` natively, so use: `SELECT COUNT(*) FROM pragma_table_info('answers') WHERE name='host_approved'` and only run the ALTER if count = 0).

Scoring rules by question type:
- **open**: `is_correct` set by host via approve/deny button → points applied when approved
- **multiple_choice**: auto-scored at submit time (exact string match, case-insensitive)
- **ranged**: auto-scored at round end (team whose answer is numerically closest to `correct_answer` gets full points; ties share points or first-submitted wins — use submitted_at tiebreak)

### New route

```
GET  /admin/game/{code}/answers          → GetAnswerReview
POST /admin/game/{code}/answers/{id}/approve  → PostApproveAnswer
POST /admin/game/{code}/answers/{id}/deny     → PostDenyAnswer
```

### Handler: GetAnswerReview

Query all answers for all questions in `current_round_id`, joined with team name and question text. Group by question. Return template data:

```go
type AnswerReviewData struct {
    Game      models.Game
    Questions []QuestionAnswers
}
type QuestionAnswers struct {
    Question models.Question
    Answers  []AnswerRow
}
type AnswerRow struct {
    AnswerID      int64
    TeamName      string
    AnswerText    string
    IsCorrect     sql.NullBool
    HostApproved  sql.NullBool
    QuestionType  string
    Points        int
}
```

### Handler: PostApproveAnswer (open questions only)

1. Set `answers.is_correct = 1`, `answers.host_approved = 1`, `answers.scored_at = datetime('now')`
2. `UPDATE teams SET score = score + q.points WHERE id = answer.team_id`
3. Broadcast SSE `score_update` event so leaderboard refreshes on player screens
4. Return the updated answer row partial via HTMX swap (`hx-swap="outerHTML"`)

### Handler: PostDenyAnswer (open questions only)

1. Set `answers.is_correct = 0`, `answers.host_approved = 0`
2. No score change
3. Return updated answer row partial

### Template: `templates/admin/answer_review.html`

Full page (uses base.html). Shows:
- Round name header
- Per question: question text, question type badge
- Per answer row: team name, answer text, correct answer (shown for MC/ranged), status badge (pending/approved/denied), approve/deny buttons (only for `open` type, hidden for others)
- For `ranged`: show numeric diff from correct answer alongside the answer
- For `multiple_choice`: show ✅ or ❌ auto-scored badge

Link from game panel: "📋 Review Answers" button → opens `/admin/game/{code}/answers` in same tab.

---

## Feature 2 — Edit existing questions in quiz editor

**Files:** `internal/handlers/admin.go`, `templates/admin/quiz_editor.html`

Currently the quiz editor only supports creating new questions. Add edit-in-place:

### New routes

```
GET  /admin/quiz/{id}/question/{qid}/edit   → GetEditQuestion  (returns inline form partial)
POST /admin/quiz/{id}/question/{qid}        → PostUpdateQuestion
DELETE /admin/quiz/{id}/question/{qid}      → DeleteQuestion
```

### GetEditQuestion

Returns a partial HTML snippet (`templates/admin/partials/question_edit_form.html`) with the question fields pre-filled. Triggered by clicking "Edit" on a question row. Use HTMX: `hx-get="/admin/quiz/{id}/question/{qid}/edit"` `hx-target="#question-{qid}"` `hx-swap="outerHTML"` to replace the question row with the inline edit form.

### PostUpdateQuestion

Updates the question in DB. Returns the updated question row partial (`templates/admin/partials/question_row.html`) so HTMX can swap it back.

### DeleteQuestion

Deletes question from DB. Returns empty string (HTTP 200) so HTMX removes the element.

### Template changes to quiz_editor.html

Each question row needs:
- An `id="question-{qid}"` wrapper div
- "Edit" button with `hx-get` pointing to the edit endpoint
- "Delete" button with `hx-delete`, `hx-confirm="Delete this question?"`, `hx-target="#question-{qid}"`, `hx-swap="outerHTML"`

The edit form partial should reuse the same MC option inputs (A/B/C/D) + correct answer dropdown as the create form — extract that into a shared partial `templates/admin/partials/question_fields.html` included by both.

---

## Done-When Criteria

- [ ] Player page shows "👑 You are submitting" / "Watching" as plain readable text (no HTML tags visible)
- [ ] Team head can select a multiple choice answer by clicking an option and submit it
- [ ] Team head can type an open/ranged answer and submit it
- [ ] Non-head players see "Waiting for your team head to answer" during question phase
- [ ] Show Question button is greyed after clicking; re-enables on next question
- [ ] Next Question button greyed when no more questions in round
- [ ] Scores update on `teams.score` at end of round for auto-scored questions (MC/ranged)
- [ ] Open answers are NOT auto-scored — host must approve
- [ ] Host answer review page shows all answers grouped by question for current round
- [ ] Approve button adds points + marks scored_at; deny marks is_correct=0
- [ ] Video: play button hides after first player click
- [ ] When show_question=1: video hidden, question + answer form shown to players
- [ ] Edit button on quiz editor replaces question row with inline form
- [ ] Save on edit form updates question and restores the row
- [ ] Delete button removes question row
- [ ] `go build ./...` passes with CGO_ENABLED=0
- [ ] Open PR against main when done

---

## Constraints

- Go binary: `/home/mundi/go-sdk/go/bin/go`
- CGO_ENABLED=0 for local builds (uses modernc.org/sqlite, no gcc needed)
- Per-page template render pattern — NEVER load multiple page templates into one `*template.Template`. Use the `render(w, data, name, files...)` helper already in each handler. Partials loaded for HTMX swaps should use their own `template.Must(template.New("").ParseFiles(...))` call.
- SQLite migration: add `host_approved` column using pragma check pattern (no `IF NOT EXISTS` on ALTER)
- Branch: `003-bugs-and-features`
- Conventional commits, one commit per logical change

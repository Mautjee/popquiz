# Pi Task: Quiz Editor Improvements — Round 5

Branch from main:
```bash
git checkout main && git pull && git checkout -b 005-quiz-editor-improvements
```

Build check after every change: `CGO_ENABLED=0 /home/mundi/go-sdk/go/bin/go build ./...`
Read `AGENTS.md`, `SPEC.md`, `internal/db/db.go`, `internal/models/models.go`, `internal/handlers/admin.go`, and `templates/admin/quiz_editor.html` before starting.

---

## Feature 1 — Paper Round questions: no answer type, just a correct answer

When a round belongs to an **offline** quiz (quiz.mode == 'offline'), question creation and editing should be simplified:
- **Remove** the "Answer Type" selector (open / multiple choice / ranged) from the add/edit question form
- **Keep** only the "Correct Answer" text field (shown at round reveal)
- **Hardcode** `question_type = 'open'` in the DB insert/update for offline quiz questions — no need to store a different type

**Where to change:**
- `templates/admin/quiz_editor.html` (or the partial that renders the add-question form): conditionally hide the question_type selector when the parent quiz is offline. Pass `Quiz.Mode` into the template data for the quiz editor so the template can use `{{if eq .Quiz.Mode "offline"}}`.
- `internal/handlers/admin.go` `PostQuestion` and `PostUpdateQuestion`: when the quiz is offline, force `questionType = "open"` regardless of the form value.

To get the quiz mode in PostQuestion: the round_id is in the URL. Join rounds → quizzes to get the mode:
```sql
SELECT q.mode FROM quizzes q JOIN rounds r ON r.quiz_id = q.id WHERE r.id = ?
```

---

## Feature 2 — Delete a quiz (with confirmation)

Add a delete button to each quiz on the admin index page (`templates/admin/index.html`) and wire up a confirmation step before deleting.

### Flow
1. Each quiz row gets a 🗑️ "Delete" button.
2. Clicking it shows an inline confirmation (HTMX swap) — replace the quiz row with:
   ```
   Delete "Quiz Name"? [Yes, delete] [Cancel]
   ```
3. "Yes, delete" sends `DELETE /admin/quiz/{quizID}` (or `POST /admin/quiz/{quizID}/delete`).
4. "Cancel" swaps the row back to normal.
5. On delete: remove the quiz, all its rounds, all its questions, all associated video/image files from disk, and all games/answers referencing it.

### Backend
Add handler `PostDeleteQuiz` in `admin.go`:
```go
// DELETE /admin/quiz/{quizID}
func (h *AdminHandler) PostDeleteQuiz(w http.ResponseWriter, r *http.Request) {
    // 1. Get all video_filename and image_filename values for questions in this quiz
    // 2. Delete files from disk
    // 3. DELETE FROM answers WHERE game_id IN (SELECT id FROM games WHERE quiz_id = ?)
    // 4. DELETE FROM games WHERE quiz_id = ?
    // 5. DELETE FROM questions WHERE round_id IN (SELECT id FROM rounds WHERE quiz_id = ?)
    // 6. DELETE FROM rounds WHERE quiz_id = ?
    // 7. DELETE FROM quizzes WHERE id = ?
    // 8. Return HX-Redirect to /admin or render updated quiz list
}
```

Register route: `r.Post("/admin/quiz/{quizID}/delete", h.PostDeleteQuiz)`

### HTMX inline confirmation partial
Add `templates/admin/partials/quiz_delete_confirm.html` — renders the confirmation row.
Add `templates/admin/partials/quiz_row.html` — renders a normal quiz row (for cancel swap-back).

---

## Feature 3 — Optional photo upload on text rounds

Players and the admin should be able to see an optional image alongside the question text in text rounds.

### DB migration
Add `image_filename` column to `questions`:
```sql
ALTER TABLE questions ADD COLUMN image_filename TEXT;
```
Use the pragma check pattern (check `pragma_table_info('questions')` for 'image_filename' before running ALTER).

### Model
Add `ImageFilename sql.NullString` to `models.Question`.

### Upload (admin quiz editor)
In the add-question form for **text** rounds (round.Type == 'text'), add an optional file input:
```html
<input type="file" name="image_file" accept="image/*">
```

In `PostQuestion` and `PostUpdateQuestion` in `admin.go`:
- If `image_file` is present: save to `{DATA_DIR}/images/{uuid}.{ext}`, store filename in DB.
- If no file uploaded: leave `image_filename` NULL.
- On `PostUpdateQuestion`: if a new image is uploaded, delete the old one from disk first.

Create the images directory on startup (alongside the existing videos directory).

Serve images statically — add a file server route:
```go
r.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.Dir(filepath.Join(dataDir, "images")))))
```

### Display
In `templates/game/partials/game_state_content.html`: if `ImageFilename` is set, show the image above the question text:
```html
{{if .CurrentQuestion.ImageFilename.Valid}}
<img src="/images/{{.CurrentQuestion.ImageFilename.String}}" class="w-full rounded-lg mb-4 max-h-64 object-contain">
{{end}}
```

Also show the image in the admin quiz editor (question list) as a thumbnail.

---

## Feature 4 — Reorder questions in the quiz editor (drag-and-drop)

Allow the admin to reorder questions within a round via drag-and-drop in the quiz editor.

### Backend
Add a PATCH/POST endpoint to update order:
```
POST /admin/question/{questionID}/reorder
Body: order_index=<new_index>
```

Or better — a bulk reorder endpoint:
```
POST /admin/round/{roundID}/reorder-questions
Body: question_ids=1&question_ids=3&question_ids=2  (ordered list)
```

Handler `PostReorderQuestions`: takes the ordered list of question IDs, updates each `order_index` = its position in the list. Validate all IDs belong to the round.

### Frontend
In `templates/admin/quiz_editor.html`, make the question list for each round sortable using the HTML5 Drag and Drop API (no extra JS libraries — use native `draggable` attribute):

```html
<div id="questions-{{.ID}}" class="space-y-2">
  {{range .Questions}}
  <div class="question-row flex items-center gap-2 cursor-move" 
       draggable="true" 
       data-question-id="{{.ID}}"
       ondragstart="dragStart(event)"
       ondragover="dragOver(event)"
       ondrop="drop(event, {{$.ID}})">
    <!-- drag handle icon + question content -->
  </div>
  {{end}}
</div>
```

After drop, collect the new order and POST to `/admin/round/{roundID}/reorder-questions` via fetch:
```js
function drop(event, roundID) {
  // reorder DOM, collect question IDs in new order
  // fetch POST /admin/round/{roundID}/reorder-questions with ordered IDs
}
```

Keep it simple — no animation library, just native drag and drop. A ≡ drag handle icon on the left of each question row signals it's draggable.

---

## Feature 5 — Delete and re-upload video in video round

Currently a question can have a video uploaded at creation time but it can't be replaced.

### Backend — replace video endpoint
In `PostUpdateQuestion` (the existing edit handler), extend it to handle a new video upload:
- If `video_file` is present in the form: delete the old video file from disk, save the new one, update `video_filename` in DB.
- If no new `video_file` uploaded: leave existing video unchanged.
- Add a separate `PostDeleteVideo` handler for explicit removal without replacement:
  ```
  POST /admin/question/{questionID}/delete-video
  ```
  Deletes the file from disk, sets `video_filename = NULL` in DB.
  Returns an HTMX partial showing the "upload video" form again.

### Frontend
In the quiz editor, for video-round questions, show the current video state:

**If video exists:**
```
📹 video_filename.mp4  [▶ Preview] [🗑 Delete video]
```
- "Delete video" posts to `/admin/question/{questionID}/delete-video` and swaps the section with the upload form.
- Also allow re-upload inline: show a small "Replace video" file input below the existing video name (separate from the delete button).

**If no video:**
```
[Choose video file] (file input)
```

Wrap this section in an `id="video-section-{{.ID}}"` div so HTMX can target it for swaps.

The existing `PostUpdateQuestion` form should be a multipart form so the video file input works correctly.

---

## Done-When Criteria

- [ ] Offline quiz question form has no answer-type selector, only correct answer field
- [ ] `PostQuestion` / `PostUpdateQuestion` force `question_type = 'open'` for offline quizzes
- [ ] Each quiz on /admin has a Delete button with inline HTMX confirmation
- [ ] `PostDeleteQuiz` cascades delete: files + DB rows
- [ ] `questions.image_filename` column added via safe migration
- [ ] Image upload works for text-round questions (saved to DATA_DIR/images/)
- [ ] Image displayed above question text in player view when set
- [ ] Questions can be reordered via drag-and-drop in quiz editor
- [ ] `POST /admin/round/{roundID}/reorder-questions` updates order_index correctly
- [ ] Existing video shown in quiz editor with Delete and Replace options
- [ ] `POST /admin/question/{questionID}/delete-video` removes file and swaps UI
- [ ] Video replacement in PostUpdateQuestion deletes old file, saves new one
- [ ] `go build ./...` passes with CGO_ENABLED=0
- [ ] Open PR against main when done

---

## Constraints

- Go binary: `/home/mundi/go-sdk/go/bin/go`
- CGO_ENABLED=0 for local builds
- Per-page template render pattern — never load multiple page templates into one `*template.Template`
- No new JS libraries — use HTMX for server interactions, native HTML5 drag-and-drop for reorder
- Branch: `005-quiz-editor-improvements`
- Conventional commits, one commit per logical change
- Static files: videos at `{DATA_DIR}/videos/`, images at `{DATA_DIR}/images/`

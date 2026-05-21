# Feature: Multi-Question Video Groups

## Background

Currently each question can have its own `video_filename`. For video rounds, the
host plays one clip and players answer one question about it.

We want to support **video groups**: one video clip shared by multiple questions.
The host plays the clip once, then steps through all questions that belong to it.

This feature must work for both `online` and `offline` quiz modes.

---

## Data Model Changes

### New table: `video_groups`

```sql
CREATE TABLE IF NOT EXISTS video_groups (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    round_id       INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    title          TEXT NOT NULL DEFAULT '',
    video_filename TEXT,
    order_index    INTEGER NOT NULL DEFAULT 0
);
```

Add via ALTER migration (safe to run on existing DBs):
- `ALTER TABLE questions ADD COLUMN video_group_id INTEGER REFERENCES video_groups(id) ON DELETE SET NULL`

### Model additions (`internal/models/models.go`)

```go
type VideoGroup struct {
    ID            int64
    RoundID       int64
    Title         string
    VideoFilename sql.NullString
    OrderIndex    int
}
```

Add `VideoGroupID sql.NullInt64` field to `Question`.

---

## Admin: Quiz Editor

The quiz editor (`templates/admin/quiz_editor.html`) currently shows rounds with
questions. For **video rounds**, update the UI so that:

1. **Video groups panel** is shown instead of a flat question list.
2. Each group shows:
   - Group title (editable inline)
   - Thumbnail / filename of the video (with replace/delete buttons, same pattern as existing question video management)
   - A collapsible list of questions belonging to that group
   - An "+ Add Question" form inside the group
   - A delete-group button (only when group has no questions, or with a confirm)
3. **"+ Add Video Group"** button at the bottom of the round to create a new group.
4. For **text rounds**, behaviour is unchanged (flat question list, no groups).

### New admin handler methods

All in `internal/handlers/admin.go`:

**`PostVideoGroup(w, r)`** — `POST /admin/round/{id}/video-group`
- Creates a new video_group for the round.
- Accepts multipart form: `title` (string), `video_file` (optional file upload).
- Saves video file to `dataDir/videos/` (same logic as existing question video upload).
- Returns HTMX fragment: the new group row (for OOB swap into the groups list).

**`DeleteVideoGroup(w, r)`** — `DELETE /admin/round/{id}/video-group/{gid}`
- Deletes the group and sets `video_group_id = NULL` on all its questions (cascade).
- Deletes the video file from disk if present.
- Returns 200 empty for HTMX row removal.

**`PostVideoGroupVideo(w, r)`** — `POST /admin/round/{id}/video-group/{gid}/video`
- Replaces the video file for the group (same pattern as `PostDeleteVideo`).
- Returns updated group header fragment.

**`DeleteVideoGroupVideo(w, r)`** — `POST /admin/round/{id}/video-group/{gid}/delete-video`
- Clears `video_filename` on the group and deletes file from disk.
- Returns upload-form fragment.

**`PostQuestionInGroup(w, r)`** — `POST /admin/round/{id}/video-group/{gid}/question`
- Same as the existing `PostQuestion` but sets `video_group_id = gid` on the new question.
- Does NOT accept a `video_file` field (video lives on the group).
- Returns HTMX fragment: the new question row inside the group.

When showing the quiz editor, load video groups for video rounds:

```sql
SELECT id, round_id, title, video_filename, order_index
FROM video_groups WHERE round_id = ? ORDER BY order_index
```

And for each group, load its questions:

```sql
SELECT id, round_id, question_text, question_type, correct_answer, options,
       video_filename, image_filename, points, order_index, video_group_id
FROM questions WHERE round_id = ? AND video_group_id = ? ORDER BY order_index
```

Also keep loading ungrouped questions (video_group_id IS NULL) for backward compat.

Pass to template as `RoundData.VideoGroups []VideoGroupWithQuestions` alongside existing `RoundData.Questions` (ungrouped).

---

## Game Flow Changes

### `PostNextQuestion` / question ordering

Currently questions are ordered by `order_index` within a round. With groups,
the ordering should be: groups ordered by their `order_index`, and within each
group questions ordered by their `order_index`. Ungrouped questions come after
all groups (or can be interleaved — keep it simple: ungrouped questions at the
end, ordered by their own `order_index`).

Query for next question in a round (replace existing logic):

```sql
-- Grouped questions, ordered by group then question
SELECT q.id FROM questions q
JOIN video_groups vg ON vg.id = q.video_group_id
WHERE q.round_id = ?
ORDER BY vg.order_index, q.order_index

-- Then ungrouped questions
UNION ALL
SELECT id FROM questions
WHERE round_id = ? AND video_group_id IS NULL
ORDER BY order_index
```

### `video_play` SSE event

The existing `PostVideoPlay` broadcasts `video_play` with `question_id`. The
player side fetches the video filename from the question's `video_filename` field.

With groups, the video filename lives on the group. Update the logic:

- When the current question has a `video_group_id`, look up the group's
  `video_filename` for the SSE payload.
- The SSE event shape gains an optional `video_url` field:
  ```json
  {"question_id": 42, "video_url": "/videos/abc123.mp4"}
  ```
- The player template uses `video_url` if present, else falls back to the
  question's own `video_filename` (backward compat).

### Admin game panel

In the game panel, when the current question belongs to a video group, show:
- The group title above the question text.
- The **Play Video** button (same as now) — but only show it for the **first**
  question in the group (host should only play the video once per group).
  For subsequent questions in the same group, hide the Play Video button and
  instead show a small label "Video already played for this group".

Detect "first question in group": compare `current_question.video_group_id` and
its `order_index` == the minimum `order_index` among questions in that group.

---

## Routes to register in `cmd/server/main.go`

```go
r.Post("/round/{id}/video-group", adminHandler.PostVideoGroup)
r.Delete("/round/{id}/video-group/{gid}", adminHandler.DeleteVideoGroup)
r.Post("/round/{id}/video-group/{gid}/video", adminHandler.PostVideoGroupVideo)
r.Post("/round/{id}/video-group/{gid}/delete-video", adminHandler.DeleteVideoGroupVideo)
r.Post("/round/{id}/video-group/{gid}/question", adminHandler.PostQuestionInGroup)
```

---

## Templates

### `templates/admin/quiz_editor.html`

For video rounds, replace the flat `{{range .Questions}}` block with:

```
{{range .VideoGroups}}
  <div class="video-group ...">
    <div class="group-header">
      <span>{{.Group.Title}}</span>
      [video thumbnail / upload / delete buttons]
      [delete group button]
    </div>
    <div class="group-questions">
      {{range .Questions}}
        [existing question-row partial]
      {{end}}
      [+ Add Question form targeting PostQuestionInGroup]
    </div>
  </div>
{{end}}

{{if .UngroupedQuestions}}
  <div class="ungrouped ...">
    <h4>Ungrouped Questions</h4>
    {{range .UngroupedQuestions}}...{{end}}
  </div>
{{end}}

[+ Add Video Group button]
```

Style consistently with existing dark theme (gray-700 cards, indigo accents).

### Player template (`templates/game/partials/game_state_content.html`)

When `video_play` SSE event arrives, the JS currently shows a `<video>` element.
Update JS to use `event.video_url` if present (group video), else fall back to
existing per-question video logic. No other player-side changes needed.

---

## Constraints & Pitfalls

- **Per-request template parse**: always use `h.render()` / `h.renderPartial()`
  helpers — never a shared template set. See existing handlers for the pattern.
- **CGO_ENABLED=0** for local build checks; container uses CGO_ENABLED=1 (uses
  `modernc.org/sqlite`, not `go-sqlite3`).
- **ALTER migrations**: use the existing safe pattern — check column existence
  with `pragma_table_info` before ALTER, ignore errors.
- **Backward compat**: existing questions with `video_filename` set and
  `video_group_id = NULL` must continue to work exactly as before.
- **Offline mode**: `PostQuestionInGroup` must also force `question_type = 'open'`
  and not require `correct_answer` when quiz mode is offline (same as existing
  `PostQuestion`).
- **File deletion**: always delete orphaned video files from disk when a group
  is deleted or its video is replaced/deleted.
- Go binary: `/home/mundi/go-sdk/go/bin/go`
- Run `CGO_ENABLED=0 /home/mundi/go-sdk/go/bin/go build ./...` to verify — must exit 0.
- Push to branch `006-video-groups`, open a PR against `main`.

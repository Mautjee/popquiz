# Quickstart: Key Integration Scenarios for Testing

**Created**: 2026-05-21

## Scenario 1: Full Game Loop (Text Round)

**Goal**: Verify the complete game loop from quiz creation to final leaderboard.

1. Start server: `PORT=8080 DATA_DIR=./data CGO_ENABLED=1 go run ./cmd/server/`
2. Open browser to `/admin` → create a quiz "Test Quiz"
3. Add a text round "Round 1 — General Knowledge"
4. Add an open question: "What is the capital of France?", correct_answer="Paris"
5. Add a ranged question: "In what year did the Moon landing happen?", correct_answer="1969"
6. Add a multiple choice question: "Largest planet?", options=["Earth","Jupiter","Mars","Venus"], correct_answer="B"
7. Create a game session → note room code "ABC123"
8. Open 3 browser tabs at `/?code=ABC123`:
   - Tab 1: Team "Alpha", Player "Alice" → becomes Head
   - Tab 2: Team "Alpha", Player "Bob" → joins as Member
   - Tab 3: Team "Beta", Player "Charlie" → becomes Head
9. In admin panel, click "Start Round"
10. Verify all 3 tabs see "Question 1" via SSE
11. Alice (Alpha Head) submits "Paris" for Q1
12. Charlie (Beta Head) submits "London" for Q1
13. Admin panel shows "1/2 teams answered"
14. Alice submits "1945" for Q2 (ranged), Charlie submits "1969" (ranged)
15. Alice submits "B" for Q3 (MC), Charlie submits "A" for Q3
16. Admin clicks "End Round"
17. Verify round_reveal shows all answers with correct answers
18. Admin marks Alpha's open answer correct, Beta's incorrect
19. Verify score_update: Alpha gets points for Q1+Q2+Q3(only if B), Beta gets points for Q2+Q3(only if B)
20. Verify ranged scoring: Beta (diff=0) wins ranged, Alpha (diff=24) does not
21. Admin clicks "End Game"
22. Verify `/game/ABC123/results` shows final leaderboard

## Scenario 2: Video Round Sync

**Goal**: Verify video playback synchronisation across devices.

1. Create a quiz with a video round
2. Add a video question with a short MP4 clip
3. Create a game, have 2 teams join
4. Start the round → players see question state but no text yet (video only)
5. Admin clicks "Play Video" → verify all player devices start playing simultaneously
6. Wait for video to end → verify play button is disabled on all devices
7. Admin clicks "Show Question" → verify question text and answer input appear
8. Team Heads submit answers
9. Admin clicks "End Round" → verify round_reveal works

## Scenario 3: Team Head Auto-Promotion

**Goal**: Verify that a disconnected Team Head is replaced within 40 seconds.

1. Create a game with 2+ teams, each with 2+ players
2. Note the current Team Head of Team Alpha
3. Simulate disconnect by setting `last_seen_at` to >30s ago for Team Alpha's Head
4. Wait up to 10 seconds for the background goroutine to detect and promote
5. Verify: Old Head is no longer Head (is_head=0), next player by joined_at is now Head (is_head=1)
6. Verify: head_change SSE event was sent to all players in the game

## Scenario 4: Admin Auth

**Goal**: Verify admin panel protection.

1. Start server with `ADMIN_PASSWORD=secret123`
2. Visit `/admin` without session → redirected to `/admin/login`
3. Submit wrong password → 401, form shows error
4. Submit correct password → redirect to `/admin`, session cookie set
5. Visit `/admin/quiz/new` → access granted
6. Clear cookie → visit `/admin` → redirected to login again
7. Start server with `ADMIN_PASSWORD=` (empty) → visit `/admin` → direct access (dev mode)

## Scenario 5: Mid-Game Join and Team Removal

**Goal**: Verify mid-game joining and host team removal.

1. Create game, start round 1
2. New player joins mid-game with correct team name → sees current question
3. New player tries to join during video question → sees "please wait" message
4. Host removes Team Beta from admin panel
5. Verify: Team Beta's players see "You have been removed" message
6. Verify: Team Beta's data (team, players, answers) is deleted from DB
# PopQuiz 🎉

A self-hosted live quiz night app. The host controls everything — teams join from their phones, answer questions in real time, and a leaderboard is shown at the end.

Supports open questions, ranged guesses, multiple choice, and a **video round** where everyone's clip plays in sync.

---

## Features

- **Teams** — players join under a team name; the first joiner becomes Team Head and submits answers
- **Three question types** — open (host marks), ranged (closest wins), multiple choice (auto-scored)
- **Video round** — host presses Play, all devices start the clip simultaneously
- **Real-time updates** — SSE-powered, no polling, no page reloads
- **No timers** — host controls all pacing
- **Admin panel** — create quizzes, manage games, reveal answers, score rounds

---

## Running locally

```bash
# Requirements: Go 1.22+, gcc (for CGO/SQLite)
git clone https://github.com/Mautjee/popquiz
cd popquiz

DATA_DIR=./data ADMIN_PASSWORD=secret CGO_ENABLED=1 go run ./cmd/server/
```

Open http://localhost:8080

---

## Running with Docker

```bash
docker build -t popquiz .

docker run -p 8080:8080 \
  -e ADMIN_PASSWORD=secret \
  -v popquiz-data:/app/data \
  popquiz
```

The SQLite database is stored in `/app/data` — mount a volume there so it survives restarts.

---

## Environment variables

| Variable         | Default     | Description                                  |
|-----------------|-------------|----------------------------------------------|
| `PORT`           | `8080`      | Port to listen on                            |
| `DATA_DIR`       | `./data`    | Directory for the SQLite database            |
| `ADMIN_PASSWORD` | *(empty)*   | Password for the admin panel. Empty = no auth (dev only) |

---

## How to run a quiz night

### 1. Create a quiz

Go to `/admin` → log in → **New Quiz** → add rounds and questions.

Question types:
- **Open** — players type a free-text answer, host marks it correct/incorrect after the round
- **Ranged** — players enter a number, closest to the correct answer wins (ties both score)
- **Multiple Choice** — players pick A/B/C/D, auto-scored

For a video round, upload a short MP4 clip per question.

### 2. Start a game

In the admin panel, select your quiz → **Create Game** → you'll get a **room code** (e.g. `ABC123`).

Share the URL with players: `https://yoursite.com/?code=ABC123`

### 3. Players join

Players go to the URL, enter their name and team name. The first person on a team becomes the **Team Head** — only the Head submits answers. Others on the same team can follow along.

### 4. Run the quiz

From the admin game panel:

1. **Start Round** → all players see the first question
2. For video questions → click **Play Video** → all devices play in sync
3. Click **Show Question** (video rounds) → answer input appears
4. Watch answer count fill up → click **End Round**
5. Reveal answers, mark open questions, scores update automatically
6. **Next Round** → repeat
7. **End Game** → leaderboard shown to all players

### 5. Team Head promotion

If a Team Head disconnects, the next player by join order is automatically promoted after 30 seconds. The game continues uninterrupted.

---

## Tech stack

- **Go** + [chi](https://github.com/go-chi/chi) router
- **HTMX** for partial page updates
- **Tailwind CSS** (CDN)
- **SQLite** via [go-sqlite3](https://github.com/mattn/go-sqlite3) (WAL mode)
- **SSE** for real-time events (no WebSockets)

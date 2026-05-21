---
description: Build Docker image, start container, and run full browser test of all features
---
Test the popquiz app end-to-end:
1. docker build -t popquiz .
2. Start container on port 8080 with ADMIN_PASSWORD=testpass and DATA_DIR=/app/data
3. Use playwright-cli to click through all features (admin login, create quiz, host game, player join, answer flow, offline mode, results page)
4. Report PASS/FAIL per feature with screenshots of failures

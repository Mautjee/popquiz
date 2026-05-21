package main

import (
	"database/sql"
	"fmt"
	"log"

	"github.com/mundi/popquiz/internal/sse"
	"time"
)

func headPromotionLoop(database *sql.DB, broker *sse.Broker, stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			promoteDisconnectedHeads(database, broker)
		}
	}
}

func promoteDisconnectedHeads(database *sql.DB, broker *sse.Broker) {
	rows, err := database.Query(`
		SELECT p.id, p.team_id, p.name, t.game_id
		FROM players p
		JOIN teams t ON p.team_id = t.id
		WHERE p.is_head = 1
		AND datetime(p.last_seen_at) < datetime('now', '-30 seconds')
	`)
	if err != nil {
		log.Printf("Head promotion query error: %v", err)
		return
	}
	defer rows.Close()

	type disconnectedHead struct {
		ID     int64
		TeamID int64
		Name   string
		GameID int64
	}

	var heads []disconnectedHead
	for rows.Next() {
		var h disconnectedHead
		if err := rows.Scan(&h.ID, &h.TeamID, &h.Name, &h.GameID); err != nil {
			continue
		}
		heads = append(heads, h)
	}

	for _, h := range heads {
		var newHeadID int64
		var newHeadName string
		err := database.QueryRow(`
			SELECT id, name FROM players
			WHERE team_id = ? AND is_head = 0
			AND datetime(last_seen_at) > datetime('now', '-30 seconds')
			ORDER BY datetime(joined_at) ASC
			LIMIT 1
		`, h.TeamID).Scan(&newHeadID, &newHeadName)

		if err != nil {
			continue
		}

		tx, err := database.Begin()
		if err != nil {
			log.Printf("Head promotion tx error: %v", err)
			continue
		}

		if _, err := tx.Exec("UPDATE players SET is_head = 0 WHERE id = ?", h.ID); err != nil {
			tx.Rollback()
			continue
		}
		if _, err := tx.Exec("UPDATE players SET is_head = 1 WHERE id = ?", newHeadID); err != nil {
			tx.Rollback()
			continue
		}

		if err := tx.Commit(); err != nil {
			log.Printf("Head promotion commit error: %v", err)
			continue
		}

		log.Printf("Promoted player %d (%s) to head of team %d", newHeadID, newHeadName, h.TeamID)

		// We need the room_code for the game. Look it up.
		var roomCode string
		if err := database.QueryRow("SELECT room_code FROM games WHERE id = ?", h.GameID).Scan(&roomCode); err != nil {
			log.Printf("Head promotion room_code lookup error: %v", err)
			continue
		}

		eventData := fmt.Sprintf(`{"team_id":%d,"new_head_player_id":%d,"new_head_name":"%s"}`, h.TeamID, newHeadID, newHeadName)
		broker.Publish(roomCode, sse.Event{Type: "head_change", Data: eventData})
		broker.Publish("admin:"+roomCode, sse.Event{
			Type: "head_changed",
			Data: fmt.Sprintf(`{"team_name":"","new_head_name":"%s"}`, newHeadName),
		})
	}
}
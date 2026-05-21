package sse

import (
	"fmt"
	"net/http"
	"sync"
)

// Broker manages SSE connections keyed by room code.
// It is a singleton that is passed to handlers via struct injection.
type Broker struct {
	mu          sync.Mutex
	subscribers map[string]map[chan Event]bool // room_code -> set of channels
}

// Event represents an SSE event with a type and data payload.
type Event struct {
	Type string
	Data string
}

// NewBroker creates a new SSE broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[string]map[chan Event]bool),
	}
}

// Subscribe adds a subscriber channel for a given room code.
// It returns the channel that will receive events.
func (b *Broker) Subscribe(roomCode string) chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	if b.subscribers[roomCode] == nil {
		b.subscribers[roomCode] = make(map[chan Event]bool)
	}
	b.subscribers[roomCode][ch] = true
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel for a given room code.
func (b *Broker) Unsubscribe(roomCode string, ch chan Event) {
	b.mu.Lock()
	if subs, ok := b.subscribers[roomCode]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(b.subscribers, roomCode)
		}
	}
	b.mu.Unlock()
}

// Publish sends an event to all subscribers of a given room code.
func (b *Broker) Publish(roomCode string, event Event) {
	b.mu.Lock()
	subs := make(map[chan Event]bool)
	for k, v := range b.subscribers[roomCode] {
		subs[k] = v
	}
	b.mu.Unlock()

	for ch := range subs {
		select {
		case ch <- event:
		default:
			// Channel full, drop event for this subscriber
		}
	}
}

// PublishToRoomAndAdmin is a convenience that publishes to both the player
// room and the admin room (admin room code is "admin:" + roomCode).
func (b *Broker) PublishToRoomAndAdmin(roomCode string, event Event) {
	b.Publish(roomCode, event)
	b.Publish("admin:"+roomCode, event)
}

// ServeHTTP handles the SSE connection for a subscriber.
// The room code is extracted from the URL path or query parameter.
func (b *Broker) ServeHTTP(roomCode string, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := b.Subscribe(roomCode)
	defer b.Unsubscribe(roomCode, ch)

	// Send initial connection confirmation
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Close notifier
	notify := r.Context().Done()

	for {
		select {
		case <-notify:
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, event.Data)
			flusher.Flush()
		}
	}
}

// RoomSubscriberCount returns the number of subscribers for a room.
func (b *Broker) RoomSubscriberCount(roomCode string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers[roomCode])
}
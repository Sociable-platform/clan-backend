// Package events defines the messages services send each other through RabbitMQ,
// and the Publisher that sends them.
//
// Every event goes to one topic exchange, Exchange, with the event type as routing key
// (for example "friend.accepted"). Which queues receive it is decided by the bindings in
// deploy/rabbitmq/definitions.json, not by the publisher.
package events

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Exchange is the topic exchange every service publishes to.
const Exchange = "socialkit.events"

// Event is the envelope around every message. The fields inside Data depend on Type;
// they are listed in README.md ("RabbitMQ events").
type Event struct {
	ID         string         `json:"event_id"`
	Type       string         `json:"type"`
	OccurredAt time.Time      `json:"occurred_at"`
	GameID     string         `json:"game_id"`
	Data       map[string]any `json:"data,omitempty"`
}

// New returns an event with a fresh random ID and the current time (UTC).
func New(eventType, gameID string, data map[string]any) Event {
	return Event{
		ID:         newID(),
		Type:       eventType,
		OccurredAt: time.Now().UTC(),
		GameID:     gameID,
		Data:       data,
	}
}

// newID returns a random UUID (version 4), for example "3f1c0b7e-9a2d-4c51-8e0f-6b7a1d2c3e4f".
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])  // crypto/rand.Read never fails (Go 1.24+ crashes the program instead)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Publisher sends events to the other services.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Noop drops every event. It is used when no broker is configured.
type Noop struct{}

// Publish does nothing and never fails.
func (Noop) Publish(context.Context, Event) error { return nil }

// Safe wraps p so that a failed publish is logged and not returned.
// Services use it because a user's request must not fail when the broker is down.
func Safe(p Publisher, log *slog.Logger) Publisher {
	return safe{p: p, log: log}
}

type safe struct {
	p   Publisher
	log *slog.Logger
}

func (s safe) Publish(ctx context.Context, e Event) error {
	if err := s.p.Publish(ctx, e); err != nil {
		s.log.Error("publish event failed", "event_id", e.ID, "type", e.Type, "error", err)
	}
	return nil
}

// Recorder keeps every event instead of sending it. Tests use it to check what was published.
type Recorder struct {
	mu     sync.Mutex
	events []Event

	// Err, when set, is returned by Publish and the event is not kept.
	Err error
}

// Publish keeps e, or returns r.Err when it is set.
func (r *Recorder) Publish(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.events = append(r.events, e)
	return nil
}

// Events returns a copy of the kept events, in the order they were published.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

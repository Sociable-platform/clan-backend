// Package frame builds the JSON frames the realtime service sends to browsers over the WebSocket.
//
// Every frame has the same envelope as an event on the queue (events.Event in pkg/events):
//
//	{ "event_id": "...", "type": "message.new", "occurred_at": "...", "game_id": "...", "data": { ... } }
//
// so the SDK reads one message format whether it came from RabbitMQ or from this service. Only the
// frames the SERVER sends have the envelope. Frames the client sends stay {type, id, data}: the game
// always comes from the verified token, never from a frame.
package frame

import (
	"encoding/json"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// TimeFormat is how occurred_at is written: UTC with milliseconds, e.g. "2026-11-10T09:41:07.512Z".
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Frame is the envelope of every frame the server sends. The field names are those of events.Event.
type Frame struct {
	EventID    string `json:"event_id"`    // random UUID of this one frame; the same frame sent to several players has one ID
	Type       string `json:"type"`        // for example "message.new"
	OccurredAt string `json:"occurred_at"` // when it happened: RFC 3339, UTC, always three decimals ("2026-11-10T09:41:07.512Z")
	GameID     string `json:"game_id"`     // the game of the connection(s) it is sent to
	Data       any    `json:"data"`        // always present; {} when there is nothing to say
}

// New returns a frame of type typ for gameID. occurredAt is when the thing the frame tells about
// happened (the time of the queue event, the time a message was stored); the zero time means now.
// The time is written in UTC with exactly three decimals, so every frame has the same shape.
func New(gameID, typ string, occurredAt time.Time, data any) Frame {
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	if data == nil {
		data = struct{}{}
	}
	return Frame{
		EventID:    events.NewID(),
		Type:       typ,
		OccurredAt: occurredAt.UTC().Format(TimeFormat),
		GameID:     gameID,
		Data:       data,
	}
}

// Encode returns the JSON of a new frame.
func Encode(gameID, typ string, occurredAt time.Time, data any) ([]byte, error) {
	return json.Marshal(New(gameID, typ, occurredAt, data))
}

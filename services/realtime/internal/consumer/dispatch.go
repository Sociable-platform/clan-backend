package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// Event types this service handles. Every other type that reaches the queue (clan.*, player.*,
// config.published are added by later tasks) is acknowledged and ignored.
const (
	typeFriendRequestSent = "friend.request_sent"
	typeFriendAccepted    = "friend.accepted"
	typeFriendRemoved     = "friend.removed"
)

// Events is what the dispatcher needs from the service layer.
type Events interface {
	FriendRequestSent(ctx context.Context, gameID, requestID string, from service.Player, toID string) ([]service.Push, error)
	FriendAccepted(ctx context.Context, gameID string, a, b service.Player) ([]service.Push, error)
	FriendRemoved(ctx context.Context, gameID, aID, bID string) ([]service.Push, error)
}

// Sender writes an encoded frame to the open WebSockets of some players of a game.
// The hub does this; it never blocks.
type Sender interface {
	SendToPlayers(gameID string, playerIDs []string, frame []byte)
}

// Dispatcher decodes events, calls the service and sends the resulting frames. It is the Handler of
// the Consumer.
type Dispatcher struct {
	events Events
	send   Sender
	log    *slog.Logger
}

// NewDispatcher returns a Dispatcher.
func NewDispatcher(events Events, send Sender, log *slog.Logger) *Dispatcher {
	return &Dispatcher{events: events, send: send, log: log}
}

// playerData is a player inside an event's data.
type playerData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type friendRequestSentData struct {
	RequestID string     `json:"request_id"`
	From      playerData `json:"from"`
	ToID      string     `json:"to_id"`
}

type friendAcceptedData struct {
	A playerData `json:"a"`
	B playerData `json:"b"`
}

type friendRemovedData struct {
	AID string `json:"a_id"`
	BID string `json:"b_id"`
}

// Handle handles one message body. It returns an error wrapping ErrDrop when the event is unusable,
// any other error when it should be tried again.
func (d *Dispatcher) Handle(ctx context.Context, body []byte) error {
	// The envelope is the same struct Social publishes (pkg/events), so a renamed field breaks both sides at compile time.
	var env events.Event
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%w: the body is not an event", ErrDrop)
	}

	var pushes []service.Push
	var err error
	switch env.Type {
	case typeFriendRequestSent:
		var data friendRequestSentData
		if err = decode(env.Data, &data); err == nil {
			pushes, err = d.events.FriendRequestSent(ctx, env.GameID, data.RequestID, player(data.From), data.ToID)
		}
	case typeFriendAccepted:
		var data friendAcceptedData
		if err = decode(env.Data, &data); err == nil {
			pushes, err = d.events.FriendAccepted(ctx, env.GameID, player(data.A), player(data.B))
		}
	case typeFriendRemoved:
		var data friendRemovedData
		if err = decode(env.Data, &data); err == nil {
			pushes, err = d.events.FriendRemoved(ctx, env.GameID, data.AID, data.BID)
		}
	default:
		d.log.Debug("ignoring event nobody handles yet", "event_id", env.ID, "type", env.Type)
		return nil
	}
	if err != nil {
		if errors.Is(err, service.ErrInvalidEvent) {
			err = fmt.Errorf("%w: %w", ErrDrop, err)
		}
		return fmt.Errorf("event %s (%s): %w", env.ID, env.Type, err)
	}

	// The data is stored; now tell whoever is connected. Sending cannot fail: a player who is not
	// connected is skipped and one with a full queue is disconnected by the hub.
	for _, p := range pushes {
		frame, err := json.Marshal(struct {
			Type string `json:"type"`
			Data any    `json:"data"`
		}{p.Type, p.Data})
		if err != nil {
			// Our own data does not encode: a bug, and not one a retry fixes.
			return fmt.Errorf("%w: encode %s frame: %w", ErrDrop, p.Type, err)
		}
		d.send.SendToPlayers(p.GameID, p.PlayerIDs, frame)
	}
	d.log.Info("event handled", "event_id", env.ID, "type", env.Type, "game_id", env.GameID, "frames", len(pushes))
	return nil
}

// decode copies an event's data into the struct of its type. Missing data, or data of the
// wrong shape, makes the event unusable.
func decode(data map[string]any, into any) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: the event has no data", ErrDrop)
	}
	raw, err := json.Marshal(data)
	if err == nil {
		err = json.Unmarshal(raw, into)
	}
	if err != nil {
		return fmt.Errorf("%w: the event data does not fit its type", ErrDrop)
	}
	return nil
}

func player(p playerData) service.Player { return service.Player{ID: p.ID, Name: p.Name} }

package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// Event types this service handles (docs/events.md). Every other type that reaches the queue
// (clan.*, player.blocked, config.published are added by later tasks) is acknowledged and ignored.
const (
	typeFriendRequestSent      = "friend.request_sent"
	typeFriendRequestCancelled = "friend.request_cancelled"
	typeFriendRequestDeclined  = "friend.request_declined"
	typeFriendAccepted         = "friend.accepted"
	typeFriendRemoved          = "friend.removed"
	typePlayerRenamed          = "player.renamed"
)

// Events is what the dispatcher needs from the service layer.
type Events interface {
	FriendRequestSent(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendRequestCancelled(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendRequestDeclined(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendAccepted(ctx context.Context, gameID, requestID string, a, b service.Player) ([]service.Push, error)
	FriendRemoved(ctx context.Context, gameID, aID, bID, removedBy string) ([]service.Push, error)
	PlayerRenamed(ctx context.Context, gameID string, p service.Player) ([]service.Push, error)
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

// friendRequestData is the data of friend.request_sent, friend.request_cancelled and
// friend.request_declined. request_id is optional in friend.request_sent only, so that an older
// event without it still works (the notification then has no requestId).
type friendRequestData struct {
	RequestID string     `json:"request_id"`
	Sender    playerData `json:"sender"`
	Receiver  playerData `json:"receiver"`
}

// friendAcceptedData is the data of friend.accepted: the two new friends, and the request that was
// accepted: required by the agreed format, but optional here so that an older event still works.
type friendAcceptedData struct {
	RequestID string       `json:"request_id"`
	Players   []playerData `json:"players"`
}

// friendRemovedData is the data of friend.removed: the two former friends and who ended it.
type friendRemovedData struct {
	Players   []playerData `json:"players"`
	RemovedBy string       `json:"removed_by"`
}

// playerRenamedData is the data of player.renamed.
type playerRenamedData struct {
	Player playerData `json:"player"`
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
	case typeFriendRequestSent, typeFriendRequestCancelled, typeFriendRequestDeclined:
		var data friendRequestData
		if err = decode(env.Data, &data); err == nil {
			pushes, err = d.friendRequest(ctx, env.Type, env.GameID, data)
		}
	case typeFriendAccepted:
		var data friendAcceptedData
		if err = decode(env.Data, &data); err == nil && len(data.Players) != 2 {
			err = fmt.Errorf("%w: friend.accepted needs exactly two players, got %d", ErrDrop, len(data.Players))
		}
		if err == nil {
			pushes, err = d.events.FriendAccepted(ctx, env.GameID, data.RequestID, player(data.Players[0]), player(data.Players[1]))
		}
	case typeFriendRemoved:
		var data friendRemovedData
		if err = decode(env.Data, &data); err == nil && len(data.Players) != 2 {
			err = fmt.Errorf("%w: friend.removed needs exactly two players, got %d", ErrDrop, len(data.Players))
		}
		if err == nil {
			pushes, err = d.events.FriendRemoved(ctx, env.GameID, data.Players[0].ID, data.Players[1].ID, data.RemovedBy)
		}
	case typePlayerRenamed:
		var data playerRenamedData
		if err = decode(env.Data, &data); err == nil {
			pushes, err = d.events.PlayerRenamed(ctx, env.GameID, player(data.Player))
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
		// The frame has the envelope of an event too; it happened when the event did.
		encoded, err := frame.Encode(p.GameID, p.Type, env.OccurredAt, p.Data)
		if err != nil {
			// Our own data does not encode: a bug, and not one a retry fixes.
			return fmt.Errorf("%w: encode %s frame: %w", ErrDrop, p.Type, err)
		}
		d.send.SendToPlayers(p.GameID, p.PlayerIDs, encoded)
	}
	d.log.Info("event handled", "event_id", env.ID, "type", env.Type, "game_id", env.GameID, "frames", len(pushes))
	return nil
}

// friendRequest calls the service for the three events that have the data of a friend request.
func (d *Dispatcher) friendRequest(ctx context.Context, typ, gameID string, data friendRequestData) ([]service.Push, error) {
	sender := player(data.Sender)
	switch typ {
	case typeFriendRequestCancelled:
		return d.events.FriendRequestCancelled(ctx, gameID, data.RequestID, sender, data.Receiver.ID)
	case typeFriendRequestDeclined:
		return d.events.FriendRequestDeclined(ctx, gameID, data.RequestID, sender, data.Receiver.ID)
	default:
		return d.events.FriendRequestSent(ctx, gameID, data.RequestID, sender, data.Receiver.ID)
	}
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

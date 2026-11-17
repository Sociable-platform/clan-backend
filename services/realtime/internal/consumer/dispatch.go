package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// Event types this service handles (docs/events.md). Every other type that reaches the queue is
// acknowledged and ignored.
const (
	typeFriendRequestSent      = "friend.request_sent"
	typeFriendRequestCancelled = "friend.request_cancelled"
	typeFriendRequestDeclined  = "friend.request_declined"
	typeFriendAccepted         = "friend.accepted"
	typeFriendRemoved          = "friend.removed"
	typePlayerRenamed          = "player.renamed"
	typePlayerBlocked          = "player.blocked"
	typePlayerUnblocked        = "player.unblocked"
	typeConfigPublished        = "config.published"
	typeGameSuspended          = "game.suspended"
	typeGameResumed            = "game.resumed"
	typeClanCreated            = "clan.created"
	typeClanMemberJoined       = "clan.member_joined"
	typeClanMemberLeft         = "clan.member_left"
	typeClanDisbanded          = "clan.disbanded"
	typeClanJoinRequested      = "clan.join_requested"
	typeClanJoinDeclined       = "clan.join_request_declined"

	// Realtime reads these two but has nothing to do for them: it stores neither the clan's name nor
	// the members' roles, and the contract has no frame for either.
	typeClanRenamed           = "clan.renamed"
	typeClanMemberRoleChanged = "clan.member_role_changed"
)

// reasonKicked is the clan.member_left reason of a player an owner or officer removed.
const reasonKicked = "kicked"

// frameConfigUpdated is the frame every connection of a game gets when the game publishes a config.
const frameConfigUpdated = "config.updated"

// Events is what the dispatcher needs from the service layer.
type Events interface {
	FriendRequestSent(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendRequestCancelled(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendRequestDeclined(ctx context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error)
	FriendAccepted(ctx context.Context, gameID, requestID string, a, b service.Player) ([]service.Push, error)
	FriendRemoved(ctx context.Context, gameID string, a, b service.Player, removedBy string) ([]service.Push, error)
	PlayerRenamed(ctx context.Context, gameID string, p service.Player) ([]service.Push, error)
	PlayerBlocked(ctx context.Context, gameID, blockerID, blockedID string) ([]service.Push, error)
	PlayerUnblocked(ctx context.Context, gameID, blockerID, blockedID string) ([]service.Push, error)
	ClanCreated(ctx context.Context, gameID, clanID string, owner service.Player) ([]service.Push, error)
	ClanMemberJoined(ctx context.Context, gameID, clanID string, p service.Player) ([]service.Push, error)
	ClanMemberLeft(ctx context.Context, gameID, clanID, playerID string, kicked bool, by *service.Player) ([]service.Push, error)
	ClanDisbanded(ctx context.Context, gameID, clanID string) ([]service.Push, error)
	ClanJoinRequested(ctx context.Context, gameID, clanID, clanName, requestID string, p service.Player, managers []string) ([]service.Push, error)
	ClanJoinRequestDeclined(ctx context.Context, gameID, clanID, clanName string, p service.Player) ([]service.Push, error)
	// HandleOnce runs fn unless the event was handled before (handled is false), recording it in the
	// same transaction as what fn stores. fn's error is returned as it is.
	HandleOnce(ctx context.Context, gameID, eventID string, fn func(ctx context.Context) error) (handled bool, err error)
}

// Sender writes an encoded frame to the open WebSockets of some players of a game.
// The hub does this; it never blocks.
type Sender interface {
	SendToPlayers(gameID string, playerIDs []string, frame []byte)
}

// ConfigCache is what the dispatcher needs to answer config.published. gameconfig.Cache implements it.
type ConfigCache interface {
	// Published fetches the config again and swaps it in. It returns the version to tell the players
	// about and whether to tell them at all. An error means try the event again.
	Published(ctx context.Context, gameID string, version int) (current int, send bool, err error)
}

// GameStatus is what the dispatcher needs to answer game.suspended and game.resumed.
// gameconfig.Cache implements it.
type GameStatus interface {
	// SetSuspended records at once what the event said, so the next connection sees it.
	SetSuspended(gameID string, suspended bool)
	// Resumed asks Platform, which has the final say, and keeps the answer. An error means try the event again.
	Resumed(ctx context.Context, gameID string) error
}

// GameCloser closes every open WebSocket of a game with the suspended close code (4403). The hub does
// this; it never blocks. It returns how many connections it closed.
type GameCloser interface {
	CloseGameSuspended(gameID string) int
}

// Broadcaster writes an encoded frame to every open WebSocket of a game. The hub does this; it never blocks.
type Broadcaster interface {
	SendToGame(gameID string, frame []byte)
}

// Dispatcher decodes events, calls the service and sends the resulting frames. It is the Handler of
// the Consumer.
type Dispatcher struct {
	events Events
	send   Sender
	log    *slog.Logger

	config    ConfigCache // nil: config.published is acknowledged and ignored
	broadcast Broadcaster

	gameStatus GameStatus // nil: game.suspended and game.resumed are acknowledged and ignored
	closer     GameCloser
}

// NewDispatcher returns a Dispatcher.
func NewDispatcher(events Events, send Sender, log *slog.Logger) *Dispatcher {
	return &Dispatcher{events: events, send: send, log: log}
}

// WithConfig makes the dispatcher handle config.published: refresh cache, then tell every connection of the game.
func (d *Dispatcher) WithConfig(cache ConfigCache, to Broadcaster) *Dispatcher {
	d.config, d.broadcast = cache, to
	return d
}

// WithGameStatus makes the dispatcher handle game.suspended (close every connection of the game) and
// game.resumed (refresh its status).
func (d *Dispatcher) WithGameStatus(status GameStatus, closer GameCloser) *Dispatcher {
	d.gameStatus, d.closer = status, closer
	return d
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

// blockData is the data of player.blocked and player.unblocked.
type blockData struct {
	BlockerID string `json:"blocker_id"`
	BlockedID string `json:"blocked_id"`
}

// clanRef is a clan inside an event's data: Social's clan ID and its name.
type clanRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// clanMemberData is the data of clan.created (player is the owner) and clan.member_joined.
type clanMemberData struct {
	Clan   clanRef    `json:"clan"`
	Player playerData `json:"player"`
}

// clanMemberLeftData is the data of clan.member_left. new_owner is in the event too; Realtime does not
// need it. kicked_by is who removed the player: set with reason "kicked", otherwise null.
type clanMemberLeftData struct {
	Clan     clanRef     `json:"clan"`
	Player   playerData  `json:"player"`
	Reason   string      `json:"reason"` // "left" or "kicked"
	KickedBy *playerData `json:"kicked_by"`
}

// clanJoinRequestedData is the data of clan.join_requested: a player asked to join a closed clan, and managers
// are the IDs of the members who may decide on it (the owner and every role that may decide on requests).
type clanJoinRequestedData struct {
	Clan      clanRef    `json:"clan"`
	Player    playerData `json:"player"`
	RequestID string     `json:"request_id"`
	Managers  []string   `json:"managers"`
}

// clanJoinDeclinedData is the data of clan.join_request_declined. declined_by is in the event too; Realtime
// does not need it.
type clanJoinDeclinedData struct {
	Clan   clanRef    `json:"clan"`
	Player playerData `json:"player"`
}

// clanDisbandedData is the data of clan.disbanded.
type clanDisbandedData struct {
	Clan clanRef `json:"clan"`
}

// configPublishedData is the data of config.published.
type configPublishedData struct {
	Version int `json:"version"`
}

// Handle handles one message body. It returns an error wrapping ErrDrop when the event is unusable,
// any other error when it should be tried again.
func (d *Dispatcher) Handle(ctx context.Context, body []byte) error {
	// The envelope is the same struct Social publishes (pkg/events), so a renamed field breaks both sides at compile time.
	var env events.Event
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%w: the body is not an event", ErrDrop)
	}

	switch {
	case env.Type == typeClanRenamed || env.Type == typeClanMemberRoleChanged:
		d.log.Debug("event needs nothing from realtime", "event_id", env.ID, "type", env.Type)
		return nil
	case env.Type == typeConfigPublished:
		if d.config == nil {
			d.log.Debug("ignoring config.published: no config cache", "event_id", env.ID)
			return nil
		}
		return d.configPublished(ctx, env)
	case env.Type == typeGameSuspended || env.Type == typeGameResumed:
		if d.gameStatus == nil {
			d.log.Debug("ignoring game status event: no game status", "event_id", env.ID, "type", env.Type)
			return nil
		}
		return d.gameStatusChanged(ctx, env)
	case !handledOnce(env.Type):
		d.log.Debug("ignoring event nobody handles yet", "event_id", env.ID, "type", env.Type)
		return nil
	}

	// A copy of an event that was handled before is skipped: delivery is at least once. The event's ID
	// is recorded in the transaction of everything the event stores, so a failure keeps neither.
	var pushes []service.Push
	handled, err := d.events.HandleOnce(ctx, env.GameID, env.ID, func(ctx context.Context) error {
		var err error
		pushes, err = d.store(ctx, env)
		return err
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidEvent) {
			err = fmt.Errorf("%w: %w", ErrDrop, err)
		}
		return fmt.Errorf("event %s (%s): %w", env.ID, env.Type, err)
	}
	if !handled {
		d.log.Debug("skipping an event handled before", "event_id", env.ID, "type", env.Type, "game_id", env.GameID)
		return nil
	}

	// The data is committed; now tell whoever is connected. Sending cannot fail: a player who is not
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

// handledOnce reports whether events of this type go through HandleOnce: the friend, player and clan
// events, whose copies would store or send something again. config.published and the game status
// events are not: repeating them changes nothing, and they ask Platform, which must not happen inside
// a database transaction.
func handledOnce(typ string) bool {
	switch typ {
	case typeFriendRequestSent, typeFriendRequestCancelled, typeFriendRequestDeclined, typeFriendAccepted,
		typeFriendRemoved, typePlayerRenamed, typePlayerBlocked, typePlayerUnblocked,
		typeClanCreated, typeClanMemberJoined, typeClanMemberLeft, typeClanDisbanded,
		typeClanJoinRequested, typeClanJoinDeclined:
		return true
	}
	return false
}

// store decodes the data of an event that handledOnce accepts and calls the service with it. It
// returns the frames to send once the event's writes have committed.
func (d *Dispatcher) store(ctx context.Context, env events.Event) ([]service.Push, error) {
	switch env.Type {
	case typeFriendRequestSent, typeFriendRequestCancelled, typeFriendRequestDeclined:
		var data friendRequestData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		return d.friendRequest(ctx, env.Type, env.GameID, data)
	case typeFriendAccepted:
		var data friendAcceptedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		if len(data.Players) != 2 {
			return nil, fmt.Errorf("%w: friend.accepted needs exactly two players, got %d", ErrDrop, len(data.Players))
		}
		return d.events.FriendAccepted(ctx, env.GameID, data.RequestID, player(data.Players[0]), player(data.Players[1]))
	case typeFriendRemoved:
		var data friendRemovedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		if len(data.Players) != 2 {
			return nil, fmt.Errorf("%w: friend.removed needs exactly two players, got %d", ErrDrop, len(data.Players))
		}
		return d.events.FriendRemoved(ctx, env.GameID, player(data.Players[0]), player(data.Players[1]), data.RemovedBy)
	case typePlayerRenamed:
		var data playerRenamedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		return d.events.PlayerRenamed(ctx, env.GameID, player(data.Player))
	case typePlayerBlocked, typePlayerUnblocked:
		var data blockData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		if env.Type == typePlayerBlocked {
			return d.events.PlayerBlocked(ctx, env.GameID, data.BlockerID, data.BlockedID)
		}
		return d.events.PlayerUnblocked(ctx, env.GameID, data.BlockerID, data.BlockedID)
	case typeClanCreated, typeClanMemberJoined:
		var data clanMemberData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		if env.Type == typeClanCreated {
			return d.events.ClanCreated(ctx, env.GameID, data.Clan.ID, player(data.Player))
		}
		return d.events.ClanMemberJoined(ctx, env.GameID, data.Clan.ID, player(data.Player))
	case typeClanMemberLeft:
		var data clanMemberLeftData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		var by *service.Player
		if data.KickedBy != nil {
			p := player(*data.KickedBy)
			by = &p
		}
		return d.events.ClanMemberLeft(ctx, env.GameID, data.Clan.ID, data.Player.ID, data.Reason == reasonKicked, by)
	case typeClanJoinRequested:
		var data clanJoinRequestedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		return d.events.ClanJoinRequested(ctx, env.GameID, data.Clan.ID, data.Clan.Name, data.RequestID, player(data.Player), data.Managers)
	case typeClanJoinDeclined:
		var data clanJoinDeclinedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		return d.events.ClanJoinRequestDeclined(ctx, env.GameID, data.Clan.ID, data.Clan.Name, player(data.Player))
	case typeClanDisbanded:
		var data clanDisbandedData
		if err := decode(env.Data, &data); err != nil {
			return nil, err
		}
		return d.events.ClanDisbanded(ctx, env.GameID, data.Clan.ID)
	}
	return nil, fmt.Errorf("%w: realtime does not store %s events", ErrDrop, env.Type) // handledOnce and this switch disagree
}

// configPublished handles config.published: the game has a new config version. The cache fetches it
// from Platform; when that fails the old config stays and the error makes the consumer try the event
// once more. Then every connection of that game (and no other) gets config.updated { version }.
func (d *Dispatcher) configPublished(ctx context.Context, env events.Event) error {
	var data configPublishedData
	if err := decode(env.Data, &data); err != nil {
		return fmt.Errorf("event %s (%s): %w", env.ID, env.Type, err)
	}
	if data.Version < 1 {
		return fmt.Errorf("event %s (%s): %w: version must be 1 or more", env.ID, env.Type, ErrDrop)
	}
	current, send, err := d.config.Published(ctx, env.GameID, data.Version)
	if errors.Is(err, gameconfig.ErrSuspended) {
		// Platform gives no config of a suspended game, and its connections are closed anyway. The
		// version is picked up when the game is resumed (see GameStatus.Resumed), so there is nothing to retry.
		d.log.Info("event handled, the game is suspended", "event_id", env.ID, "type", env.Type, "game_id", env.GameID)
		return nil
	}
	if err != nil {
		if errors.Is(err, gameconfig.ErrInvalidGame) {
			err = fmt.Errorf("%w: %w", ErrDrop, err)
		}
		return fmt.Errorf("event %s (%s): %w", env.ID, env.Type, err)
	}
	if !send {
		d.log.Info("event handled, a newer config was already announced", "event_id", env.ID, "type", env.Type, "game_id", env.GameID, "version", data.Version, "current", current)
		return nil
	}
	encoded, err := frame.Encode(env.GameID, frameConfigUpdated, env.OccurredAt, map[string]int{"version": current})
	if err != nil {
		return fmt.Errorf("%w: encode %s frame: %w", ErrDrop, frameConfigUpdated, err)
	}
	d.broadcast.SendToGame(env.GameID, encoded)
	d.log.Info("event handled", "event_id", env.ID, "type", env.Type, "game_id", env.GameID, "version", current)
	return nil
}

// gameStatusChanged handles game.suspended and game.resumed. The event's type is the whole message:
// its data (status) only repeats it.
//
// game.suspended is trusted at once: the status is recorded first, so a connection that is just being
// opened sees it, and then every open connection of that game, and no other, is closed with 4403.
// game.resumed is not: the cache asks Platform, so an old event that is delivered again after a newer
// suspension cannot open the game by mistake. When Platform cannot be asked the error makes the
// consumer try once more, and the status expires by itself after a while.
func (d *Dispatcher) gameStatusChanged(ctx context.Context, env events.Event) error {
	if env.GameID == "" {
		return fmt.Errorf("event %s (%s): %w: no game_id", env.ID, env.Type, ErrDrop)
	}
	if env.Type == typeGameSuspended {
		d.gameStatus.SetSuspended(env.GameID, true)
		closed := d.closer.CloseGameSuspended(env.GameID)
		d.log.Info("event handled", "event_id", env.ID, "type", env.Type, "game_id", env.GameID, "closed", closed)
		return nil
	}
	if err := d.gameStatus.Resumed(ctx, env.GameID); err != nil {
		if errors.Is(err, gameconfig.ErrInvalidGame) {
			err = fmt.Errorf("%w: %w", ErrDrop, err)
		}
		return fmt.Errorf("event %s (%s): %w", env.ID, env.Type, err)
	}
	d.log.Info("event handled", "event_id", env.ID, "type", env.Type, "game_id", env.GameID)
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

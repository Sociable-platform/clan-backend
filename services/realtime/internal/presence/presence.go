// Package presence tells friends when a player comes online or goes offline, and who is online.
//
// A player is online while at least one of their WebSockets is open. When their first one opens,
// their connected friends get presence.update "online" (TEAM-274). When the last one closes
// they are not declared offline at once: a page refresh closes the socket and opens a new one a
// moment later, and friends must not see that as a flash of "offline". The tracker waits a grace
// period (10 s), looks again, and only then sends presence.update "offline" to the player's friends.
// A player in the grace period still counts as online, so coming back within it announces nothing.
//
// Friends are the other members of the player's DM conversations (the people they have a DM with).
// Pending timers live in memory of this one process: when it restarts during a grace period, the
// friends are never told that player went offline.
package presence

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
)

// DefaultGrace is how long a player with no open connection still counts as online.
const DefaultGrace = 10 * time.Second

// friendsTimeout limits the lookup of a player's friends when the grace period ends.
const friendsTimeout = 5 * time.Second

// frameType is the frame friends get when a player comes online or goes offline (openapi/realtime-ws.md).
const frameType = "presence.update"

// The status of a presence.update frame.
const (
	statusOnline  = "online"
	statusOffline = "offline"
)

// Connections tells whether a player has an open connection. The hub implements it.
type Connections interface {
	Online(gameID, playerID string) bool
}

// Friends gives the IDs of the players who share a DM with the player, in the game. The repository implements it.
type Friends interface {
	FriendsOf(ctx context.Context, gameID, playerID string) ([]string, error)
}

// Sender writes an encoded frame to the open WebSockets of some players of a game. The hub implements it.
// Players who are not connected are skipped.
type Sender interface {
	SendToPlayers(gameID string, playerIDs []string, frame []byte)
}

// Options tune a Tracker. Zero values give the defaults.
type Options struct {
	// Grace is how long a player without a connection still counts as online (default 10 s).
	Grace time.Duration
	// Log receives the lookups that failed. Default: no log.
	Log *slog.Logger
	// AfterFunc runs f once after d and returns a function that stops it (reports whether it was
	// stopped in time). Default: time.AfterFunc. Tests replace it with a clock they control.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
	// Now is the clock (default time.Now).
	Now func() time.Time
}

type key struct{ gameID, playerID string }

// offline is one grace period that is running.
type offline struct {
	stop func() bool
}

// Tracker follows who is online. It is safe for concurrent use.
type Tracker struct {
	conns   Connections
	friends Friends
	send    Sender
	grace   time.Duration
	log     *slog.Logger
	after   func(d time.Duration, f func()) (stop func() bool)
	now     func() time.Time

	mu      sync.Mutex
	pending map[key]*offline // players whose last connection closed and whose grace period runs
	closed  bool
}

// New returns a Tracker.
func New(conns Connections, friends Friends, send Sender, opts Options) *Tracker {
	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.AfterFunc == nil {
		opts.AfterFunc = func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Tracker{
		conns: conns, friends: friends, send: send,
		grace: opts.Grace, log: opts.Log, after: opts.AfterFunc, now: opts.Now,
		pending: make(map[key]*offline),
	}
}

// Connected is called after a connection of the player was added to the hub; first is whether it
// is the player's only one (hub.Register). It reports whether the player came online, so their
// friends should be told (AnnounceOnline):
//
//   - a player who came back within the grace period was never offline: the timer is cancelled and
//     the answer is false (a page refresh tells nobody anything);
//   - another tab of a player who is already connected is not news either: false;
//   - otherwise, their first connection: true. Several tabs opening at once give one true, because
//     only one of them is the first in the hub.
func (t *Tracker) Connected(gameID, playerID string, first bool) (cameOnline bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := key{gameID, playerID}
	if p := t.pending[k]; p != nil {
		p.stop()
		delete(t.pending, k)
		return false
	}
	return first && !t.closed
}

// AnnounceOnline sends presence.update "online" for the player to friends: the IDs of their friends
// who are online, as OnlineFriends lists them for the hello frame (the caller passes that list, so the
// friends are looked up once). Only friends with an open connection get it. Call it only when
// Connected reported that the player came online. Sending never blocks.
func (t *Tracker) AnnounceOnline(gameID, playerID string, friends []string) {
	if len(friends) == 0 {
		return
	}
	t.sendStatus(gameID, playerID, statusOnline, friends)
}

// Disconnected is called after the player's last connection was removed from the hub. It starts the
// grace period; when it ends and the player is still not connected, their friends are told.
func (t *Tracker) Disconnected(gameID, playerID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	k := key{gameID, playerID}
	if old := t.pending[k]; old != nil {
		old.stop()
	}
	p := &offline{}
	t.pending[k] = p
	// The timer's function takes t.mu, which is held until here is done: p.stop is set before it can run.
	p.stop = t.after(t.grace, func() { t.expire(k, p) })
}

// Online reports whether the player is online: an open connection, or a grace period that has not ended.
func (t *Tracker) Online(gameID, playerID string) bool {
	if t.conns.Online(gameID, playerID) {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending[key{gameID, playerID}] != nil
}

// OnlineFriends returns the IDs of the player's friends who are online right now. The result is never nil.
func (t *Tracker) OnlineFriends(ctx context.Context, gameID, playerID string) ([]string, error) {
	friends, err := t.friends.FriendsOf(ctx, gameID, playerID)
	if err != nil {
		return nil, err
	}
	online := make([]string, 0, len(friends))
	for _, f := range friends {
		if t.Online(gameID, f) {
			online = append(online, f)
		}
	}
	return online, nil
}

// Close stops every grace period that is running and ignores later disconnects. Call it when the
// service shuts down: everybody is disconnecting then, and nobody should be told.
func (t *Tracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for k, p := range t.pending {
		p.stop()
		delete(t.pending, k)
	}
}

// expire runs when a grace period ended.
func (t *Tracker) expire(k key, p *offline) {
	t.mu.Lock()
	if t.pending[k] != p {
		t.mu.Unlock() // the player came back, or a newer grace period replaced this one
		return
	}
	t.mu.Unlock()

	// The player still counts as online while their friends are looked up.
	ctx, cancel := context.WithTimeout(context.Background(), friendsTimeout)
	friends, err := t.friends.FriendsOf(ctx, k.gameID, k.playerID)
	cancel()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending[k] != p {
		return // came back, or Close ran, while the friends were looked up
	}
	delete(t.pending, k)
	if err != nil {
		t.log.Warn("cannot tell the friends a player went offline", "game_id", k.gameID, "player_id", k.playerID, "error", err)
		return
	}
	if t.conns.Online(k.gameID, k.playerID) {
		return // connected again (Connected has not run yet): they were never offline
	}
	if len(friends) == 0 {
		return
	}
	// Sending never blocks, so it is safe to do while the lock is held; that keeps Connected from
	// slipping in between the check above and the frames.
	t.sendStatus(k.gameID, k.playerID, statusOffline, friends)
}

// sendStatus sends presence.update { playerId, status } for the player to the friends who are connected.
func (t *Tracker) sendStatus(gameID, playerID, status string, friends []string) {
	b, err := frame.Encode(gameID, frameType, t.now(), map[string]string{"playerId": playerID, "status": status})
	if err != nil {
		t.log.Error("encode presence frame", "error", err)
		return
	}
	t.send.SendToPlayers(gameID, friends, b)
}

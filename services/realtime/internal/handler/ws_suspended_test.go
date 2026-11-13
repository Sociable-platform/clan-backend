package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/consumer"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
)

// statusSource is a Platform that answers per game: a config, or the error the test chose
// (gameconfig.ErrSuspended for a suspended game, gameconfig.ErrUnavailable for a Platform that is down).
type statusSource struct {
	mu    sync.Mutex
	state map[string]error
}

func newStatusSource() *statusSource { return &statusSource{state: map[string]error{}} }

func (s *statusSource) set(gameID string, err error) {
	s.mu.Lock()
	s.state[gameID] = err
	s.mu.Unlock()
}

func (s *statusSource) Fetch(_ context.Context, gameID string) (gameconfig.Config, error) {
	s.mu.Lock()
	err := s.state[gameID]
	s.mu.Unlock()
	if err != nil {
		return gameconfig.Config{}, err
	}
	return gameconfig.Config{Version: 1, Settings: json.RawMessage(`{"schemaVersion":1}`)}, nil
}

// suspensionEnv is a running service whose games' status comes from a real gameconfig.Cache.
func suspensionEnv(t *testing.T) (*wsEnv, *statusSource, *gameconfig.Cache) {
	t.Helper()
	src := newStatusSource()
	cache := gameconfig.NewCache(src, gameconfig.Options{})
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) {
		o.Config = cache
		o.Suspension = cache
	})
	return env, src, cache
}

func (e *wsEnv) tokenInGame(t *testing.T, sub, gameID string) string {
	t.Helper()
	return e.token(t, jwt.Claims{Subject: sub, GameID: gameID, Name: "Name of " + sub, Type: jwt.TypePlayer}, time.Hour)
}

// closeOf reads until the connection ends and returns how it was closed. Frames before that are skipped.
func closeOf(t *testing.T, c *websocket.Conn) (code websocket.StatusCode, reason string, frames int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err == nil {
			frames++
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("the connection ended without a close frame: %v", err)
		}
		return ce.Code, ce.Reason, frames
	}
}

func TestWSSuspendedGameIsClosedWith4403AndNoHello(t *testing.T) {
	env, src, _ := suspensionEnv(t)
	src.set(testGameID, gameconfig.ErrSuspended)

	c, resp, err := env.dial(t, env.playerToken(t), "")
	if err != nil {
		t.Fatalf("dial: %v (response %v): the upgrade must succeed, a close code needs a WebSocket", err, resp)
	}
	code, reason, frames := closeOf(t, c)
	if code != 4403 || reason != "game_suspended" || frames != 0 {
		t.Fatalf("closed with %d %q after %d frames, want 4403 game_suspended and no hello", code, reason, frames)
	}
	if n := env.resolveCalls.Load(); n != 0 {
		t.Fatalf("social was asked %d times: a suspended game is refused before that", n)
	}
	if env.h.Hub().Len() != 0 {
		t.Fatal("a refused connection was registered in the hub")
	}
}

// The origin is still checked first, like for any connection.
func TestWSSuspendedGameStillChecksTheOrigin(t *testing.T) {
	env, src, _ := suspensionEnv(t)
	src.set(testGameID, gameconfig.ErrSuspended)
	_, resp, err := env.dial(t, env.playerToken(t), "http://evil.example")
	if err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("dial = %v, %v; want the upgrade refused with 403", err, resp)
	}
}

func TestWSAnotherGameIsNotAffected(t *testing.T) {
	env, src, _ := suspensionEnv(t)
	src.set(testGameID, gameconfig.ErrSuspended)
	_, hello := env.connect(t, env.tokenInGame(t, "u_1", otherGameID))
	if hello["playerId"] == nil {
		t.Fatalf("hello = %v, want a normal hello for a game that is served", hello)
	}
}

// Platform being down must never lock players out.
func TestWSFailsOpenWhenPlatformIsDown(t *testing.T) {
	env, src, _ := suspensionEnv(t)
	src.set(testGameID, gameconfig.ErrUnavailable)
	if _, hello := env.connect(t, env.playerToken(t)); hello["playerId"] == nil {
		t.Fatalf("hello = %v", hello)
	}
}

// flipping answers "served" once and "suspended" after, like a game that is suspended while a
// connection is being set up.
type flipping struct{ calls atomic.Int32 }

func (f *flipping) Suspended(context.Context, string) bool { return f.calls.Add(1) > 1 }

func TestWSAConnectionOpenedWhileTheGameIsSuspendedIsClosedToo(t *testing.T) {
	flip := &flipping{}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Suspension = flip })

	c, resp, err := env.dial(t, env.playerToken(t), "")
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	code, reason, _ := closeOf(t, c)
	if code != 4403 || reason != "game_suspended" {
		t.Fatalf("closed with %d %q, want 4403 game_suspended", code, reason)
	}
	eventually(t, "the connection to leave the hub", func() bool { return env.h.Hub().Len() == 0 })
}

func gameEvent(typ, gameID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"event_id": "e1", "type": typ, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "game_id": gameID,
	})
	return b
}

// The whole story, with real connections: events close the game's open connections within seconds,
// other games are not touched, new connections are refused, and after game.resumed they succeed.
func TestWSSuspendAndResumeEndToEnd(t *testing.T) {
	env, src, cache := suspensionEnv(t)
	dispatcher := consumer.NewDispatcher(nil, nil, slog.Default()).WithGameStatus(cache, env.h.Hub())
	ctx := context.Background()

	// two tabs of two players in the game that will be suspended, one player in another game
	a1, _ := env.connect(t, env.tokenInGame(t, "u_1", testGameID))
	a2, _ := env.connect(t, env.tokenInGame(t, "u_2", testGameID))
	other, _ := env.connect(t, env.tokenInGame(t, "u_1", otherGameID))
	if env.h.Hub().Len() != 3 {
		t.Fatalf("hub has %d connections, want 3", env.h.Hub().Len())
	}

	// An admin suspends the game: Platform now refuses it, and announces it.
	src.set(testGameID, gameconfig.ErrSuspended)
	if err := dispatcher.Handle(ctx, gameEvent("game.suspended", testGameID)); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*websocket.Conn{a1, a2} {
		if code, reason, _ := closeOf(t, c); code != 4403 || reason != "game_suspended" {
			t.Fatalf("connection %d of the suspended game closed with %d %q, want 4403 game_suspended", i, code, reason)
		}
	}
	if typ, _ := exchange(t, other, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("the other game's connection got %q, want it untouched (pong)", typ)
	}
	eventually(t, "the closed connections to leave the hub", func() bool { return env.h.Hub().Len() == 1 })

	// New connections of the suspended game are refused.
	c, _, err := env.dial(t, env.tokenInGame(t, "u_1", testGameID), "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, frames := closeOf(t, c); code != 4403 || frames != 0 {
		t.Fatalf("a new connection closed with %d after %d frames, want 4403 and no hello", code, frames)
	}

	// The admin resumes the game.
	src.set(testGameID, nil)
	if err := dispatcher.Handle(ctx, gameEvent("game.resumed", testGameID)); err != nil {
		t.Fatal(err)
	}
	if _, hello := env.connect(t, env.tokenInGame(t, "u_1", testGameID)); hello["playerId"] == nil {
		t.Fatalf("hello = %v, want a normal hello after game.resumed", hello)
	}
}

// If the resume event is lost, the status expires by itself and the next connection finds out.
func TestWSAGameResumedWithoutAnEventIsServedAgainAfterTheStatusExpires(t *testing.T) {
	src := newStatusSource()
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	cache := gameconfig.NewCache(src, gameconfig.Options{Now: clock, StatusTTL: time.Minute})
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Config = cache; o.Suspension = cache })

	src.set(testGameID, gameconfig.ErrSuspended)
	c, _, err := env.dial(t, env.playerToken(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := closeOf(t, c); code != 4403 {
		t.Fatalf("closed with %d, want 4403", code)
	}

	src.set(testGameID, nil)
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	if _, hello := env.connect(t, env.playerToken(t)); hello["playerId"] == nil {
		t.Fatalf("hello = %v", hello)
	}
}

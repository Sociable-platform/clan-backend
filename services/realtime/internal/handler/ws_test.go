package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

const (
	testGameID    = "a6c1e2f0-1111-4222-8333-944455556666"
	otherGameID   = "00000000-0000-0000-0000-000000000002"
	allowedOrigin = "http://localhost:5174"
)

// socialIDFor is the fake social service's ID for a player: stable, and different for every game's player.
func socialIDFor(sub string) string {
	h := fnv.New64a()
	h.Write([]byte(sub))
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", h.Sum64()&0xffffffffffff)
}

// syncBuffer is a log destination that is safe to use from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// wsEnv is a running realtime handler behind the same middleware chain as production,
// with a fake social service (it is also the handler's PlayerResolver).
type wsEnv struct {
	srv      *httptest.Server
	h        *Handler
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	logs     *syncBuffer
	shutdown context.CancelFunc
	chat     *chatService // the fake service behind message.send

	socialDown   atomic.Bool  // when set, the fake social service is unreachable
	resolveCalls atomic.Int32 // how often the handler asked social
}

// Player is the fake social service: it knows every player with a valid token.
func (e *wsEnv) Player(_ context.Context, token, gameID string) (social.Player, error) {
	e.resolveCalls.Add(1)
	if e.socialDown.Load() {
		return social.Player{}, fmt.Errorf("fake social is down: %w", social.ErrUnavailable)
	}
	claims, err := jwt.Verify(e.pub, token, jwt.TypePlayer, time.Now())
	if err != nil || !strings.EqualFold(claims.GameID, gameID) {
		return social.Player{}, social.ErrUnavailable
	}
	return social.Player{ID: socialIDFor(claims.GameID + "/" + claims.Subject), DisplayName: claims.Name}, nil
}

// newWSEnv starts the service. tweak may change the options (for example the ping timing)
// and the test server (for example its timeouts) before it starts.
func newWSEnv(t *testing.T, tweak func(*WSOptions, *httptest.Server)) *wsEnv {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	shutdownCtx, shutdown := context.WithCancel(context.Background())
	env := &wsEnv{pub: pub, priv: priv, logs: logs, shutdown: shutdown, chat: &chatService{}}
	env.h = New("test", env.chat, WSOptions{
		Verifier: auth.NewVerifier(pub, time.Now),
		Players:  env,
		Origins:  []string{allowedOrigin},
		Shutdown: shutdownCtx,
	}, log)
	mux := http.NewServeMux()
	env.h.Register(mux)
	cors, err := httpx.CORS([]string{allowedOrigin})
	if err != nil {
		t.Fatal(err)
	}
	env.srv = httptest.NewUnstartedServer(httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors))
	if tweak != nil {
		tweak(&env.h.wsOpts, env.srv)
	}
	env.srv.Start()
	t.Cleanup(func() {
		shutdown()
		env.srv.Close()
	})
	return env
}

func (e *wsEnv) token(t *testing.T, c jwt.Claims, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.Sign(e.priv, c, ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// tokenFor is a player token for the player with the game's own ID sub, in testGameID.
func (e *wsEnv) tokenFor(t *testing.T, sub string) string {
	t.Helper()
	return e.token(t, jwt.Claims{Subject: sub, GameID: testGameID, Name: "Name of " + sub, Type: jwt.TypePlayer}, time.Hour)
}

func (e *wsEnv) playerToken(t *testing.T) string { return e.tokenFor(t, "u_42") }

// dial opens /api/v1/ws with the token and optional Origin and does NOT read anything.
// On a refused upgrade it returns the HTTP response.
func (e *wsEnv) dial(t *testing.T, token, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": []string{origin}}
	}
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws"
	if token != "" {
		url += "?token=" + token
	}
	c, resp, err := websocket.Dial(ctx, url, opts)
	if c != nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

// connect opens a socket for the token and returns it together with the hello frame's data.
func (e *wsEnv) connect(t *testing.T, token string) (*websocket.Conn, map[string]any) {
	t.Helper()
	c, resp, err := e.dial(t, token, "")
	if err != nil {
		t.Fatalf("dial: %v (response %v)", err, resp)
	}
	typ, data := readFrame(t, c)
	if typ != "hello" {
		t.Fatalf("first frame is %q, want hello", typ)
	}
	return c, data
}

// mustDial connects as u_42 and consumes the hello frame.
func (e *wsEnv) mustDial(t *testing.T) *websocket.Conn {
	t.Helper()
	c, _ := e.connect(t, e.playerToken(t))
	return c
}

// exchange sends one text frame and returns the server's next frame.
func exchange(t *testing.T, c *websocket.Conn, frame string) (typ string, data map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}
	return readFrame(t, c)
}

func readFrame(t *testing.T, c *websocket.Conn) (string, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("server sent a frame that is not JSON: %q", b)
	}
	return f.Type, f.Data
}

func errorCodeOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("response is not the standard error shape: %q", b)
	}
	return body.Error.Code
}

// eventually waits up to 5 s for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWSPingGetsPong(t *testing.T) {
	c := newWSEnv(t, nil).mustDial(t)

	tests := []struct {
		name        string
		frame       string
		wantReplyTo any
	}{
		{"with id", `{"type":"ping","id":"p-1","data":{}}`, "p-1"},
		{"without id", `{"type":"ping"}`, nil},
		{"without data", `{"type":"ping","id":"p-2"}`, "p-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, data := exchange(t, c, tt.frame)
			if typ != "pong" {
				t.Fatalf("got %q frame, want pong", typ)
			}
			if data["replyTo"] != tt.wantReplyTo {
				t.Fatalf("replyTo = %v, want %v", data["replyTo"], tt.wantReplyTo)
			}
		})
	}
}

func TestWSHello(t *testing.T) {
	env := newWSEnv(t, nil)
	_, data := env.connect(t, env.playerToken(t))

	if want := socialIDFor(testGameID + "/u_42"); data["playerId"] != want {
		t.Fatalf("playerId = %v, want social's ID %s (not the game's own ID u_42)", data["playerId"], want)
	}
	if data["configVersion"] != float64(0) {
		t.Fatalf("configVersion = %v, want 0 (not known yet)", data["configVersion"])
	}
	friends, ok := data["onlineFriends"].([]any)
	if !ok || len(friends) != 0 {
		t.Fatalf("onlineFriends = %#v, want an empty array (not null)", data["onlineFriends"])
	}
}

// The Done-when of the task: two tabs as different players both get hello, and
// closing one does not affect the other.
func TestWSTwoPlayersAreIndependent(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, aliceHello := env.connect(t, env.tokenFor(t, "u_1"))
	bob, bobHello := env.connect(t, env.tokenFor(t, "u_2"))

	aliceID, bobID := aliceHello["playerId"].(string), bobHello["playerId"].(string)
	if aliceID == bobID || aliceID == "" || bobID == "" {
		t.Fatalf("both hello frames carry the same player ID: %q %q", aliceID, bobID)
	}
	if env.h.Hub().Len() != 2 || !env.h.Hub().Online(testGameID, aliceID) || !env.h.Hub().Online(testGameID, bobID) {
		t.Fatal("the hub does not list both players")
	}

	// A frame sent through the hub reaches only the listed player.
	env.h.Hub().SendToPlayers(testGameID, []string{aliceID}, []byte(`{"type":"notification","data":{"kind":"test"}}`))
	if typ, _ := readFrame(t, alice); typ != "notification" {
		t.Fatalf("alice got %q, want notification", typ)
	}
	// If bob had been sent the notification too, it would arrive before this pong.
	if typ, _ := exchange(t, bob, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("bob got %q first, want pong (he was not addressed)", typ)
	}

	// Alice leaves; bob is not affected.
	if err := alice.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("close alice: %v", err)
	}
	eventually(t, "alice to leave the hub", func() bool { return !env.h.Hub().Online(testGameID, aliceID) })
	if !env.h.Hub().Online(testGameID, bobID) || env.h.Hub().Len() != 1 {
		t.Fatal("bob left the hub when alice closed her tab")
	}
	if typ, _ := exchange(t, bob, `{"type":"ping","id":"after"}`); typ != "pong" {
		t.Fatalf("bob got %q after alice left, want pong", typ)
	}
	env.h.Hub().SendToPlayers(testGameID, []string{bobID}, []byte(`{"type":"notification","data":{}}`))
	if typ, _ := readFrame(t, bob); typ != "notification" {
		t.Fatalf("bob got %q, want notification", typ)
	}
}

func TestWSTwoTabsOfOnePlayer(t *testing.T) {
	env := newWSEnv(t, nil)
	tok := env.playerToken(t)
	tab1, hello := env.connect(t, tok)
	tab2, _ := env.connect(t, tok)
	id := hello["playerId"].(string)

	env.h.Hub().SendToPlayers(testGameID, []string{id}, []byte(`{"type":"notification","data":{}}`))
	for i, tab := range []*websocket.Conn{tab1, tab2} {
		if typ, _ := readFrame(t, tab); typ != "notification" {
			t.Fatalf("tab %d got %q, want notification (every tab gets the frame)", i+1, typ)
		}
	}

	tab1.Close(websocket.StatusNormalClosure, "")
	eventually(t, "one tab to leave the hub", func() bool { return env.h.Hub().Len() == 1 })
	if !env.h.Hub().Online(testGameID, id) {
		t.Fatal("the player went offline while a tab is still open")
	}
	if typ, _ := exchange(t, tab2, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("the other tab got %q, want pong", typ)
	}
}

func TestWSGamesAreIsolated(t *testing.T) {
	env := newWSEnv(t, nil)
	// The same game's own player ID "u_1" exists in two games: two different players.
	c1, h1 := env.connect(t, env.token(t, jwt.Claims{Subject: "u_1", GameID: testGameID, Name: "A", Type: jwt.TypePlayer}, time.Hour))
	c2, h2 := env.connect(t, env.token(t, jwt.Claims{Subject: "u_1", GameID: otherGameID, Name: "A", Type: jwt.TypePlayer}, time.Hour))

	id1, id2 := h1["playerId"].(string), h2["playerId"].(string)
	// Sending to the ID of game 1's player, but in game 2, must reach nobody...
	env.h.Hub().SendToPlayers(otherGameID, []string{id1}, []byte(`{"type":"notification","data":{"to":"leak"}}`))
	// ...and sending in game 1 reaches only game 1's player.
	env.h.Hub().SendToPlayers(testGameID, []string{id1, id2}, []byte(`{"type":"notification","data":{"to":"game1"}}`))

	if typ, data := readFrame(t, c1); typ != "notification" || data["to"] != "game1" {
		t.Fatalf("game 1's player got %q %v, want the game1 notification", typ, data)
	}
	// Game 2's player must see nothing of the above: the next frame is the pong.
	if typ, _ := exchange(t, c2, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("game 2's player got %q first, want pong (frames of game 1 must not reach game 2)", typ)
	}
}

func TestWSRefusesBadTokensBeforeUpgrade(t *testing.T) {
	env := newWSEnv(t, nil)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	forged, err := jwt.Sign(otherPriv, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		token    string
		wantCode string
	}{
		{"missing token", "", "missing_token"},
		{"not a token", "garbage", "invalid_token"},
		{"signed by another key", forged, "invalid_token"},
		{"expired", env.token(t, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, -time.Minute), "token_expired"},
		{"developer token", env.token(t, jwt.Claims{Subject: "dev_1", Type: jwt.TypeDeveloper}, time.Hour), "wrong_token_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, resp, err := env.dial(t, tt.token, "")
			if err == nil || c != nil {
				t.Fatal("the upgrade succeeded, want it refused")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response = %v, want 401", resp)
			}
			if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
			if code := errorCodeOf(t, resp); code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", code, tt.wantCode)
			}
		})
	}
	if n := env.resolveCalls.Load(); n != 0 {
		t.Fatalf("social was asked %d times about tokens that were already refused", n)
	}
}

func TestWSSocialUnavailable(t *testing.T) {
	env := newWSEnv(t, nil)
	env.socialDown.Store(true)
	tok := env.playerToken(t)

	c, resp, err := env.dial(t, tok, "")
	if err == nil || c != nil {
		t.Fatal("the upgrade succeeded although social is down")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("response = %v, want 503", resp)
	}
	if code := errorCodeOf(t, resp); code != "social_unavailable" {
		t.Fatalf("error code = %q, want social_unavailable", code)
	}
	if env.h.Hub().Len() != 0 {
		t.Fatal("a connection was registered although the upgrade was refused")
	}

	// Once social is back, the same token works.
	env.socialDown.Store(false)
	env.connect(t, tok)
}

func TestWSOrigin(t *testing.T) {
	env := newWSEnv(t, nil)
	tok := env.playerToken(t)

	t.Run("listed origin connects", func(t *testing.T) {
		c, _, err := env.dial(t, tok, allowedOrigin)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		readFrame(t, c) // hello
		if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
			t.Fatalf("got %q, want pong", typ)
		}
	})
	t.Run("no origin (not a browser) connects", func(t *testing.T) {
		if _, _, err := env.dial(t, tok, ""); err != nil {
			t.Fatalf("dial: %v", err)
		}
	})
	before := env.resolveCalls.Load()
	for _, origin := range []string{"https://evil.example", "http://localhost:9999", "https://localhost:5174"} {
		t.Run("refused "+origin, func(t *testing.T) {
			c, resp, err := env.dial(t, tok, origin)
			if err == nil || c != nil {
				t.Fatal("the upgrade succeeded, want it refused")
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("response = %v, want 403", resp)
			}
			if code := errorCodeOf(t, resp); code != "origin_not_allowed" {
				t.Fatalf("error code = %q, want origin_not_allowed", code)
			}
		})
	}
	if n := env.resolveCalls.Load(); n != before {
		t.Fatalf("social was asked %d times for requests from forbidden origins", n-before)
	}
}

func TestWSBadFramesKeepTheConnectionOpen(t *testing.T) {
	c := newWSEnv(t, nil).mustDial(t)

	tests := []struct {
		name        string
		frame       string
		binary      bool
		wantReplyTo any
	}{
		{"not JSON", `hello`, false, nil},
		{"JSON but not an object", `[1,2]`, false, nil},
		{"no type", `{"id":"x-1","data":{}}`, false, "x-1"},
		{"id is a number", `{"type":"ping","id":5}`, false, nil},
		{"id too long", `{"type":"ping","id":"` + strings.Repeat("a", 65) + `"}`, false, nil},
		{"data is not an object", `{"type":"ping","id":"x-2","data":"hi"}`, false, "x-2"},
		{"unknown type", `{"type":"nope","id":"x-3"}`, false, "x-3"},
		{"a server frame sent by the client", `{"type":"message.new","id":"x-4","data":{}}`, false, "x-4"},
		{"binary frame", `{"type":"ping"}`, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			kind := websocket.MessageText
			if tt.binary {
				kind = websocket.MessageBinary
			}
			if err := c.Write(ctx, kind, []byte(tt.frame)); err != nil {
				t.Fatalf("write: %v", err)
			}
			typ, data := readFrame(t, c)
			if typ != "error" || data["code"] != "bad_frame" {
				t.Fatalf("got %q %v, want error bad_frame", typ, data)
			}
			if data["replyTo"] != tt.wantReplyTo {
				t.Fatalf("replyTo = %v, want %v", data["replyTo"], tt.wantReplyTo)
			}
		})
	}
	// After all of that the connection still works.
	if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("got %q after bad frames, want pong", typ)
	}
}

func TestWSFrameSizeLimit(t *testing.T) {
	c := newWSEnv(t, nil).mustDial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Exactly at the limit is accepted (the frame is a bad_frame, but the connection stays).
	if err := c.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("a"), hub.DefaultReadLimit)); err != nil {
		t.Fatal(err)
	}
	if typ, _ := readFrame(t, c); typ != "error" {
		t.Fatalf("frame of %d bytes: got %q, want an error frame (not a close)", hub.DefaultReadLimit, typ)
	}

	// One byte more closes the connection with 1009.
	if err := c.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("a"), hub.DefaultReadLimit+1)); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %d (%v), want %d", got, err, websocket.StatusMessageTooBig)
	}
}

func TestWSTokenExpiryClosesWith4001(t *testing.T) {
	env := newWSEnv(t, nil)
	tok := env.token(t, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, 2*time.Second)
	c, _ := env.connect(t, tok)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != 4001 {
		t.Fatalf("close status = %d (%v), want 4001", got, err)
	}
	eventually(t, "the expired connection to leave the hub", func() bool { return env.h.Hub().Len() == 0 })
}

func TestWSShutdownClosesWith1001(t *testing.T) {
	env := newWSEnv(t, nil)
	c := env.mustDial(t)
	env.shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
		t.Fatalf("close status = %d (%v), want %d", got, err, websocket.StatusGoingAway)
	}
	env.h.Wait(5 * time.Second)

	// A new connection after shutdown began is refused.
	if _, resp, err := env.dial(t, env.playerToken(t), ""); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after shutdown: err=%v resp=%v, want 503", err, resp)
	}
}

func TestWSDropsAClientThatStopsAnsweringPings(t *testing.T) {
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) {
		o.PingInterval = 20 * time.Millisecond
		o.PongTimeout = 150 * time.Millisecond
	})
	// The client never reads after the handshake, so the library never answers the server's pings.
	c, _, err := env.dial(t, env.playerToken(t), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c

	eventually(t, "the silent client to be dropped", func() bool {
		return env.h.Hub().Len() == 0 && strings.Contains(env.logs.String(), "websocket disconnected")
	})
	if !strings.Contains(env.logs.String(), `reason="no pong"`) {
		t.Fatalf("the connection was not dropped for a missing pong:\n%s", env.logs.String())
	}
}

// A client that does not read must not hold up anyone else: it is dropped with 4002 and the
// frames for the healthy player still arrive.
func TestWSSlowClientIsDroppedWith4002(t *testing.T) {
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) {
		o.SendBuffer = 8
		o.WriteTimeout = 2 * time.Second
	})
	_, slowHello := env.connectWithoutReading(t, env.tokenFor(t, "u_slow"))
	healthy, healthyHello := env.connect(t, env.tokenFor(t, "u_ok"))
	slowID, healthyID := slowHello, healthyHello["playerId"].(string)

	// Big frames fill the socket buffers, then the writer blocks, then the queue fills.
	big := []byte(`{"type":"notification","data":{"pad":"` + strings.Repeat("x", 256<<10) + `"}}`)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(env.logs.String(), "slow client") {
		if time.Now().After(deadline) {
			t.Fatal("the slow client was not dropped")
		}
		start := time.Now()
		env.h.Hub().SendToPlayers(testGameID, []string{slowID}, big)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("SendToPlayers took %v: it waited for a slow client", d)
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(env.logs.String(), "code=4002") {
		t.Fatalf("the slow client was not closed with 4002:\n%s", env.logs.String())
	}

	// The healthy player was not affected.
	env.h.Hub().SendToPlayers(testGameID, []string{healthyID}, []byte(`{"type":"notification","data":{"to":"healthy"}}`))
	if typ, data := readFrame(t, healthy); typ != "notification" || data["to"] != "healthy" {
		t.Fatalf("healthy player got %q %v", typ, data)
	}
	if typ, _ := exchange(t, healthy, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("healthy player got %q, want pong", typ)
	}
	eventually(t, "the slow client to leave the hub", func() bool { return !env.h.Hub().Online(testGameID, slowID) })
}

// connectWithoutReading opens a socket, never reads from it, and returns the player's social ID.
func (e *wsEnv) connectWithoutReading(t *testing.T, token string) (*websocket.Conn, string) {
	t.Helper()
	c, resp, err := e.dial(t, token, "")
	if err != nil {
		t.Fatalf("dial: %v (response %v)", err, resp)
	}
	claims, err := jwt.Verify(e.pub, token, jwt.TypePlayer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id := socialIDFor(claims.GameID + "/" + claims.Subject)
	eventually(t, "the connection to be registered", func() bool { return e.h.Hub().Online(testGameID, id) })
	return c, id
}

func TestWSStaysOpenPastServerTimeouts(t *testing.T) {
	// The real server sets a read and a write timeout (httpx.Run uses 15 s). An upgraded
	// connection must not be cut by them.
	env := newWSEnv(t, func(_ *WSOptions, srv *httptest.Server) {
		srv.Config.ReadTimeout = 200 * time.Millisecond
		srv.Config.WriteTimeout = 200 * time.Millisecond
	})
	c := env.mustDial(t)
	time.Sleep(700 * time.Millisecond)
	if typ, _ := exchange(t, c, `{"type":"ping","id":"late"}`); typ != "pong" {
		t.Fatalf("got %q, want pong", typ)
	}
}

func TestWSTokenNeverReachesTheLogs(t *testing.T) {
	env := newWSEnv(t, nil)
	good := env.playerToken(t)
	expired := env.token(t, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, -time.Minute)

	c, _, err := env.dial(t, good, allowedOrigin)
	if err != nil {
		t.Fatal(err)
	}
	readFrame(t, c) // hello
	exchange(t, c, `{"type":"ping"}`)
	env.dial(t, expired, "")                  // 401
	env.dial(t, "garbage-token", "")          // 401
	env.dial(t, good, "https://evil.example") // 403
	env.socialDown.Store(true)
	env.dial(t, good, "") // 503: the social error is logged
	c.Close(websocket.StatusNormalClosure, "")
	env.shutdown()
	env.h.Wait(5 * time.Second)

	logs := env.logs.String()
	if !strings.Contains(logs, "path=/api/v1/ws") || !strings.Contains(logs, "cannot resolve the player in social") {
		t.Fatalf("expected request and social error log lines, got:\n%s", logs)
	}
	for _, secret := range []string{good, expired, "garbage-token", "token="} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the logs contain %q:\n%s", secret, logs)
		}
	}
}

// The origin is also checked by the WebSocket upgrade itself (scheme, host and port), so a
// change in the middleware around the handler cannot open the endpoint to other sites.
func TestWSOriginIsCheckedWithoutTheCORSMiddleware(t *testing.T) {
	env := newWSEnv(t, nil)
	mux := http.NewServeMux()
	env.h.Register(mux)
	srv := httptest.NewServer(mux) // no CORS, no other middleware
	t.Cleanup(srv.Close)

	tok := env.playerToken(t)
	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?token="+tok,
			&websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{origin}}})
	}

	c, _, err := dial(allowedOrigin)
	if err != nil {
		t.Fatalf("listed origin: %v", err)
	}
	c.CloseNow()
	for _, origin := range []string{"https://evil.example", "http://localhost:9999", "https://localhost:5174"} {
		c, resp, err := dial(origin)
		if err == nil || c != nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %s: err=%v resp=%v, want 403", origin, err, resp)
		}
	}
}

// fakePresence records what the endpoint tells the presence tracker, and answers OnlineFriends from a list.
type fakePresence struct {
	mu           sync.Mutex
	calls        []string // "connected game player" / "disconnected game player", in order
	friends      map[string][]string
	err          error
	hub          *hub.Hub
	connectedHub []bool // whether the hub already had the player when Connected ran
	firsts       []bool // the first flag of each Connected call
	inGrace      bool   // Connected answers false, like for a player back within the grace period
	announced    []announcement
}

// announcement is one AnnounceOnline call.
type announcement struct {
	gameID, playerID string
	friends          []string
}

func (f *fakePresence) watch(h *hub.Hub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hub = h
}

func (f *fakePresence) Connected(g, p string, first bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "connected "+g+" "+p)
	f.firsts = append(f.firsts, first)
	if f.hub != nil {
		f.connectedHub = append(f.connectedHub, f.hub.Online(g, p))
	}
	return first && !f.inGrace
}

func (f *fakePresence) AnnounceOnline(g, p string, friends []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announced = append(f.announced, announcement{g, p, append([]string(nil), friends...)})
}

func (f *fakePresence) announcements() []announcement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]announcement(nil), f.announced...)
}

func (f *fakePresence) firstFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.firsts...)
}

func (f *fakePresence) Disconnected(g, p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "disconnected "+g+" "+p)
}

func (f *fakePresence) OnlineFriends(_ context.Context, _, p string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.friends[p], f.err
}

func (f *fakePresence) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHelloListsTheOnlineFriendsFromPresence(t *testing.T) {
	pres := &fakePresence{friends: map[string][]string{socialIDFor(testGameID + "/alice"): {"5a2b3c4d-0000-4000-8000-000000000001"}}}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Presence = pres })
	_, hello := env.connect(t, env.tokenFor(t, "alice"))
	friends, _ := hello["onlineFriends"].([]any)
	if len(friends) != 1 || friends[0] != "5a2b3c4d-0000-4000-8000-000000000001" {
		t.Fatalf("onlineFriends = %#v, want the one friend presence named", hello["onlineFriends"])
	}
}

func TestHelloHasNoFriendsWhenPresenceCannotLookThemUpOrReturnsNil(t *testing.T) {
	for name, pres := range map[string]*fakePresence{
		"lookup fails": {err: errors.New("database down")},
		"nil list":     {},
	} {
		t.Run(name, func(t *testing.T) {
			env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Presence = pres })
			_, hello := env.connect(t, env.tokenFor(t, "alice"))
			friends, ok := hello["onlineFriends"].([]any)
			if !ok || len(friends) != 0 {
				t.Fatalf("onlineFriends = %#v, want an empty array, and the connection must still work", hello["onlineFriends"])
			}
		})
	}
}

func TestPresenceIsToldWhenTheFirstTabOpensAndTheLastTabCloses(t *testing.T) {
	pres := &fakePresence{}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Presence = pres })
	pres.watch(env.h.Hub())
	alice := socialIDFor(testGameID + "/alice")
	tok := env.tokenFor(t, "alice")

	tab1, _ := env.connect(t, tok)
	tab2, _ := env.connect(t, tok)
	waitFor(t, "two Connected calls", func() bool { return len(pres.log()) == 2 })

	_ = tab1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond) // a first tab closing while another is open is not a disconnect
	if got := pres.log(); len(got) != 2 {
		t.Fatalf("calls = %v: closing one of two tabs must not start the grace period", got)
	}

	_ = tab2.Close(websocket.StatusNormalClosure, "")
	want := []string{"connected " + testGameID + " " + alice, "connected " + testGameID + " " + alice, "disconnected " + testGameID + " " + alice}
	waitFor(t, "the Disconnected call", func() bool { return len(pres.log()) == 3 })
	if got := pres.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for _, online := range pres.connectedHub {
		if !online {
			t.Error("Connected ran before the connection was in the hub")
		}
	}
}

// TEAM-274: the first tab of a player who was offline tells their online friends; a second tab does not.
func TestTheFirstTabAnnouncesThePlayerOnlineToTheFriendsInHello(t *testing.T) {
	friend := "5a2b3c4d-0000-4000-8000-000000000001"
	alice := socialIDFor(testGameID + "/alice")
	pres := &fakePresence{friends: map[string][]string{alice: {friend}}}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Presence = pres })
	tok := env.tokenFor(t, "alice")

	env.connect(t, tok)
	env.connect(t, tok)
	waitFor(t, "two Connected calls", func() bool { return len(pres.firstFlags()) == 2 })

	if got := pres.firstFlags(); !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("first flags = %v, want [true false]: only the first tab is first in the hub", got)
	}
	want := []announcement{{testGameID, alice, []string{friend}}}
	if got := pres.announcements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("announced %+v, want %+v: one, to the friends hello listed", got, want)
	}
}

func TestComingBackWithinTheGracePeriodAnnouncesNothing(t *testing.T) {
	alice := socialIDFor(testGameID + "/alice")
	pres := &fakePresence{inGrace: true, friends: map[string][]string{alice: {"5a2b3c4d-0000-4000-8000-000000000001"}}}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Presence = pres })

	env.connect(t, env.tokenFor(t, "alice"))
	waitFor(t, "the Connected call", func() bool { return len(pres.firstFlags()) == 1 })
	if got := pres.announcements(); len(got) != 0 {
		t.Fatalf("announced %+v for a player back within the grace period, want nothing", got)
	}
}

func TestAConnectionWithoutPresenceStillWorks(t *testing.T) {
	env := newWSEnv(t, nil)
	c, _ := env.connect(t, env.tokenFor(t, "alice"))
	if err := c.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Log(err) // the close handshake may race the server; the point is that nothing panicked
	}
}

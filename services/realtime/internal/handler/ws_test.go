package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
)

const (
	testGameID    = "a6c1e2f0-1111-4222-8333-944455556666"
	allowedOrigin = "http://localhost:5174"
)

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

// wsEnv is a running realtime handler behind the same middleware chain as production.
type wsEnv struct {
	srv      *httptest.Server
	h        *Handler
	priv     ed25519.PrivateKey
	logs     *syncBuffer
	shutdown context.CancelFunc
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
	opts := WSOptions{
		Verifier: auth.NewVerifier(pub, time.Now),
		Origins:  []string{allowedOrigin},
		Shutdown: shutdownCtx,
	}
	h := New("test", fakeService{}, opts, log)
	mux := http.NewServeMux()
	h.Register(mux)
	cors, err := httpx.CORS([]string{allowedOrigin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors))
	if tweak != nil {
		tweak(&h.wsOpts, srv)
	}
	srv.Start()
	t.Cleanup(func() {
		shutdown()
		srv.Close()
	})
	return &wsEnv{srv: srv, h: h, priv: priv, logs: logs, shutdown: shutdown}
}

func (e *wsEnv) token(t *testing.T, c jwt.Claims, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.Sign(e.priv, c, ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *wsEnv) playerToken(t *testing.T) string {
	t.Helper()
	return e.token(t, jwt.Claims{Subject: "u_42", GameID: testGameID, Name: "Ali", Type: jwt.TypePlayer}, time.Hour)
}

// dial opens /api/v1/ws with the token and optional Origin. On a refused upgrade it returns the HTTP response.
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

func (e *wsEnv) mustDial(t *testing.T) *websocket.Conn {
	t.Helper()
	c, _, err := e.dial(t, e.playerToken(t), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
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
}

func TestWSOrigin(t *testing.T) {
	env := newWSEnv(t, nil)
	tok := env.playerToken(t)

	t.Run("listed origin connects", func(t *testing.T) {
		c, _, err := env.dial(t, tok, allowedOrigin)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
			t.Fatalf("got %q, want pong", typ)
		}
	})
	t.Run("no origin (not a browser) connects", func(t *testing.T) {
		if _, _, err := env.dial(t, tok, ""); err != nil {
			t.Fatalf("dial: %v", err)
		}
	})
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
		{"type not built yet", `{"type":"message.send","id":"x-4","data":{"conversationId":"c","body":"hi"}}`, false, "x-4"},
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
	if err := c.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("a"), maxFrameBytes)); err != nil {
		t.Fatal(err)
	}
	if typ, _ := readFrame(t, c); typ != "error" {
		t.Fatalf("frame of %d bytes: got %q, want an error frame (not a close)", maxFrameBytes, typ)
	}

	// One byte more closes the connection with 1009.
	if err := c.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("a"), maxFrameBytes+1)); err != nil {
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
	c, _, err := env.dial(t, tok, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != 4001 {
		t.Fatalf("close status = %d (%v), want 4001", got, err)
	}
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
	// The client never reads, so the library never answers the server's pings.
	env.mustDial(t)

	done := make(chan struct{})
	go func() {
		env.h.Wait(5 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the server still holds the connection of a client that does not answer pings")
	}
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
	exchange(t, c, `{"type":"ping"}`)
	env.dial(t, expired, "")                  // 401
	env.dial(t, "garbage-token", "")          // 401
	env.dial(t, good, "https://evil.example") // 403
	c.Close(websocket.StatusNormalClosure, "")
	env.shutdown()
	env.h.Wait(5 * time.Second)

	logs := env.logs.String()
	if !strings.Contains(logs, "path=/api/v1/ws") {
		t.Fatalf("expected request log lines, got:\n%s", logs)
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
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := New("test", fakeService{}, WSOptions{
		Verifier: auth.NewVerifier(pub, time.Now),
		Origins:  []string{allowedOrigin},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux) // no CORS, no other middleware
	t.Cleanup(srv.Close)

	tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
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

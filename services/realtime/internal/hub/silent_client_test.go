package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// serveSilentClient runs one Conn on a server whose client never reads, so it never answers a ping or
// a close frame. It returns how long Serve took after the first ping was due, and how the Conn closed.
func serveSilentClient(t *testing.T, opts ConnOptions, closeFromTest func(*Conn)) (time.Duration, websocket.StatusCode, string) {
	t.Helper()
	type result struct {
		took   time.Duration
		code   websocket.StatusCode
		reason string
	}
	got := make(chan result, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c := NewConn(ws, Identity{GameID: "g", PlayerID: "p"}, opts)
		if closeFromTest != nil {
			go closeFromTest(c)
		}
		start := time.Now()
		c.Serve(r.Context(), func(*Conn, websocket.MessageType, []byte) {})
		code, reason := c.CloseInfo()
		got <- result{time.Since(start), code, reason}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow() // the client is never read: nothing answers pings or close frames

	select {
	case r := <-got:
		return r.took, r.code, r.reason
	case <-ctx.Done():
		t.Fatal("Serve did not return")
		return 0, 0, ""
	}
}

// A client that stops answering pings is dropped right after the pong timeout. The writer must not try
// the polite close handshake with it: it waits up to 5 s for a reply that never comes, and the keepalive's
// CloseNow would then only wait behind it.
func TestSilentClientIsDroppedRightAfterThePongTimeout(t *testing.T) {
	for i := range 20 { // the old bug needed the writer to win a race, so one run proved little
		took, code, reason := serveSilentClient(t, ConnOptions{
			PingInterval: 5 * time.Millisecond,
			PongTimeout:  50 * time.Millisecond,
		}, nil)
		if took > 2*time.Second {
			t.Fatalf("run %d: Serve took %v, want well under the 5 s close handshake", i, took)
		}
		if code != websocket.StatusGoingAway || reason != "no pong" {
			t.Fatalf("run %d: closed with %d %q, want 1001 \"no pong\"", i, code, reason)
		}
	}
}

func TestCloseDeadWinsOnlyOnceAndKeepsItsReason(t *testing.T) {
	c := detached("g", "p", 4)
	if !c.closeDead(websocket.StatusGoingAway, "no pong") {
		t.Fatal("the first closeDead must win")
	}
	if c.closeDead(CloseSlowClient, "slow client") {
		t.Error("a second closeDead reported that it won")
	}
	c.Close(ClosePolicy, "later") // does not change it either
	if code, reason := c.CloseInfo(); code != websocket.StatusGoingAway || reason != "no pong" {
		t.Errorf("close = %d %q, want 1001 no pong", code, reason)
	}

	other := detached("g", "p", 4)
	other.Close(websocket.StatusNormalClosure, "")
	if other.closeDead(websocket.StatusGoingAway, "no pong") {
		t.Error("closeDead won after a normal Close")
	}
	if other.dead {
		t.Error("a lost closeDead must not mark the connection dead: its writer should still close politely")
	}
}

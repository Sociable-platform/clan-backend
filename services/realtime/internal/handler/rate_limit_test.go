package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// readFrameOrClose reads the next frame, or returns the close code when the server closed the connection.
func readFrameOrClose(t *testing.T, c *websocket.Conn) (typ string, data map[string]any, closeCode websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		code := websocket.CloseStatus(err)
		if code == -1 {
			t.Fatalf("read: %v", err)
		}
		return "", nil, code
	}
	var f struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Type, f.Data, 0
}

func rateLimitedEnv(t *testing.T, rl RateLimit) (*wsEnv, *websocket.Conn) {
	t.Helper()
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.RateLimit = rl })
	c, hello := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.storing(hello["playerId"].(string))
	return env, c
}

// sendAndWait sends one message.send and reads its answer (the ack and message.new, or an error frame).
func sendAndWait(t *testing.T, c *websocket.Conn, id string) (string, map[string]any, websocket.StatusCode) {
	t.Helper()
	write(t, c, sendFrame(id, convID, "hi"))
	typ, data, code := readFrameOrClose(t, c)
	if typ == "message.ack" {
		readFrame(t, c) // message.new, the sender is a member
	}
	return typ, data, code
}

func TestRateLimitFiveInFiveSecondsThenRateLimited(t *testing.T) {
	env, c := rateLimitedEnv(t, RateLimit{})
	for i := 1; i <= 5; i++ {
		if typ, _, _ := sendAndWait(t, c, "ok-"+string(rune('0'+i))); typ != "message.ack" {
			t.Fatalf("message %d: %s, want message.ack", i, typ)
		}
	}
	typ, data, _ := sendAndWait(t, c, "c-6")
	if typ != "error" || data["code"] != "rate_limited" || data["replyTo"] != "c-6" || data["message"] == "" {
		t.Fatalf("sixth message: %s %v, want error rate_limited with replyTo c-6", typ, data)
	}
	if _, has := data["retryAfterMs"]; has {
		t.Error("rate_limited must not carry retryAfterMs (only slow_mode does)")
	}
	if n := len(env.chat.callList()); n != 5 {
		t.Errorf("the service was called %d times, want 5: the refused message must not reach it", n)
	}
	// One refusal is not a reason to close: the connection still works.
	if typ, data := exchange(t, c, `{"type":"ping","id":"p"}`); typ != "pong" {
		t.Errorf("after one refusal: %s %v, want pong", typ, data)
	}
}

func TestRateLimitThreeRefusedInARowCloseWith1008AfterTheErrorFrame(t *testing.T) {
	env, c := rateLimitedEnv(t, RateLimit{})
	for i := 1; i <= 5; i++ {
		sendAndWait(t, c, "ok")
	}
	for i := 1; i <= 3; i++ {
		write(t, c, sendFrame("bad-"+string(rune('0'+i)), convID, "hi"))
	}
	for i := 1; i <= 3; i++ { // all three refusals are answered, the last one too
		typ, data, code := readFrameOrClose(t, c)
		want := "bad-" + string(rune('0'+i))
		if code != 0 || typ != "error" || data["code"] != "rate_limited" || data["replyTo"] != want {
			t.Fatalf("answer %d: type %q code %v data %v close %d, want rate_limited for %s", i, typ, data["code"], data, code, want)
		}
	}
	if _, _, code := readFrameOrClose(t, c); code != 1008 {
		t.Fatalf("close code = %d, want 1008", code)
	}
	if n := len(env.chat.callList()); n != 5 {
		t.Errorf("service calls = %d, want 5", n)
	}
}

func TestRateLimitAnAcceptedMessageBreaksTheStreak(t *testing.T) {
	// 2 per 300 ms; 2 strikes would close.
	_, c := rateLimitedEnv(t, RateLimit{Max: 2, Per: 300 * time.Millisecond, Strikes: 3})
	for round := 0; round < 3; round++ {
		for i := 0; i < 2; i++ {
			if typ, _, _ := sendAndWait(t, c, "ok"); typ != "message.ack" {
				t.Fatalf("round %d: message refused too early", round)
			}
		}
		for i := 0; i < 2; i++ { // two refusals, one short of closing
			if typ, data, code := sendAndWait(t, c, "x"); typ != "error" || data["code"] != "rate_limited" || code != 0 {
				t.Fatalf("round %d: %s %v %d, want rate_limited", round, typ, data, code)
			}
		}
		time.Sleep(350 * time.Millisecond) // the window passes; the next accepted message resets the count
	}
	if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("the connection was closed: %s", typ)
	}
}

func TestRateLimitIsPerConnection(t *testing.T) {
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.RateLimit = RateLimit{Max: 2, Per: time.Minute} })
	tab1, hello := env.connect(t, env.tokenFor(t, "alice"))
	tab2, _ := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.storing(hello["playerId"].(string))
	for i := 0; i < 2; i++ {
		sendAndWait(t, tab1, "a")
	}
	if typ, data, _ := sendAndWait(t, tab1, "a"); typ != "error" || data["code"] != "rate_limited" {
		t.Fatalf("tab 1: %s %v, want rate_limited", typ, data)
	}
	// Tab 2 first hears about tab 1's two messages (it is a member too), then its own answer.
	write(t, tab2, sendFrame("b", convID, "hi"))
	for {
		typ, data := readFrame(t, tab2)
		if typ == "message.new" {
			continue
		}
		if typ != "message.ack" || data["replyTo"] != "b" {
			t.Fatalf("tab 2 of the same player was limited by tab 1: %s %v", typ, data)
		}
		break
	}
}

func TestRateLimitOnlyCountsMessageSendFrames(t *testing.T) {
	_, c := rateLimitedEnv(t, RateLimit{Max: 2, Per: time.Minute})
	for i := 0; i < 20; i++ {
		if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
			t.Fatalf("ping %d: %s", i, typ)
		}
	}
	if typ, _, _ := sendAndWait(t, c, "a"); typ != "message.ack" {
		t.Errorf("pings used up the message allowance: %s", typ)
	}
}

func TestRateLimitCountsMalformedMessageSendsToo(t *testing.T) {
	_, c := rateLimitedEnv(t, RateLimit{Max: 2, Per: time.Minute})
	for i := 0; i < 2; i++ {
		if typ, data := exchange(t, c, `{"type":"message.send","id":"m","data":{}}`); typ != "error" || data["code"] != "bad_frame" {
			t.Fatalf("malformed %d: %s %v, want bad_frame", i, typ, data)
		}
	}
	if typ, data := exchange(t, c, `{"type":"message.send","id":"m","data":{}}`); typ != "error" || data["code"] != "rate_limited" {
		t.Errorf("third malformed: %s %v, want rate_limited (garbage cannot be sent for free)", typ, data)
	}
}

func TestRateLimitWindowMovesOn(t *testing.T) {
	_, c := rateLimitedEnv(t, RateLimit{Max: 1, Per: 300 * time.Millisecond})
	sendAndWait(t, c, "a")
	if typ, _, _ := sendAndWait(t, c, "b"); typ != "error" {
		t.Fatalf("second message: %s, want refused", typ)
	}
	time.Sleep(350 * time.Millisecond)
	if typ, _, _ := sendAndWait(t, c, "c"); typ != "message.ack" {
		t.Errorf("after the window: %s, want message.ack", typ)
	}
}

func TestEveryRuleErrorBecomesAnErrorFrameWithReplyTo(t *testing.T) {
	tests := []struct {
		name       string
		err        *service.SendError
		wantRetry  float64
		wantNoText bool
	}{
		{"feature_disabled", &service.SendError{Code: "feature_disabled", Message: "Direct messages are turned off in this game"}, 0, false},
		{"blocked", &service.SendError{Code: "blocked", Message: "You can't message this player"}, 0, false},
		{"too_long", &service.SendError{Code: "too_long", Message: "The message is longer than 20 characters"}, 0, false},
		{"slow_mode", &service.SendError{Code: "slow_mode", Message: "Wait a moment before sending again", RetryAfter: 1400 * time.Millisecond}, 1400, false},
		{"slow_mode rounds up", &service.SendError{Code: "slow_mode", Message: "Wait", RetryAfter: 1400*time.Millisecond + time.Microsecond}, 1401, false},
		{"slow_mode under a millisecond is 1", &service.SendError{Code: "slow_mode", Message: "Wait", RetryAfter: time.Microsecond}, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newWSEnv(t, nil)
			c, _ := env.connect(t, env.tokenFor(t, "alice"))
			env.chat.send = func(service.Sender, string, string) (service.Sent, error) { return service.Sent{}, tt.err }
			write(t, c, sendFrame("c-9", convID, "hi"))
			typ, data := readFrame(t, c)
			if typ != "error" || data["code"] != tt.err.Code || data["message"] != tt.err.Message || data["replyTo"] != "c-9" {
				t.Fatalf("frame = %s %v, want an error frame with code %s, its message and replyTo c-9", typ, data, tt.err.Code)
			}
			got, has := data["retryAfterMs"]
			if tt.wantRetry == 0 && has {
				t.Errorf("retryAfterMs = %v on a %s error, want none", got, tt.err.Code)
			}
			if tt.wantRetry != 0 && got != tt.wantRetry {
				t.Errorf("retryAfterMs = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}

// With the default limit (5 in 5 s, 3 refusals), time moved by hand: the window is exactly 5 seconds.
func TestRateLimitDefaultsAreFiveInFiveSecondsAndThreeStrikes(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	move := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	_, c := rateLimitedEnv(t, RateLimit{Now: clock})

	for i := 1; i <= 5; i++ {
		if typ, _, _ := sendAndWait(t, c, "a"); typ != "message.ack" {
			t.Fatalf("message %d refused", i)
		}
	}
	move(4999 * time.Millisecond)
	if typ, data, _ := sendAndWait(t, c, "b"); typ != "error" || data["code"] != "rate_limited" {
		t.Fatalf("at 4.999 s: %s %v, want rate_limited (the window is 5 s)", typ, data)
	}
	move(time.Millisecond) // exactly 5 s after the first five
	if typ, _, _ := sendAndWait(t, c, "c"); typ != "message.ack" {
		t.Fatalf("at 5 s: %s, want the window to have moved on", typ)
	}
	// Four more are free (the window now holds the one at 5 s), the next two are refused, one short of 3 strikes.
	for i := 0; i < 4; i++ {
		sendAndWait(t, c, "d")
	}
	for i := 0; i < 2; i++ {
		if typ, data, code := sendAndWait(t, c, "e"); typ != "error" || data["code"] != "rate_limited" || code != 0 {
			t.Fatalf("refusal %d: %s %v %d", i+1, typ, data, code)
		}
	}
	if typ, _ := exchange(t, c, `{"type":"ping"}`); typ != "pong" {
		t.Fatalf("closed after only 2 refusals: %s", typ)
	}
	if typ, data, code := sendAndWait(t, c, "f"); typ != "error" || data["code"] != "rate_limited" {
		t.Fatalf("third refusal: %s %v %d", typ, data, code)
	}
	if _, _, code := readFrameOrClose(t, c); code != 1008 {
		t.Errorf("close code = %d, want 1008 after the third refusal in a row", code)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// serveFrames starts a WebSocket server that writes the given frames to the first client, then
// either keeps the connection open (hold) or closes it.
func serveFrames(t *testing.T, frames []string, hold bool) *wsClient {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for _, f := range frames {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(f)); err != nil {
				return
			}
		}
		if hold {
			select {
			case <-done:
			case <-r.Context().Done():
			}
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(func() {
		close(done)
		srv.Close()
	})

	c, err := dialWS(context.Background(), "test", srv.URL, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	return c
}

func TestExpectFindsFramesOutOfOrderAndKeepsTheSkippedOnes(t *testing.T) {
	c := serveFrames(t, []string{
		`{"type":"presence.update","data":{}}`,
		`{"type":"notification","data":{"kind":"friend_accepted"}}`,
		`{"type":"message.new","data":{"n":1}}`,
	}, true)
	deadline := time.Now().Add(2 * time.Second)

	tests := []struct {
		name  string
		match func(frame) bool
		want  string
	}{
		{"a later frame first", ofType("message.new"), "message.new"},
		{"a skipped frame afterwards", ofType("presence.update"), "presence.update"},
		{"a notification by kind", ofNotification("friend_accepted"), "notification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := c.expect(deadline, tt.name, tt.match)
			if err != nil {
				t.Fatal(err)
			}
			if f.Type != tt.want {
				t.Errorf("type = %q, want %q", f.Type, tt.want)
			}
		})
	}
}

func TestExpectDoesNotMatchANotificationOfAnotherKind(t *testing.T) {
	c := serveFrames(t, []string{`{"type":"notification","data":{"kind":"clan_joined"}}`}, true)
	_, err := c.expect(time.Now().Add(100*time.Millisecond), "friend_request", ofNotification("friend_request"))
	if err == nil || !strings.Contains(err.Error(), "no friend_request arrived in time") {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestExpectTimesOutWhenNothingArrives(t *testing.T) {
	c := serveFrames(t, nil, true)
	start := time.Now()
	_, err := c.expect(start.Add(100*time.Millisecond), "hello", ofType("hello"))
	if err == nil || !strings.Contains(err.Error(), "no hello arrived in time") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v, want about 100ms", elapsed)
	}
}

func TestExpectReportsAClosedConnection(t *testing.T) {
	c := serveFrames(t, nil, false)
	_, err := c.expect(time.Now().Add(2*time.Second), "hello", ofType("hello"))
	if err == nil || !strings.Contains(err.Error(), "connection ended while waiting for hello") {
		t.Errorf("err = %v, want a closed-connection error", err)
	}
}

func TestAbsentPassesWhenNoMatchingFrameArrives(t *testing.T) {
	c := serveFrames(t, []string{`{"type":"message.new","data":{"message":{"id":"m-1"}}}`}, true)
	if err := c.absent(time.Now().Add(150*time.Millisecond), "m-2", ofMessage("m-2")); err != nil {
		t.Fatal(err)
	}
	// The frame it skipped is still there for expect.
	if _, err := c.expect(time.Now().Add(time.Second), "m-1", ofMessage("m-1")); err != nil {
		t.Errorf("the skipped frame is gone: %v", err)
	}
}

func TestAbsentFailsWhenAMatchingFrameArrivesOrWaits(t *testing.T) {
	c := serveFrames(t, []string{
		`{"type":"presence.update","data":{}}`,
		`{"type":"message.new","data":{"message":{"id":"m-1"}}}`,
	}, true)
	if err := c.absent(time.Now().Add(time.Second), "m-1", ofMessage("m-1")); err == nil || !strings.Contains(err.Error(), "got m-1, want none") {
		t.Errorf("arriving frame: err = %v, want it reported", err)
	}

	// A matching frame that an earlier expect skipped counts too.
	c = serveFrames(t, []string{`{"type":"message.new","data":{"message":{"id":"m-1"}}}`, `{"type":"hello","data":{}}`}, true)
	if _, err := c.expect(time.Now().Add(time.Second), "hello", ofType("hello")); err != nil {
		t.Fatal(err)
	}
	if err := c.absent(time.Now().Add(100*time.Millisecond), "m-1", ofMessage("m-1")); err == nil {
		t.Error("waiting frame: err = nil, want it reported")
	}
}

func TestClanMatchers(t *testing.T) {
	const clan, other = "k-1", "k-2"
	tests := []struct {
		name  string
		match func(frame) bool
		frame string
		want  bool
	}{
		{"the clan's room", ofClanRoom(clan), `{"type":"conversation.added","data":{"conversation":{"id":"c-1","kind":"clan","clanId":"k-1"}}}`, true},
		{"another clan's room", ofClanRoom(clan), `{"type":"conversation.added","data":{"conversation":{"id":"c-1","kind":"clan","clanId":"k-2"}}}`, false},
		{"a DM is no clan room", ofClanRoom(clan), `{"type":"conversation.added","data":{"conversation":{"id":"c-1","kind":"dm","clanId":null}}}`, false},
		{"the removed conversation", ofConversationRemoved("c-1"), `{"type":"conversation.removed","data":{"conversationId":"c-1"}}`, true},
		{"another removed conversation", ofConversationRemoved("c-1"), `{"type":"conversation.removed","data":{"conversationId":"c-2"}}`, false},
		{"the clan's notification", ofClanNotification("clan_kicked", clan), `{"type":"notification","data":{"kind":"clan_kicked","payload":{"clanId":"k-1"}}}`, true},
		{"another kind", ofClanNotification("clan_kicked", clan), `{"type":"notification","data":{"kind":"clan_disbanded","payload":{"clanId":"k-1"}}}`, false},
		{"another clan", ofClanNotification("clan_kicked", other), `{"type":"notification","data":{"kind":"clan_kicked","payload":{"clanId":"k-1"}}}`, false},
		{"the message", ofMessage("m-1"), `{"type":"message.new","data":{"message":{"id":"m-1"}}}`, true},
		{"another message", ofMessage("m-1"), `{"type":"message.new","data":{"message":{"id":"m-2"}}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f frame
			if err := json.Unmarshal([]byte(tt.frame), &f); err != nil {
				t.Fatal(err)
			}
			if got := tt.match(f); got != tt.want {
				t.Errorf("match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDialErrorDoesNotLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more: the dial fails

	_, err := dialWS(context.Background(), "test", url, "secret-token")
	if err == nil {
		t.Fatal("dial succeeded, want an error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error %q contains the token", err)
	}
}

func TestCallChecksTheStatusAndDecodesTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"email_taken","message":"x"}}`))
			return
		}
		if r.Header.Get("X-Test") != "yes" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()
	c := &apiClient{http: srv.Client()}

	var out struct {
		ID string `json:"id"`
	}
	if err := c.call(context.Background(), http.MethodPost, srv.URL+"/ok", http.Header{"X-Test": {"yes"}}, map[string]string{"a": "b"}, &out, http.StatusCreated); err != nil {
		t.Fatal(err)
	}
	if out.ID != "abc" {
		t.Errorf("id = %q, want abc", out.ID)
	}

	err := c.call(context.Background(), http.MethodGet, srv.URL+"/bad", nil, nil, nil, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), `status 409 (code "email_taken")`) {
		t.Errorf("err = %v, want the status and the error code", err)
	}
}

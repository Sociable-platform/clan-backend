package main

import (
	"context"
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

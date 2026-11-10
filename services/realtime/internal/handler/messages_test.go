package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

const convID = "0193a6b4-7c1e-7d2a-9f10-2b3c4d5e6f70"

// chatService is the service behind message.send in the WebSocket tests. It records what the
// handler passed and answers with whatever send returns.
type chatService struct {
	fakeService
	readFake // the REST reads and conversation.read (rest_test.go)

	mu    sync.Mutex
	calls []sendCall
	send  func(from service.Sender, conversationID, body string) (service.Sent, error)
}

type sendCall struct {
	from           service.Sender
	conversationID string
	body           string
}

func (c *chatService) SendMessage(_ context.Context, from service.Sender, conversationID, body string) (service.Sent, error) {
	c.mu.Lock()
	c.calls = append(c.calls, sendCall{from, conversationID, body})
	send := c.send
	c.mu.Unlock()
	if send == nil {
		return service.Sent{}, errors.New("no fake configured")
	}
	return send(from, conversationID, body)
}

func (c *chatService) callList() []sendCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sendCall(nil), c.calls...)
}

// storing makes the fake store every message and deliver it to the given players.
func (c *chatService) storing(members ...string) {
	c.send = func(from service.Sender, conversationID, body string) (service.Sent, error) {
		return service.Sent{
			Message: service.Message{ID: "0193a6c0-1111-7222-8333-944455556666", SenderID: from.ID, SenderName: from.Name,
				Body: strings.TrimSpace(body), CreatedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)},
			Members: members,
		}, nil
	}
}

func sendFrame(id, conv, body string) string {
	b, _ := json.Marshal(map[string]any{"type": "message.send", "id": id, "data": map[string]any{"conversationId": conv, "body": body}})
	return string(b)
}

func write(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// noFrameWithin fails the test when the connection gets any frame within d.
func noFrameWithin(t *testing.T, c *websocket.Conn, d time.Duration, who string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if _, b, err := c.Read(ctx); err == nil {
		t.Fatalf("%s got a frame it should not get: %s", who, b)
	}
}

func TestWSMessageSendAcksAndDeliversToEveryTab(t *testing.T) {
	env := newWSEnv(t, nil)
	aliceTab1, aliceHello := env.connect(t, env.tokenFor(t, "alice"))
	aliceTab2, _ := env.connect(t, env.tokenFor(t, "alice"))
	bob, bobHello := env.connect(t, env.tokenFor(t, "bob"))
	carol, _ := env.connect(t, env.tokenFor(t, "carol")) // online, but not in the conversation
	aliceID, bobID := aliceHello["playerId"].(string), bobHello["playerId"].(string)
	env.chat.storing(aliceID, bobID)

	write(t, aliceTab1, sendFrame("c-1", convID, "  gg  "))

	// The sending tab: the ack first, then the message itself.
	typ, ack := readFrame(t, aliceTab1)
	if typ != "message.ack" || ack["replyTo"] != "c-1" || ack["messageId"] != "0193a6c0-1111-7222-8333-944455556666" || ack["createdAt"] != "2026-11-10T08:00:00Z" {
		t.Fatalf("first frame = %s %v, want message.ack echoing c-1", typ, ack)
	}
	// Every tab of every member gets message.new: the sender's other tab, the sender's own tab, the friend.
	for name, c := range map[string]*websocket.Conn{"alice tab 1": aliceTab1, "alice tab 2": aliceTab2, "bob": bob} {
		typ, data := readFrame(t, c)
		msg, _ := data["message"].(map[string]any)
		if typ != "message.new" || data["conversationId"] != convID || msg["body"] != "gg" ||
			msg["senderId"] != aliceID || msg["senderName"] != "Name of alice" || msg["id"] != "0193a6c0-1111-7222-8333-944455556666" ||
			msg["createdAt"] != "2026-11-10T08:00:00Z" {
			t.Errorf("%s got %s %v, want message.new with the stored message", name, typ, data)
		}
		if _, hasReply := data["replyTo"]; hasReply {
			t.Errorf("%s: message.new must not carry replyTo", name)
		}
	}
	noFrameWithin(t, carol, 200*time.Millisecond, "a player who is not a member")
	noFrameWithin(t, bob, 100*time.Millisecond, "bob (a second frame)")
}

func TestWSMessageSendTakesGameAndSenderFromTheConnection(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, hello := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.storing(hello["playerId"].(string))

	// The frame tries to name another game and another sender; both are ignored.
	write(t, alice, `{"type":"message.send","id":"c-1","data":{"conversationId":"`+convID+`","body":"hi","gameId":"`+otherGameID+`","senderId":"evil","senderName":"Evil"}}`)
	readFrame(t, alice) // ack

	calls := env.chat.callList()
	if len(calls) != 1 {
		t.Fatalf("service calls = %d, want 1", len(calls))
	}
	if got := calls[0].from; got.GameID != testGameID || got.ID != hello["playerId"] || got.Name != "Name of alice" {
		t.Errorf("sender = %+v, want the game, ID and name of the connection", got)
	}
}

func TestWSMessageSendErrorsGoToTheSenderOnly(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, _ := env.connect(t, env.tokenFor(t, "alice"))
	bob, _ := env.connect(t, env.tokenFor(t, "bob"))
	aliceTab2, _ := env.connect(t, env.tokenFor(t, "alice"))

	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"not a member", &service.SendError{Code: "not_member", Message: "You are not a member of this conversation"}, "not_member"},
		{"empty", &service.SendError{Code: "empty", Message: "The message is empty"}, "empty"},
		{"too long", &service.SendError{Code: "too_long", Message: "too long"}, "too_long"},
		{"wrapped player error", errors.Join(errors.New("x"), &service.SendError{Code: "empty", Message: "The message is empty"}), "empty"},
		{"our failure", errors.New("pq: password authentication failed for user secret_user"), "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.chat.send = func(service.Sender, string, string) (service.Sent, error) { return service.Sent{}, tt.err }
			typ, data := exchange(t, alice, sendFrame("c-9", convID, "hello"))
			if typ != "error" || data["code"] != tt.wantErr || data["replyTo"] != "c-9" {
				t.Fatalf("got %s %v, want error %s replying to c-9", typ, data, tt.wantErr)
			}
			if msg, _ := data["message"].(string); msg == "" || strings.Contains(msg, "secret_user") || strings.Contains(msg, "pq:") {
				t.Errorf("message = %q: empty, or it leaks an internal detail", msg)
			}
		})
	}
	noFrameWithin(t, bob, 200*time.Millisecond, "bob after rejected messages")
	noFrameWithin(t, aliceTab2, 100*time.Millisecond, "alice's other tab after rejected messages")
}

func TestWSMessageSendInternalFailureIsLoggedWithoutTheMessage(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, _ := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.send = func(service.Sender, string, string) (service.Sent, error) {
		return service.Sent{}, errors.New("insert message: connection refused")
	}
	typ, data := exchange(t, alice, sendFrame("c-1", convID, "my secret chat text"))
	if typ != "error" || data["code"] != "internal" {
		t.Fatalf("got %s %v, want error internal", typ, data)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, "connection refused") {
		t.Errorf("the failure was not logged:\n%s", logs)
	}
	if strings.Contains(logs, "my secret chat text") {
		t.Errorf("the log contains the message text:\n%s", logs)
	}
}

func TestWSMessageSendBadFrames(t *testing.T) {
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.RateLimit = RateLimit{Max: 1000} }) // many bad frames in a row
	alice, _ := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.storing()

	tests := []struct{ name, frame string }{
		{"no data", `{"type":"message.send","id":"c-1"}`},
		{"empty data", `{"type":"message.send","id":"c-1","data":{}}`},
		{"no body", `{"type":"message.send","id":"c-1","data":{"conversationId":"` + convID + `"}}`},
		{"no conversation", `{"type":"message.send","id":"c-1","data":{"body":"hi"}}`},
		{"body is a number", `{"type":"message.send","id":"c-1","data":{"conversationId":"` + convID + `","body":5}}`},
		{"body is null", `{"type":"message.send","id":"c-1","data":{"conversationId":"` + convID + `","body":null}}`},
		{"conversation is a number", `{"type":"message.send","id":"c-1","data":{"conversationId":7,"body":"hi"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, data := exchange(t, alice, tt.frame)
			if typ != "error" || data["code"] != "bad_frame" || data["replyTo"] != "c-1" {
				t.Fatalf("got %s %v, want error bad_frame replying to c-1", typ, data)
			}
		})
	}
	if n := len(env.chat.callList()); n != 0 {
		t.Errorf("the service was called %d times for frames that are not valid", n)
	}
	// The connection is still fine.
	if typ, _ := exchange(t, alice, `{"type":"ping","id":"p"}`); typ != "pong" {
		t.Errorf("after bad frames the next reply is %q, want pong", typ)
	}
}

func TestWSMessageSendWithoutAnIDAcksWithNullReplyTo(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, hello := env.connect(t, env.tokenFor(t, "alice"))
	env.chat.storing(hello["playerId"].(string))
	typ, data := exchange(t, alice, `{"type":"message.send","data":{"conversationId":"`+convID+`","body":"hi"}}`)
	if _, has := data["replyTo"]; typ != "message.ack" || !has || data["replyTo"] != nil {
		t.Errorf("got %s %v, want message.ack with replyTo null", typ, data)
	}
}

// Players of another game never get a message, even with the same conversation ID and the same
// social player ID in the member list.
func TestWSMessageNewStaysInTheGame(t *testing.T) {
	env := newWSEnv(t, nil)
	alice, hello := env.connect(t, env.tokenFor(t, "alice"))
	otherTok := env.token(t, jwt.Claims{Subject: "alice", GameID: otherGameID, Name: "A", Type: jwt.TypePlayer}, time.Hour)
	other, otherHello := env.connect(t, otherTok)
	env.chat.storing(hello["playerId"].(string), otherHello["playerId"].(string))

	write(t, alice, sendFrame("c-1", convID, "hi"))
	readFrame(t, alice) // ack
	readFrame(t, alice) // message.new
	noFrameWithin(t, other, 250*time.Millisecond, "a player of another game")
}

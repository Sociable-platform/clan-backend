package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

const lastMsgID = "0193a6c0-1111-7222-8333-944455556666"

func readFrameText(id, conv, last string) string {
	b, _ := json.Marshal(map[string]any{"type": "conversation.read", "id": id, "data": map[string]any{"conversationId": conv, "lastMessageId": last}})
	return string(b)
}

func TestConversationReadMovesThePositionAndSendsNoReply(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.markRead = func() error { return nil }
	c := env.mustDial(t)

	write(t, c, readFrameText("r-1", convID, lastMsgID))
	// No reply on success: the next frame the socket gets is the pong of the ping sent after it,
	// so nothing was queued in between (frames of one connection are handled in order).
	typ, data := exchange(t, c, `{"type":"ping","id":"p-1","data":{}}`)
	if typ != "pong" || data["replyTo"] != "p-1" {
		t.Fatalf("next frame = %s %v, want the pong (conversation.read has no reply)", typ, data)
	}
	// The game and the player are the connection's, not the frame's.
	calls := env.chat.recorded()
	want := readCall{who: service.Reader{GameID: testGameID, PlayerID: u42}, conversationID: convID, lastReadID: lastMsgID}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("service got %+v, want %+v", calls, want)
	}
}

func TestConversationReadIgnoresAGameOrPlayerInTheFrame(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.markRead = func() error { return nil }
	c := env.mustDial(t)
	frame := `{"type":"conversation.read","data":{"conversationId":"` + convID + `","lastMessageId":"` + lastMsgID +
		`","gameId":"22222222-2222-4222-8222-222222222222","playerId":"00000000-0000-4000-8000-000000000001"}}`
	write(t, c, frame)
	exchange(t, c, `{"type":"ping","data":{}}`)
	calls := env.chat.recorded()
	if len(calls) != 1 || calls[0].who != (service.Reader{GameID: testGameID, PlayerID: u42}) {
		t.Errorf("service got %+v, want the connection's game and player", calls)
	}
}

func TestConversationReadErrors(t *testing.T) {
	env := newWSEnv(t, nil)
	c := env.mustDial(t)
	tests := []struct {
		name     string
		frame    string
		serviceE error
		code     string
		calls    int // how often the service is asked
	}{
		{"not a member", readFrameText("r-1", convID, lastMsgID),
			httpx.NewError(http.StatusForbidden, "not_member", "You are not a member of this conversation"), "not_member", 1},
		{"message of another conversation", readFrameText("r-1", convID, lastMsgID),
			httpx.NewError(http.StatusBadRequest, "bad_frame", "lastMessageId is not a message of this conversation"), "bad_frame", 1},
		{"our own failure", readFrameText("r-1", convID, lastMsgID), errors.New("pq: connection refused"), "internal", 1},
		{"no data", `{"type":"conversation.read","id":"r-1"}`, nil, "bad_frame", 0},
		{"conversationId missing", `{"type":"conversation.read","id":"r-1","data":{"lastMessageId":"` + lastMsgID + `"}}`, nil, "bad_frame", 0},
		{"lastMessageId missing", `{"type":"conversation.read","id":"r-1","data":{"conversationId":"` + convID + `"}}`, nil, "bad_frame", 0},
		{"lastMessageId is a number", `{"type":"conversation.read","id":"r-1","data":{"conversationId":"` + convID + `","lastMessageId":7}}`, nil, "bad_frame", 0},
		{"data is a string", `{"type":"conversation.read","id":"r-1","data":"x"}`, nil, "bad_frame", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(env.chat.recorded())
			env.chat.markRead = func() error { return tt.serviceE }
			typ, data := exchange(t, c, tt.frame)
			if typ != "error" || data["code"] != tt.code || data["replyTo"] != "r-1" {
				t.Fatalf("reply = %s %v, want error %s replying to r-1", typ, data, tt.code)
			}
			if got := len(env.chat.recorded()) - before; got != tt.calls {
				t.Errorf("service asked %d times, want %d", got, tt.calls)
			}
			if msg, _ := data["message"].(string); strings.Contains(msg, "pq:") || strings.Contains(msg, "connection refused") {
				t.Errorf("the error message leaks the cause: %q", msg)
			}
			// The connection stays open: it still answers a ping.
			if typ, _ := exchange(t, c, `{"type":"ping","data":{}}`); typ != "pong" {
				t.Errorf("after the error the next frame is %s, want pong (the connection stays open)", typ)
			}
		})
	}
}

func TestConversationReadFailureIsLoggedWithoutTheFrame(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.markRead = func() error { return errors.New("pq: connection refused") }
	c := env.mustDial(t)
	write(t, c, readFrameText("r-1", convID, lastMsgID))
	exchange(t, c, `{"type":"ping","data":{}}`)
	logs := env.logs.String()
	if !strings.Contains(logs, "mark read") || !strings.Contains(logs, "connection refused") {
		t.Errorf("the failure is not logged: %s", logs)
	}
	if strings.Contains(logs, lastMsgID) {
		t.Error("the frame's content is in the logs")
	}
}

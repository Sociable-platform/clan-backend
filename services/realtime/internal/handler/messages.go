package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/ratelimit"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

const (
	frameTypeMessageAck = "message.ack"
	frameTypeMessageNew = "message.new"

	// sendTimeout limits the database work for one message.send. It starts from a fresh context, not
	// the connection's: a message whose insert has begun is stored and delivered even if the sender
	// disconnects or the service is stopping.
	sendTimeout = 5 * time.Second
)

// messageSendData is the data of a message.send frame. Pointers tell "missing" from "empty".
type messageSendData struct {
	ConversationID *string `json:"conversationId"`
	Body           *string `json:"body"`
}

type messageAckData struct {
	ReplyTo   *string   `json:"replyTo"`
	MessageID string    `json:"messageId"`
	CreatedAt time.Time `json:"createdAt"`
}

type messageNewData struct {
	ConversationID string          `json:"conversationId"`
	Message        service.Message `json:"message"`
}

// messageSend handles a message.send frame on the connection's reader goroutine: the service
// stores the message, then the sender gets message.ack and every connected member (the sender's
// other tabs too) gets message.new. The reply comes after the message is stored. A message the
// player may not send, or one we failed to store, gets an error frame and is sent to nobody.
func (h *Handler) messageSend(c *hub.Conn, f clientFrame, state *connState) {
	// The rate limit comes first and counts every message.send of the connection, well formed or not.
	if !h.allowSend(c, f, state) {
		return
	}
	var data messageSendData
	if len(f.Data) == 0 || json.Unmarshal(f.Data, &data) != nil || data.ConversationID == nil || data.Body == nil {
		h.enqueueError(c, codeBadFrame, "message.send needs conversationId and body, both text", f.idPointer())
		return
	}

	who := c.Identity()
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	// The game and the sender come from the connection (the verified token), never from the frame.
	sent, err := h.svc.SendMessage(ctx, service.Sender{GameID: who.GameID, ID: who.PlayerID, Name: who.Name},
		*data.ConversationID, *data.Body)
	if err != nil {
		var sendErr *service.SendError
		if errors.As(err, &sendErr) {
			h.enqueue(c, serverFrame{Type: frameTypeErr, Data: errorData{
				Code: sendErr.Code, Message: sendErr.Message, ReplyTo: f.idPointer(), RetryAfterMs: retryAfterMs(sendErr.RetryAfter),
			}})
			return
		}
		// Not logged with the body or the frame: only who and what failed.
		h.log.Error("store message", "game_id", who.GameID, "player_id", who.PlayerID, "error", err)
		h.enqueueError(c, service.CodeInternal, "Something went wrong; try again later", f.idPointer())
		return
	}

	// Both frames happened when the message was stored.
	h.enqueue(c, serverFrame{Type: frameTypeMessageAck, At: sent.Message.CreatedAt, Data: messageAckData{
		ReplyTo: f.idPointer(), MessageID: sent.Message.ID, CreatedAt: sent.Message.CreatedAt,
	}})

	// One encoded frame goes to every member, so they all get the same event_id.
	encoded, err := frame.Encode(who.GameID, frameTypeMessageNew, sent.Message.CreatedAt, messageNewData{
		ConversationID: *data.ConversationID, Message: sent.Message,
	})
	if err != nil {
		h.log.Error("encode message.new", "error", err) // cannot happen with these types
		return
	}
	h.hub.SendToPlayers(who.GameID, sent.Members, encoded)
}

// retryAfterMs turns a wait into the milliseconds the error frame carries: rounded up, at least 1, and nil for no wait.
func retryAfterMs(d time.Duration) *int {
	if d <= 0 {
		return nil
	}
	ms := int((d + time.Millisecond - 1) / time.Millisecond)
	return &ms
}

// RateLimit is the limit on message.send frames of one connection: at most Max in any period of Per.
// Strikes refused frames in a row (with no frame accepted between them) close the connection with 1008.
type RateLimit struct {
	Max     int           // default 5
	Per     time.Duration // default 5 s
	Strikes int           // default 3
	// Now is the clock (default time.Now). Tests use it to move time by hand.
	Now func() time.Time
}

func (r RateLimit) withDefaults() RateLimit {
	if r.Max <= 0 {
		r.Max = 5
	}
	if r.Per <= 0 {
		r.Per = 5 * time.Second
	}
	if r.Strikes <= 0 {
		r.Strikes = 3
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	return r
}

// connState is what the handler remembers about one connection between its frames. Only the goroutine that
// reads the connection's frames touches it, so it needs no lock.
type connState struct {
	window  *ratelimit.Window
	strikes int // refused message.send frames in a row
	max     int // how many strikes close the connection
	now     func() time.Time
}

func newConnState(rl RateLimit) *connState {
	rl = rl.withDefaults()
	return &connState{window: ratelimit.NewWindow(rl.Max, rl.Per), max: rl.Strikes, now: rl.Now}
}

// allowSend applies the rate limit to a message.send frame. A refused frame gets a rate_limited error;
// the third in a row (see RateLimit) also closes the connection with 1008, after the error frame was written.
func (h *Handler) allowSend(c *hub.Conn, f clientFrame, state *connState) bool {
	if state.window.Allow(state.now()) {
		state.strikes = 0
		return true
	}
	state.strikes++
	h.enqueueError(c, codeRateLimited, "You are sending messages too fast", f.idPointer())
	if state.strikes >= state.max {
		who := c.Identity()
		h.log.Warn("closing a connection that keeps hitting the rate limit", "game_id", who.GameID, "player_id", who.PlayerID)
		c.CloseAfterFlush(hub.ClosePolicy, "rate limit exceeded")
	}
	return false
}

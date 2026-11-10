package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
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
func (h *Handler) messageSend(c *hub.Conn, f clientFrame) {
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
			h.enqueueError(c, sendErr.Code, sendErr.Message, f.idPointer())
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

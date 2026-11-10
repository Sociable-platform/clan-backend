package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// readTimeout limits the database work for one conversation.read.
const readTimeout = 5 * time.Second

// conversationReadData is the data of a conversation.read frame. Pointers tell "missing" from "empty".
type conversationReadData struct {
	ConversationID *string `json:"conversationId"`
	LastMessageID  *string `json:"lastMessageId"`
}

// conversationRead handles a conversation.read frame on the connection's reader goroutine: the
// player's read position moves up to lastMessageId, so their unread count drops. There is no reply
// on success; a frame that cannot be accepted gets an error frame and nothing changes.
func (h *Handler) conversationRead(c *hub.Conn, f clientFrame) {
	var data conversationReadData
	if len(f.Data) == 0 || json.Unmarshal(f.Data, &data) != nil || data.ConversationID == nil || data.LastMessageID == nil {
		h.enqueueError(c, codeBadFrame, "conversation.read needs conversationId and lastMessageId, both text", f.idPointer())
		return
	}

	who := c.Identity()
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	// The game and the player come from the connection (the verified token), never from the frame.
	err := h.svc.MarkRead(ctx, service.Reader{GameID: who.GameID, PlayerID: who.PlayerID}, *data.ConversationID, *data.LastMessageID)
	if err == nil {
		return
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		h.enqueueError(c, he.Code, he.Message, f.idPointer())
		return
	}
	h.log.Error("mark read", "game_id", who.GameID, "player_id", who.PlayerID, "error", err)
	h.enqueueError(c, service.CodeInternal, "Something went wrong; try again later", f.idPointer())
}

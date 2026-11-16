package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// Page sizes of the message history (openapi/realtime.yaml).
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// MessagePage is one page of a conversation's history.
type MessagePage struct {
	Messages []Message `json:"messages"` // newest first
	HasMore  bool      `json:"hasMore"`  // older messages exist: ask again with before = the last message's id
}

// Reader is the player who reads: their game and their own social ID, both from the verified token.
type Reader struct {
	GameID   string
	PlayerID string
}

// Conversations returns the reader's conversations, most recently active first, each with its
// members (and whether they are online), its last message and the reader's unread count.
// A reader without conversations gets an empty list, never nil.
func (s *Service) Conversations(ctx context.Context, who Reader) ([]Conversation, error) {
	rows, err := s.store.ListConversations(ctx, who.GameID, who.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	out := make([]Conversation, 0, len(rows))
	for _, c := range rows {
		conv := Conversation{ID: c.ID, Kind: c.Kind, ClanID: c.ClanID, Members: make([]ConversationMember, 0, len(c.Members)), Unread: c.Unread, ReadOnly: c.ReadOnly}
		for _, m := range c.Members {
			// A read-only DM is not a friendship: its members' presence is not shown (always offline).
			conv.Members = append(conv.Members, ConversationMember{ID: m.PlayerID, Name: m.Name, Online: !c.ReadOnly && s.online(who.GameID, m.PlayerID)})
		}
		conv.LastMessage = toMessage(c.LastMessage)
		out = append(out, conv)
	}
	return out, nil
}

// Messages returns one page of the history of a conversation the reader is a member of, newest
// first. before and limit are the raw query values: before is a message ID ("" = the newest
// page), limit a whole number from 1 to 100 ("" = 50).
//
// Errors the client can act on (*httpx.Error):
//   - 400 invalid_before / invalid_limit
//   - 403 not_member: not a member, an unknown conversation, one of another game, or an ID that is
//     not a UUID (one answer for all, so IDs cannot be probed)
func (s *Service) Messages(ctx context.Context, who Reader, conversationID, before, limit string) (MessagePage, error) {
	n := DefaultPageSize
	if limit = strings.TrimSpace(limit); limit != "" {
		var err error
		if n, err = strconv.Atoi(limit); err != nil || n < 1 || n > MaxPageSize {
			return MessagePage{}, httpx.NewError(http.StatusBadRequest, "invalid_limit", "limit must be a whole number from 1 to 100")
		}
	}
	if before = strings.TrimSpace(before); before != "" {
		if !isUUID(before) {
			return MessagePage{}, httpx.NewError(http.StatusBadRequest, "invalid_before", "before must be a message ID")
		}
		before = strings.ToLower(before)
	}
	conversationID, err := cleanID("conversationId", conversationID)
	if err != nil {
		return MessagePage{}, errNotMember()
	}

	msgs, hasMore, err := s.store.ListMessages(ctx, who.GameID, who.PlayerID, conversationID, before, n)
	if errors.Is(err, repository.ErrNotMember) {
		return MessagePage{}, errNotMember()
	}
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", err)
	}
	page := MessagePage{Messages: make([]Message, 0, len(msgs)), HasMore: hasMore}
	for i := range msgs {
		page.Messages = append(page.Messages, *toMessage(&msgs[i]))
	}
	return page, nil
}

// MarkRead records that the reader has read the conversation up to lastMessageID, so their unread
// count drops. It only moves forward: an older ID than before changes nothing. Success has no result.
//
// Errors the player causes (*httpx.Error; the code is also the WebSocket error code):
//   - 403 not_member: not a member, an unknown conversation, one of another game, or an ID that is not a UUID
//   - 400 bad_frame: lastMessageID is not a UUID, or not a message of this conversation
func (s *Service) MarkRead(ctx context.Context, who Reader, conversationID, lastMessageID string) error {
	conversationID, err := cleanID("conversationId", conversationID)
	if err != nil {
		return errNotMember()
	}
	lastMessageID = strings.TrimSpace(lastMessageID)
	if !isUUID(lastMessageID) {
		return errNoSuchMessage()
	}

	err = s.store.MarkRead(ctx, who.GameID, who.PlayerID, conversationID, strings.ToLower(lastMessageID))
	switch {
	case errors.Is(err, repository.ErrNotMember):
		return errNotMember()
	case errors.Is(err, repository.ErrNoSuchMessage):
		return errNoSuchMessage()
	case err != nil:
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

func errNotMember() error {
	return httpx.NewError(http.StatusForbidden, CodeNotMember, "You are not a member of this conversation")
}

func errNoSuchMessage() error {
	return httpx.NewError(http.StatusBadRequest, CodeBadFrame, "lastMessageId is not a message of this conversation")
}

// toMessage converts a stored message to what the SDK sees; nil stays nil.
func toMessage(m *repository.Message) *Message {
	if m == nil {
		return nil
	}
	return &Message{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, Body: m.Body, CreatedAt: m.CreatedAt.UTC()}
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// Codes of the errors SendMessage returns, as the WebSocket protocol names them (openapi/realtime-ws.md).
const (
	CodeBadFrame  = "bad_frame"
	CodeNotMember = "not_member"
	CodeEmpty     = "empty"
	CodeTooLong   = "too_long"
	CodeInternal  = "internal"
)

// MaxBodyChars is the longest message in characters. It is the largest value a game can set as its
// own limit, so it is never too strict; the per-game limit comes with the message rules.
const MaxBodyChars = 1000

// SendError is a message the player may not send. Its Code and Message go to the player as an error frame.
type SendError struct {
	Code    string
	Message string
}

func (e *SendError) Error() string { return e.Code + ": " + e.Message }

// Sender is the player who sends a message.
type Sender struct {
	GameID string // from the verified token, never from the frame
	ID     string // social's player ID
	Name   string
}

// Sent is a stored message and the players who should get it.
type Sent struct {
	Message Message
	Members []string // every member of the conversation, the sender included
}

// IDs makes the IDs of new messages.
type IDs interface {
	// New returns a new UUIDv7 and the time inside it.
	New() (id string, at time.Time)
}

// WithIDs sets where new message IDs come from. Tests use it for fixed IDs.
func WithIDs(ids IDs) Option {
	return func(s *Service) { s.ids = ids }
}

// SendMessage stores a message from the sender in a conversation they are a member of.
// The body is trimmed first. Nothing is stored when it returns an error.
// Errors the player causes are *SendError; anything else is a failure of ours (the caller answers `internal`).
func (s *Service) SendMessage(ctx context.Context, from Sender, conversationID, body string) (Sent, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return Sent{}, &SendError{CodeEmpty, "The message is empty"}
	case strings.ContainsRune(body, 0):
		// Postgres text cannot hold a NUL character.
		return Sent{}, &SendError{CodeBadFrame, "The message contains a character that is not allowed"}
	case utf8.RuneCountInString(body) > MaxBodyChars:
		return Sent{}, &SendError{CodeTooLong, fmt.Sprintf("The message is longer than %d characters", MaxBodyChars)}
	}
	// A conversation ID that is not a UUID cannot exist, which is the same answer as one that does not.
	conversationID, err := cleanID("conversationId", conversationID)
	if err != nil {
		return Sent{}, notMember()
	}

	id, at := s.ids.New()
	members, err := s.store.InsertMessage(ctx, from.GameID, conversationID, from.ID, repository.NewMessage{
		ID: id, SenderName: from.Name, Body: body, CreatedAt: at,
	})
	if errors.Is(err, repository.ErrNotMember) {
		return Sent{}, notMember()
	}
	if err != nil {
		return Sent{}, fmt.Errorf("send message: %w", err)
	}
	return Sent{
		Message: Message{ID: id, SenderID: from.ID, SenderName: from.Name, Body: body, CreatedAt: at},
		Members: members,
	}, nil
}

func notMember() error {
	return &SendError{CodeNotMember, "You are not a member of this conversation"}
}

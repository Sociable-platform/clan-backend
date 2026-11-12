package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// Codes of the errors SendMessage returns, as the WebSocket protocol names them (openapi/realtime-ws.md).
// rate_limited is not here: the connection's limiter answers it before the service is called.
const (
	CodeBadFrame        = "bad_frame"
	CodeNotMember       = "not_member"
	CodeFeatureDisabled = "feature_disabled"
	CodeBlocked         = "blocked"
	CodeEmpty           = "empty"
	CodeTooLong         = "too_long"
	CodeSlowMode        = "slow_mode"
	CodeInternal        = "internal"
)

// MaxBodyChars is the longest message in characters, whatever the game's own limit: the largest value
// the config schema allows.
const MaxBodyChars = 1000

// SendError is a message the player may not send. Its Code and Message go to the player as an error frame.
type SendError struct {
	Code       string
	Message    string
	RetryAfter time.Duration // only for slow_mode: how long until the player may send again
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

// Rules gives the message rules of a game. gameconfig.Cache implements it. It never fails: for a game whose
// config is not known it returns gameconfig.DefaultChatRules.
type Rules interface {
	ChatRules(ctx context.Context, gameID string) gameconfig.ChatRules
}

// WithRules sets where the game's message rules come from. Without it every game has gameconfig.DefaultChatRules.
func WithRules(r Rules) Option {
	return func(s *Service) { s.rules = r }
}

// WithClock sets the clock of the slow mode. Tests use it.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// SendMessage stores a message from the sender in a conversation they are a member of, after the
// rules of the game. Nothing is stored when it returns an error. The checks run in this order, and the
// first one that fails answers:
//
//  1. the body, trimmed, is not empty and has no NUL character (empty, bad_frame)
//  2. the sender is a member (not_member; the same for an unknown conversation or another game's)
//  3. the game has this kind of chat turned on (feature_disabled)
//  4. in a DM, neither player blocked the other (blocked)
//  5. the body is at most the game's max_message_length characters (too_long)
//  6. the sender is not in slow mode, counted across all their tabs and conversations (slow_mode)
//
// and then, if the game turned the profanity filter on, banned words become ***. The rate limit of
// the connection comes before all of this, in the WebSocket handler.
// Errors the player causes are *SendError; anything else is a failure of ours (the caller answers `internal`).
func (s *Service) SendMessage(ctx context.Context, from Sender, conversationID, body string) (Sent, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return Sent{}, &SendError{Code: CodeEmpty, Message: "The message is empty"}
	case strings.ContainsRune(body, 0):
		// Postgres text cannot hold a NUL character.
		return Sent{}, &SendError{Code: CodeBadFrame, Message: "The message contains a character that is not allowed"}
	}
	// A conversation ID that is not a UUID cannot exist, which is the same answer as one that does not.
	conversationID, err := cleanID("conversationId", conversationID)
	if err != nil {
		return Sent{}, notMember()
	}

	conv, err := s.store.ConversationOf(ctx, from.GameID, conversationID, from.ID)
	if errors.Is(err, repository.ErrNotMember) {
		return Sent{}, notMember()
	}
	if err != nil {
		return Sent{}, fmt.Errorf("send message: %w", err)
	}

	rules := s.chatRules(ctx, from.GameID)
	if err := featureEnabled(rules, conv.Kind); err != nil {
		return Sent{}, err
	}
	if conv.Kind == kindDM {
		if err := s.checkNotBlocked(ctx, from, conv.Members); err != nil {
			return Sent{}, err
		}
	}
	limit := rules.MaxMessageLength
	if limit < 1 || limit > MaxBodyChars {
		limit = MaxBodyChars
	}
	if utf8.RuneCountInString(body) > limit {
		return Sent{}, &SendError{Code: CodeTooLong, Message: fmt.Sprintf("The message is longer than %d characters", limit)}
	}

	// The turn is taken now and given back if the message is not stored, so a failure of ours does not cost the player a wait.
	turn, retry, ok := s.slow.Reserve(from.GameID, from.ID, s.now(), time.Duration(rules.SlowModeSeconds)*time.Second)
	if !ok {
		return Sent{}, &SendError{Code: CodeSlowMode, Message: "Wait a moment before sending again", RetryAfter: retry}
	}

	if rules.ProfanityFilter {
		body = s.filter.Replace(body)
	}
	id, at := s.ids.New()
	members, err := s.store.InsertMessage(ctx, from.GameID, conversationID, from.ID, repository.NewMessage{
		ID: id, SenderName: from.Name, Body: body, CreatedAt: at,
	})
	if err != nil {
		turn.Cancel()
		if errors.Is(err, repository.ErrNotMember) { // removed from the conversation a moment ago
			return Sent{}, notMember()
		}
		return Sent{}, fmt.Errorf("send message: %w", err)
	}
	return Sent{
		Message: Message{ID: id, SenderID: from.ID, SenderName: from.Name, Body: body, CreatedAt: at},
		Members: members,
	}, nil
}

func (s *Service) chatRules(ctx context.Context, gameID string) gameconfig.ChatRules {
	if s.rules == nil {
		return gameconfig.DefaultChatRules
	}
	return s.rules.ChatRules(ctx, gameID)
}

// featureEnabled says whether the game allows messages in this kind of conversation.
func featureEnabled(rules gameconfig.ChatRules, kind string) error {
	switch {
	case !rules.Enabled:
		return &SendError{Code: CodeFeatureDisabled, Message: "Chat is turned off in this game"}
	case kind == kindDM && !rules.DirectMessages:
		return &SendError{Code: CodeFeatureDisabled, Message: "Direct messages are turned off in this game"}
	case kind == kindClan && !rules.ClanChat:
		return &SendError{Code: CodeFeatureDisabled, Message: "Clan chat is turned off in this game"}
	}
	return nil
}

// checkNotBlocked fails when one of the other members of a DM blocked the sender or was blocked by them.
func (s *Service) checkNotBlocked(ctx context.Context, from Sender, members []string) error {
	for _, m := range members {
		if m == from.ID {
			continue
		}
		blocked, err := s.store.IsBlocked(ctx, from.GameID, from.ID, m)
		if err != nil {
			return fmt.Errorf("send message: %w", err)
		}
		if blocked {
			return &SendError{Code: CodeBlocked, Message: "You can't message this player"}
		}
	}
	return nil
}

func notMember() error {
	return &SendError{Code: CodeNotMember, Message: "You are not a member of this conversation"}
}

// Package service holds the realtime service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/profanity"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/ratelimit"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/uuidv7"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	// UpsertDM makes the DM conversation of the pair and both memberships; safe to repeat.
	UpsertDM(ctx context.Context, gameID, dmKey string, a, b repository.Member) (repository.DM, error)
	// MakeDMReadOnly keeps both memberships and marks the DM read-only (the two are not friends any more).
	// changed is false without such a conversation or when it already was read-only. UpsertDM opens it again.
	MakeDMReadOnly(ctx context.Context, gameID, dmKey string) (dm repository.DM, changed bool, err error)
	// JoinClanRoom makes the clan's conversation and the player's membership; safe to repeat.
	// Joined is true only when the player was not a member yet.
	JoinClanRoom(ctx context.Context, gameID, clanID string, m repository.Member) (repository.ClanRoom, error)
	// LeaveClanRoom deletes the player's membership; safe to repeat. found is false without a room for
	// the clan; Left is true only when the player was a member.
	LeaveClanRoom(ctx context.Context, gameID, clanID, playerID string) (leave repository.ClanLeave, found bool, err error)
	// InsertMemberMessage stores a member line (a join or a leave) and returns the members now.
	InsertMemberMessage(ctx context.Context, gameID, conversationID string, m repository.MemberMessage) ([]string, error)
	// DeleteClanRoom deletes the clan's conversation and returns who its members were. found is false without one.
	DeleteClanRoom(ctx context.Context, gameID, clanID string) (conversationID string, memberIDs []string, found bool, err error)
	// RenamePlayer stores a new display name for the player's memberships in the game; safe to repeat.
	RenamePlayer(ctx context.Context, gameID, playerID, name string) error
	// ListConversations returns the player's conversations in the game, most recently active first.
	ListConversations(ctx context.Context, gameID, playerID string) ([]repository.PlayerConversation, error)
	// ListMessages returns one page of history, newest first, and whether older messages exist.
	// It returns repository.ErrNotMember when the player is not in the conversation.
	ListMessages(ctx context.Context, gameID, playerID, conversationID, before string, limit int) ([]repository.Message, bool, error)
	// MarkRead moves the player's read position forward to the message; safe to repeat. It returns
	// repository.ErrNotMember or repository.ErrNoSuchMessage.
	MarkRead(ctx context.Context, gameID, playerID, conversationID, messageID string) error
	// ConversationOf returns the kind and the members of a conversation the player is a member of.
	// It returns repository.ErrNotMember otherwise.
	ConversationOf(ctx context.Context, gameID, conversationID, playerID string) (repository.ConversationInfo, error)
	// IsBlocked reports whether one of the two players blocked the other.
	IsBlocked(ctx context.Context, gameID, a, b string) (bool, error)
	// AddBlock and RemoveBlock keep Realtime's copy of Social's blocks; both are safe to repeat.
	AddBlock(ctx context.Context, gameID, blockerID, blockedID string) error
	RemoveBlock(ctx context.Context, gameID, blockerID, blockedID string) error
	// InsertMessage stores a message if the sender is a member and returns all members.
	// It returns repository.ErrNotMember otherwise.
	InsertMessage(ctx context.Context, gameID, conversationID, senderID string, m repository.NewMessage) ([]string, error)
	// RunOnce runs fn and records the event as handled, in one transaction, unless it was recorded
	// before (ran is false). fn's error is returned as it is, and then nothing is recorded.
	RunOnce(ctx context.Context, gameID, eventID string, fn func(ctx context.Context) error) (ran bool, err error)
	// DeleteProcessedEvents deletes the records of the events handled before the given time.
	DeleteProcessedEvents(ctx context.Context, before time.Time) (int64, error)
}

// Service is the realtime service's logic.
type Service struct {
	store    Store
	presence Presence
	ids      IDs
	rules    Rules
	slow     *ratelimit.Slow
	filter   *profanity.Filter
	now      func() time.Time
}

// New returns a Service.
func New(store Store, opts ...Option) *Service {
	s := &Service{store: store, ids: uuidv7.New(time.Now), slow: ratelimit.NewSlow(), filter: profanity.Default(), now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

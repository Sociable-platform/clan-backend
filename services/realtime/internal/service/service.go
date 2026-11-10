// Package service holds the realtime service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/uuidv7"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	// UpsertDM makes the DM conversation of the pair and both memberships; safe to repeat.
	UpsertDM(ctx context.Context, gameID, dmKey string, a, b repository.Member) (repository.DM, error)
	// RemoveDMMembers deletes both memberships; safe to repeat. found is false without such a conversation.
	RemoveDMMembers(ctx context.Context, gameID, dmKey string) (conversationID string, found bool, err error)
	// RenamePlayer stores a new display name for the player's memberships in the game; safe to repeat.
	RenamePlayer(ctx context.Context, gameID, playerID, name string) error
	// InsertMessage stores a message if the sender is a member and returns all members.
	// It returns repository.ErrNotMember otherwise.
	InsertMessage(ctx context.Context, gameID, conversationID, senderID string, m repository.NewMessage) ([]string, error)
}

// Service is the realtime service's logic.
type Service struct {
	store    Store
	presence Presence
	ids      IDs
}

// New returns a Service.
func New(store Store, opts ...Option) *Service {
	s := &Service{store: store, ids: uuidv7.New(time.Now)}
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

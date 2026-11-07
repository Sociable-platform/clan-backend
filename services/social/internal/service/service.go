// Package service holds the social service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	UpsertPlayer(ctx context.Context, gameID, externalID, displayName string) (repository.Player, error)
	ListClans(ctx context.Context, gameID, query string, limit int) ([]repository.Clan, error)
}

// Service is the social service's logic.
type Service struct {
	store Store
}

// New returns a Service.
func New(store Store) *Service {
	return &Service{store: store}
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

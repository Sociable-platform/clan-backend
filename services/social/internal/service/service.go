// Package service holds the social service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	UpsertPlayer(ctx context.Context, gameID, externalID, displayName string) (repository.Player, error)
	ListClans(ctx context.Context, gameID, query string, opts repository.ListClansOptions) ([]repository.Clan, error)
	CreateClan(ctx context.Context, gameID, ownerID, name string) (repository.Clan, error)
	JoinClan(ctx context.Context, gameID, clanID, playerID string) (repository.Clan, error)
	LeaveClan(ctx context.Context, gameID, clanID, externalID string) error
	MyClan(ctx context.Context, gameID, externalID string) (repository.PlayerClan, error)
	ListMembers(ctx context.Context, gameID, clanID string) ([]repository.Member, error)
}

// Service is the social service's logic.
type Service struct {
	store  Store
	events events.Publisher
}

// New returns a Service. Its events are dropped until WithPublisher sets a publisher.
func New(store Store) *Service {
	return &Service{store: store, events: events.Noop{}}
}

// WithPublisher sets where the service sends events for the other services (see pkg/events).
// It returns s, so it can follow New: service.New(repo).WithPublisher(p).
func (s *Service) WithPublisher(p events.Publisher) *Service {
	s.events = p
	return s
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

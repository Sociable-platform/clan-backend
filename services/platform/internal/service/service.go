// Package service holds the platform service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
}

// Service is the platform service's logic.
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

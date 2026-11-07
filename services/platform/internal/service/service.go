// Package service holds the platform service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	CreateDeveloper(ctx context.Context, email, passwordHash, name string) (repository.Developer, error)
	DeveloperByEmail(ctx context.Context, email string) (repository.Developer, error)
	DeveloperByID(ctx context.Context, id string) (repository.Developer, error)
	CreateGame(ctx context.Context, developerID, name, description string) (repository.Game, error)
	GamesByDeveloper(ctx context.Context, developerID string) ([]repository.Game, error)
	GameOfDeveloper(ctx context.Context, gameID, developerID string) (repository.Game, error)
}

// Service is the platform service's logic.
type Service struct {
	store      Store
	key        ed25519.PrivateKey
	pub        ed25519.PublicKey
	tokenTTL   time.Duration
	bcryptCost int
	// dummyHash is compared against when a login email is unknown, so that
	// "no such developer" and "wrong password" take about the same time.
	dummyHash []byte
	now       func() time.Time
}

// New returns a Service. key signs developer tokens (and its public half verifies them);
// tokenTTL is how long a developer token stays valid.
func New(store Store, key ed25519.PrivateKey, tokenTTL time.Duration) (*Service, error) {
	return newService(store, key, tokenTTL, bcrypt.DefaultCost)
}

func newService(store Store, key ed25519.PrivateKey, tokenTTL time.Duration, cost int) (*Service, error) {
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not an Ed25519 private key")
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate dummy password: %w", err)
	}
	dummy, err := bcrypt.GenerateFromPassword(secret, cost)
	if err != nil {
		return nil, fmt.Errorf("generate dummy hash: %w", err)
	}
	return &Service{
		store: store, key: key, pub: pub, tokenTTL: tokenTTL,
		bcryptCost: cost, dummyHash: dummy, now: time.Now,
	}, nil
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

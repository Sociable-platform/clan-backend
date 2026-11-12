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

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	CreateDeveloper(ctx context.Context, email, passwordHash, name string) (repository.Developer, error)
	DeveloperByEmail(ctx context.Context, email string) (repository.Developer, error)
	DeveloperByID(ctx context.Context, id string) (repository.Developer, error)
	AdminByEmail(ctx context.Context, email string) (repository.Admin, error)
	AdminByID(ctx context.Context, id string) (repository.Admin, error)
	ListDevelopers(ctx context.Context, f repository.DeveloperFilter) ([]repository.DeveloperSummary, error)
	AdminDeveloperByID(ctx context.Context, id string) (repository.AdminDeveloper, error)
	AdminGamesByDeveloper(ctx context.Context, developerID string) ([]repository.AdminGame, error)
	AuditEntries(ctx context.Context, f repository.AuditFilter) ([]repository.AuditEntry, error)
	CreateGame(ctx context.Context, developerID, name, description string) (repository.Game, error)
	GamesByDeveloper(ctx context.Context, developerID string) ([]repository.Game, error)
	GameOfDeveloper(ctx context.Context, gameID, developerID string) (repository.Game, error)
	GameByID(ctx context.Context, gameID string) (repository.Game, error)
	SetAllowedOrigins(ctx context.Context, gameID, developerID string, origins []string) (repository.Game, error)
	CreateAPIKey(ctx context.Context, gameID, keyPrefix, keyHash string) (repository.APIKey, error)
	APIKeysByGame(ctx context.Context, gameID string) ([]repository.APIKey, error)
	RevokeAPIKey(ctx context.Context, keyID, developerID string) error
	GameIDByActiveKeyHash(ctx context.Context, keyHash string) (string, error)
	WidgetDraftOfGame(ctx context.Context, gameID string) (repository.WidgetDraft, error)
	SaveWidgetDraft(ctx context.Context, gameID string, config []byte) (repository.WidgetDraft, error)
	AddWidgetVersion(ctx context.Context, gameID, developerID string, config []byte) (repository.WidgetVersion, error)
	WidgetVersionsOfGame(ctx context.Context, gameID string) ([]repository.WidgetVersion, error)
	WidgetVersionOfGame(ctx context.Context, gameID string, version int) (repository.WidgetVersion, error)
	LatestWidgetVersion(ctx context.Context, gameID string) (repository.WidgetVersion, error)
}

// Service is the platform service's logic.
type Service struct {
	store    Store
	key      ed25519.PrivateKey
	pub      ed25519.PublicKey
	tokenTTL time.Duration
	// adminTokenTTL is how long an admin token stays valid; admin sessions are short on purpose.
	adminTokenTTL time.Duration
	bcryptCost    int
	// dummyHash is compared against when a login email is unknown, so that
	// "no such developer" and "wrong password" take about the same time.
	dummyHash []byte
	now       func() time.Time
	// events tells the other services what changed (config.published); see pkg/events.
	events events.Publisher
}

// defaultAdminTokenTTL is used until WithAdminTokenTTL is called.
const defaultAdminTokenTTL = time.Hour

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
		store: store, key: key, pub: pub, tokenTTL: tokenTTL, adminTokenTTL: defaultAdminTokenTTL,
		bcryptCost: cost, dummyHash: dummy, now: time.Now, events: events.Noop{},
	}, nil
}

// WithPublisher sets where the service sends events for the other services (see pkg/events).
// Until it is called, events are dropped. It returns s, so it can follow New.
func (s *Service) WithPublisher(p events.Publisher) *Service {
	s.events = p
	return s
}

// WithAdminTokenTTL sets how long an admin token stays valid (default 1 hour). It returns s,
// so it can follow New.
func (s *Service) WithAdminTokenTTL(ttl time.Duration) *Service {
	s.adminTokenTTL = ttl
	return s
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

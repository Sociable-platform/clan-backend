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
	DeveloperStatusOf(ctx context.Context, id string) (string, error)
	CreatePasswordResetToken(ctx context.Context, developerID, tokenHash string, expiresAt, now time.Time) error
	ResetPassword(ctx context.Context, tokenHash, passwordHash string, now time.Time) (developerID string, err error)
	CreateRefreshSession(ctx context.Context, developerID, tokenHash string, expiresAt, now time.Time) error
	RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt, now time.Time, grace time.Duration) (developerID string, err error)
	DeleteRefreshSession(ctx context.Context, tokenHash string) error
	SavePendingRegistration(ctx context.Context, p repository.PendingRegistration, now time.Time) error
	PendingRegistrationByEmail(ctx context.Context, email string, now time.Time) (repository.PendingRegistration, error)
	ReplacePendingRegistrationCode(ctx context.Context, email, codeHash string, expiresAt, now time.Time) error
	ConfirmPendingRegistration(ctx context.Context, email, codeHash string, now time.Time, maxAttempts int) (repository.Developer, error)
	AdminByEmail(ctx context.Context, email string) (repository.Admin, error)
	AdminByID(ctx context.Context, id string) (repository.Admin, error)
	ListDevelopers(ctx context.Context, f repository.DeveloperFilter) ([]repository.DeveloperSummary, error)
	AdminDeveloperByID(ctx context.Context, id string) (repository.AdminDeveloper, error)
	AdminGamesByDeveloper(ctx context.Context, developerID string) ([]repository.AdminGame, error)
	AuditEntries(ctx context.Context, f repository.AuditFilter) ([]repository.AuditEntry, error)
	SetDeveloperStatus(ctx context.Context, developerID string, c repository.StatusChange) (repository.AdminDeveloper, bool, error)
	SetGameStatus(ctx context.Context, gameID string, c repository.StatusChange) (repository.AdminGame, bool, error)
	GameAccessOf(ctx context.Context, gameID string) (repository.GameAccess, error)
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
	// InTx runs fn in one transaction; the store methods fn calls with its ctx join it. fn's error
	// is returned as it is, and then nothing fn did is kept.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	// AddEvents writes events to the outbox inside the transaction of InTx; outside it fails.
	AddEvents(ctx context.Context, evs ...events.Event) error
}

// Service is the platform service's logic.
type Service struct {
	store    Store
	key      ed25519.PrivateKey
	pub      ed25519.PublicKey
	tokenTTL time.Duration
	// refreshTokenTTL is how long a developer's refresh token stays valid (WithRefreshTokenTTL).
	refreshTokenTTL time.Duration
	// adminTokenTTL is how long an admin token stays valid; admin sessions are short on purpose.
	adminTokenTTL time.Duration
	bcryptCost    int
	// dummyHash is compared against when a login email is unknown, so that
	// "no such developer" and "wrong password" take about the same time.
	dummyHash []byte
	now       func() time.Time
	// resetSender delivers password reset links, which point at dashboardURL (WithPasswordReset).
	resetSender  ResetSender
	dashboardURL string
	// verification delivers the codes of registrations with email verification. nil: verification
	// is off and Register creates the account at once (WithEmailVerification).
	verification VerificationSender
}

// defaultAdminTokenTTL is used until WithAdminTokenTTL is called.
const defaultAdminTokenTTL = time.Hour

// defaultDashboardURL is where reset links point until WithPasswordReset is called: the
// dashboard's dev server.
const defaultDashboardURL = "http://localhost:5173"

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
		bcryptCost: cost, dummyHash: dummy, now: time.Now, refreshTokenTTL: defaultRefreshTokenTTL,
		resetSender: noResetSender{}, dashboardURL: defaultDashboardURL,
	}, nil
}

// change makes one change and records its events for the other services, in one transaction:
// fn does the change through the store with the ctx it is given and returns the events that
// describe it, which are written to the outbox before the commit. Either both are kept or neither
// is, and the outbox relay publishes the events once the transaction has committed (pkg/events).
// fn's error is returned as it is, so callers map the store's errors as before.
func (s *Service) change(ctx context.Context, fn func(ctx context.Context) ([]events.Event, error)) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		evs, err := fn(ctx)
		if err != nil || len(evs) == 0 {
			return err
		}
		return s.store.AddEvents(ctx, evs...)
	})
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

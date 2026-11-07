package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// keyPrefix starts every API key, so a leaked key is easy to recognise and report.
	keyPrefix = "clk_"
	// keyRandomBytes of randomness per key: 256 bits.
	keyRandomBytes = 32
	// keyDisplayLen is how many leading characters are stored in clear and shown in the
	// dashboard: the "clk_" marker plus 4 random characters.
	keyDisplayLen = len(keyPrefix) + 4
)

// APIKey describes a key without its secret. It is what lists show.
type APIKey struct {
	ID        string
	GameID    string
	KeyPrefix string
	CreatedAt time.Time
	RevokedAt *time.Time // nil while active
}

// NewAPIKey is a freshly created key. Key is the full secret; it exists only in this
// value and is never stored, so it cannot be shown again.
type NewAPIKey struct {
	APIKey
	Key string
}

func errAPIKeyNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "api_key_not_found", "API key not found")
}

func errInvalidAPIKey() *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, "invalid_api_key", "API key is missing, wrong or revoked")
}

// hashKey returns the hex sha256 of a full key. Keys are 256 random bits, so a fast
// hash is enough; there is nothing to brute-force.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func newKey() (string, error) {
	b := make([]byte, keyRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateAPIKey makes a new key for a game the developer owns. The full key is in the
// result and nowhere else: the database gets only its prefix and its hash.
func (s *Service) CreateAPIKey(ctx context.Context, developerID, gameID string) (NewAPIKey, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return NewAPIKey{}, err
	}
	key, err := newKey()
	if err != nil {
		return NewAPIKey{}, err
	}
	k, err := s.store.CreateAPIKey(ctx, gameID, key[:keyDisplayLen], hashKey(key))
	if err != nil {
		return NewAPIKey{}, fmt.Errorf("create api key for game %s: %w", gameID, err)
	}
	return NewAPIKey{APIKey: toAPIKey(k), Key: key}, nil
}

// ListAPIKeys returns the keys (prefix only, active and revoked) of a game the developer owns.
func (s *Service) ListAPIKeys(ctx context.Context, developerID, gameID string) ([]APIKey, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return nil, err
	}
	rows, err := s.store.APIKeysByGame(ctx, gameID)
	if err != nil {
		return nil, fmt.Errorf("list api keys of game %s: %w", gameID, err)
	}
	keys := make([]APIKey, 0, len(rows))
	for _, k := range rows {
		keys = append(keys, toAPIKey(k))
	}
	return keys, nil
}

// RevokeAPIKey revokes a key of a game the developer owns. Revoking twice is fine.
// A key that does not exist, a malformed ID and another developer's key all give the same 404.
func (s *Service) RevokeAPIKey(ctx context.Context, developerID, keyID string) error {
	if !uuidPattern.MatchString(keyID) {
		return errAPIKeyNotFound()
	}
	err := s.store.RevokeAPIKey(ctx, keyID, developerID)
	if errors.Is(err, repository.ErrNotFound) {
		return errAPIKeyNotFound()
	}
	if err != nil {
		return fmt.Errorf("revoke api key %s: %w", keyID, err)
	}
	return nil
}

// GameForAPIKey returns the game an active API key belongs to, or 401 invalid_api_key
// for a key that is malformed, unknown or revoked. It checks the database on every call,
// so a key stops working the moment it is revoked. The player-token endpoint uses it.
func (s *Service) GameForAPIKey(ctx context.Context, key string) (string, error) {
	if !strings.HasPrefix(key, keyPrefix) {
		return "", errInvalidAPIKey()
	}
	gameID, err := s.store.GameIDByActiveKeyHash(ctx, hashKey(key))
	if errors.Is(err, repository.ErrNotFound) {
		return "", errInvalidAPIKey()
	}
	if err != nil {
		return "", fmt.Errorf("check api key: %w", err)
	}
	return gameID, nil
}

func toAPIKey(k repository.APIKey) APIKey {
	return APIKey{ID: k.ID, GameID: k.GameID, KeyPrefix: k.KeyPrefix, CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt}
}

package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIKey is a row of the api_keys table, without the hash (nothing outside the
// repository needs it).
type APIKey struct {
	ID        string
	GameID    string
	KeyPrefix string
	CreatedAt time.Time
	RevokedAt *time.Time // nil while the key is active
}

// CreateAPIKey stores a key for gameID. Only the prefix and the hash are stored.
func (r *Repository) CreateAPIKey(ctx context.Context, gameID, keyPrefix, keyHash string) (APIKey, error) {
	if r.pool == nil {
		return APIKey{}, ErrNoDatabase
	}
	k := APIKey{GameID: gameID, KeyPrefix: keyPrefix}
	err := r.db(ctx).QueryRow(ctx,
		`insert into api_keys (id, game_id, key_prefix, key_hash)
		 values (gen_random_uuid(), $1, $2, $3)
		 returning id::text, created_at`,
		gameID, keyPrefix, keyHash,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return APIKey{}, fmt.Errorf("insert api key: %w", err)
	}
	return k, nil
}

// APIKeysByGame returns all keys of a game, active and revoked, newest first.
// A game without keys gets an empty, non-nil slice.
func (r *Repository) APIKeysByGame(ctx context.Context, gameID string) ([]APIKey, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select id::text, game_id::text, key_prefix, created_at, revoked_at
		 from api_keys where game_id = $1
		 order by created_at desc, id`, gameID)
	if err != nil {
		return nil, fmt.Errorf("select api keys: %w", err)
	}
	defer rows.Close()

	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.GameID, &k.KeyPrefix, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read api keys: %w", err)
	}
	return keys, nil
}

// RevokeAPIKey revokes the key keyID if it belongs to a game owned by developerID.
// Revoking a key that is already revoked succeeds and keeps the first revocation time.
// A key that does not exist and a key of someone else's game both give ErrNotFound.
func (r *Repository) RevokeAPIKey(ctx context.Context, keyID, developerID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	tag, err := r.db(ctx).Exec(ctx,
		`update api_keys k set revoked_at = coalesce(k.revoked_at, now())
		 from games g
		 where k.id = $1 and g.id = k.game_id and g.developer_id = $2`,
		keyID, developerID)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GameIDByActiveKeyHash returns the game an active (not revoked) key belongs to,
// or ErrNotFound. It reads revoked_at on every call, so a revoked key stops working
// at once.
func (r *Repository) GameIDByActiveKeyHash(ctx context.Context, keyHash string) (string, error) {
	if r.pool == nil {
		return "", ErrNoDatabase
	}
	var gameID string
	err := r.db(ctx).QueryRow(ctx,
		`select game_id::text from api_keys where key_hash = $1 and revoked_at is null`, keyHash,
	).Scan(&gameID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select api key: %w", err)
	}
	return gameID, nil
}

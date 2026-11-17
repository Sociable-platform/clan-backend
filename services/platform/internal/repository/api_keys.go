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

// RevokeAPIKey revokes the key keyID if developerID is an owner of the game it belongs to. The role is read
// and used in one statement, so a role that changes at the same moment cannot let a member revoke.
// Revoking a key that is already revoked succeeds and keeps the first revocation time.
// A key that does not exist and a key of a game the developer is not in both give ErrNotFound; a member
// of the game, who may not manage its keys, gets ErrOwnerRequired.
func (r *Repository) RevokeAPIKey(ctx context.Context, keyID, developerID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	var role *string
	var revoked bool
	err := r.db(ctx).QueryRow(ctx,
		`with target as (
		     select k.id, m.role
		     from api_keys k join game_members m on m.game_id = k.game_id and m.developer_id = $2
		     where k.id = $1),
		 done as (
		     update api_keys set revoked_at = coalesce(revoked_at, now())
		     where id in (select id from target where role = 'owner')
		     returning id)
		 select (select role from target), exists (select 1 from done)`,
		keyID, developerID).Scan(&role, &revoked)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	switch {
	case role == nil:
		return ErrNotFound
	case !revoked:
		return ErrOwnerRequired
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

package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrRefreshTokenReused means a refresh token was sent again long after a refresh had replaced
// it. Two parties hold the same token, so the session it belonged to has been ended.
var ErrRefreshTokenReused = errors.New("refresh token reused")

// CreateRefreshSession starts a session for a developer who just logged in: it stores the hash of
// the session's first refresh token under a new session ID. The developer's rows that expired
// before now are removed on the way, which keeps the table small.
func (r *Repository) CreateRefreshSession(ctx context.Context, developerID, tokenHash string, expiresAt, now time.Time) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.db(ctx).Exec(ctx,
			`delete from developer_refresh_tokens where developer_id = $1 and expires_at <= $2`,
			developerID, now); err != nil {
			return fmt.Errorf("delete expired refresh tokens: %w", err)
		}
		if _, err := r.db(ctx).Exec(ctx,
			`insert into developer_refresh_tokens (token_hash, developer_id, session_id, expires_at)
			 values ($1, $2, gen_random_uuid(), $3)`,
			tokenHash, developerID, expiresAt); err != nil {
			return fmt.Errorf("insert refresh token: %w", err)
		}
		return nil
	})
}

// RotateRefreshToken replaces the refresh token with hash oldHash by a new one (newHash, valid
// until newExpiresAt) in the same session, and returns the developer the session belongs to.
//
//   - No such token, or it expired before now: ErrNotFound, nothing changes.
//   - The token is the session's current one: it is marked as replaced and the new one is stored.
//   - The token was replaced at most grace ago: the new one is stored as well. This is the same
//     refresh arriving twice (a lost answer, two tabs starting together), not a stolen token.
//   - The token was replaced longer ago: every token of the session is deleted and the result is
//     ErrRefreshTokenReused.
//
// The row is locked while this runs, so of two requests with the same token the second one waits
// and then sees what the first one did.
func (r *Repository) RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt, now time.Time, grace time.Duration) (string, error) {
	if r.pool == nil {
		return "", ErrNoDatabase
	}
	var developerID string
	reused := false
	err := r.InTx(ctx, func(ctx context.Context) error {
		var (
			sessionID  string
			expiresAt  time.Time
			replacedAt *time.Time
		)
		err := r.db(ctx).QueryRow(ctx,
			`select developer_id::text, session_id::text, expires_at, replaced_at
			 from developer_refresh_tokens where token_hash = $1 for update`,
			oldHash).Scan(&developerID, &sessionID, &expiresAt, &replacedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("select refresh token: %w", err)
		}
		if !expiresAt.After(now) {
			return ErrNotFound
		}

		switch {
		case replacedAt == nil:
			if _, err := r.db(ctx).Exec(ctx,
				`update developer_refresh_tokens set replaced_at = $2 where token_hash = $1`,
				oldHash, now); err != nil {
				return fmt.Errorf("mark refresh token as replaced: %w", err)
			}
		case now.Sub(*replacedAt) > grace:
			// The deletion must be kept, so this is not returned as an error from inside the
			// transaction (that would roll it back); see below.
			if _, err := r.db(ctx).Exec(ctx,
				`delete from developer_refresh_tokens where session_id = $1`, sessionID); err != nil {
				return fmt.Errorf("end reused refresh session: %w", err)
			}
			reused = true
			return nil
		}

		if _, err := r.db(ctx).Exec(ctx,
			`insert into developer_refresh_tokens (token_hash, developer_id, session_id, expires_at)
			 values ($1, $2, $3, $4)`,
			newHash, developerID, sessionID, newExpiresAt); err != nil {
			return fmt.Errorf("insert refresh token: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if reused {
		return "", ErrRefreshTokenReused
	}
	return developerID, nil
}

// DeleteRefreshSession ends the session that the refresh token with that hash belongs to: every
// token of the session is deleted, the replaced ones too. An unknown hash changes nothing and is
// not an error, so logging out twice is fine.
func (r *Repository) DeleteRefreshSession(ctx context.Context, tokenHash string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	if _, err := r.db(ctx).Exec(ctx,
		`delete from developer_refresh_tokens
		 where session_id = (select session_id from developer_refresh_tokens where token_hash = $1)`,
		tokenHash); err != nil {
		return fmt.Errorf("delete refresh session: %w", err)
	}
	return nil
}

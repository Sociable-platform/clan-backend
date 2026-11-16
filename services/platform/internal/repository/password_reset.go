package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreatePasswordResetToken stores the hash of a new reset token for a developer. The developer's
// earlier tokens are removed in the same transaction, so only the newest link works; tokens of
// anyone that expired before now are removed too, which keeps the table small.
func (r *Repository) CreatePasswordResetToken(ctx context.Context, developerID, tokenHash string, expiresAt, now time.Time) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.db(ctx).Exec(ctx,
			`delete from password_reset_tokens where developer_id = $1 or expires_at <= $2`,
			developerID, now); err != nil {
			return fmt.Errorf("delete old password reset tokens: %w", err)
		}
		if _, err := r.db(ctx).Exec(ctx,
			`insert into password_reset_tokens (token_hash, developer_id, expires_at) values ($1, $2, $3)`,
			tokenHash, developerID, expiresAt); err != nil {
			return fmt.Errorf("insert password reset token: %w", err)
		}
		return nil
	})
}

// ResetPassword uses up the reset token with that hash and gives its developer the new password
// hash, in one transaction. It returns the developer's ID, or ErrNotFound when there is no such
// token or it expired before now; then nothing is changed.
//
// The token row is deleted first, so of two requests with the same token only one finds it.
// password_changed_at is set to now, which makes the access tokens of earlier logins invalid, and
// the developer's refresh tokens are deleted, so those logins cannot get new ones either.
func (r *Repository) ResetPassword(ctx context.Context, tokenHash, passwordHash string, now time.Time) (string, error) {
	if r.pool == nil {
		return "", ErrNoDatabase
	}
	var developerID string
	err := r.InTx(ctx, func(ctx context.Context) error {
		err := r.db(ctx).QueryRow(ctx,
			`delete from password_reset_tokens where token_hash = $1 and expires_at > $2
			 returning developer_id::text`,
			tokenHash, now).Scan(&developerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("use password reset token: %w", err)
		}
		if _, err := r.db(ctx).Exec(ctx,
			`update developers set password_hash = $2, password_changed_at = $3 where id = $1`,
			developerID, passwordHash, now); err != nil {
			return fmt.Errorf("update developer password: %w", err)
		}
		// Every session ends: whoever was logged in with the old password must log in again.
		if _, err := r.db(ctx).Exec(ctx,
			`delete from developer_refresh_tokens where developer_id = $1`, developerID); err != nil {
			return fmt.Errorf("delete refresh tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return developerID, nil
}

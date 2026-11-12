package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Admin is a row of the admins table.
type Admin struct {
	ID           string
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
	// DisabledAt is nil for an admin who can sign in.
	DisabledAt *time.Time
}

// AdminByEmail returns the admin with that (already lowercased) email, or ErrNotFound.
// A disabled admin is returned too; the service decides what that means.
func (r *Repository) AdminByEmail(ctx context.Context, email string) (Admin, error) {
	if r.pool == nil {
		return Admin{}, ErrNoDatabase
	}
	row := r.pool.QueryRow(ctx,
		`select id::text, email, password_hash, name, created_at, disabled_at
		 from admins where email = $1`, email)
	return scanAdmin(row)
}

// AdminByID returns the admin with that ID, or ErrNotFound.
func (r *Repository) AdminByID(ctx context.Context, id string) (Admin, error) {
	if r.pool == nil {
		return Admin{}, ErrNoDatabase
	}
	row := r.pool.QueryRow(ctx,
		`select id::text, email, password_hash, name, created_at, disabled_at
		 from admins where id = $1`, id)
	return scanAdmin(row)
}

func scanAdmin(row pgx.Row) (Admin, error) {
	var a Admin
	err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.CreatedAt, &a.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrNotFound
	}
	if err != nil {
		return Admin{}, fmt.Errorf("select admin: %w", err)
	}
	return a, nil
}

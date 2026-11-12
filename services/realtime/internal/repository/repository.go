// Package repository is the only part of the realtime service that talks to the database.
// It reads and writes the realtime schema only, never another service's tables.
package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoDatabase means the service was started without a database URL.
var ErrNoDatabase = errors.New("database not configured")

// Repository holds the database pool.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository. pool may be nil when no database is configured.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Ping checks that the database is reachable.
func (r *Repository) Ping(ctx context.Context) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.pool.Ping(ctx)
}

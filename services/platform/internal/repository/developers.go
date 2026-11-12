package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors the service turns into API errors.
var (
	// ErrEmailTaken means a developer with that email already exists.
	ErrEmailTaken = errors.New("email already registered")
	// ErrNotFound means no row matched.
	ErrNotFound = errors.New("not found")
)

// uniqueViolation is the Postgres error code for a broken unique constraint.
const uniqueViolation = "23505"

// Developer is a row of the developers table.
type Developer struct {
	ID           string
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
}

// CreateDeveloper inserts a developer and returns it with its generated ID.
// It returns ErrEmailTaken when the email is already registered.
func (r *Repository) CreateDeveloper(ctx context.Context, email, passwordHash, name string) (Developer, error) {
	if r.pool == nil {
		return Developer{}, ErrNoDatabase
	}
	d := Developer{Email: email, PasswordHash: passwordHash, Name: name}
	err := r.pool.QueryRow(ctx,
		`insert into developers (id, email, password_hash, name)
		 values (gen_random_uuid(), $1, $2, $3)
		 returning id::text, created_at`,
		email, passwordHash, name,
	).Scan(&d.ID, &d.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Developer{}, ErrEmailTaken
		}
		return Developer{}, fmt.Errorf("insert developer: %w", err)
	}
	return d, nil
}

// DeveloperByEmail returns the developer with that (already lowercased) email, or ErrNotFound.
func (r *Repository) DeveloperByEmail(ctx context.Context, email string) (Developer, error) {
	if r.pool == nil {
		return Developer{}, ErrNoDatabase
	}
	row := r.pool.QueryRow(ctx,
		`select id::text, email, password_hash, name, created_at
		 from developers where email = $1`, email)
	return scanDeveloper(row)
}

// DeveloperByID returns the developer with that ID, or ErrNotFound.
func (r *Repository) DeveloperByID(ctx context.Context, id string) (Developer, error) {
	if r.pool == nil {
		return Developer{}, ErrNoDatabase
	}
	row := r.pool.QueryRow(ctx,
		`select id::text, email, password_hash, name, created_at
		 from developers where id = $1`, id)
	return scanDeveloper(row)
}

func scanDeveloper(row pgx.Row) (Developer, error) {
	var d Developer
	err := row.Scan(&d.ID, &d.Email, &d.PasswordHash, &d.Name, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Developer{}, ErrNotFound
	}
	if err != nil {
		return Developer{}, fmt.Errorf("select developer: %w", err)
	}
	return d, nil
}

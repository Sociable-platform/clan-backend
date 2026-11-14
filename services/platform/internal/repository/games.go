package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// foreignKeyViolation is the Postgres error code for a reference to a row that does not exist.
const foreignKeyViolation = "23503"

// Game is a row of the games table.
type Game struct {
	ID          string
	DeveloperID string
	Name        string
	Description string // empty when the database value is null
	// AllowedOrigins are the websites that may open a WebSocket for this game. Never nil.
	AllowedOrigins []string
	CreatedAt      time.Time
}

// CreateGame inserts a game owned by developerID and returns it with its generated ID.
// It returns ErrNotFound when the developer does not exist.
func (r *Repository) CreateGame(ctx context.Context, developerID, name, description string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	g := Game{DeveloperID: developerID, Name: name, Description: description}
	err := r.db(ctx).QueryRow(ctx,
		`insert into games (id, developer_id, name, description)
		 values (gen_random_uuid(), $1, $2, nullif($3, ''))
		 returning id::text, allowed_origins, created_at`,
		developerID, name, description,
	).Scan(&g.ID, &g.AllowedOrigins, &g.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return Game{}, ErrNotFound
		}
		return Game{}, fmt.Errorf("insert game: %w", err)
	}
	return g, nil
}

// GamesByDeveloper returns the games owned by developerID, newest first.
// A developer without games gets an empty, non-nil slice.
func (r *Repository) GamesByDeveloper(ctx context.Context, developerID string) ([]Game, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select id::text, developer_id::text, name, coalesce(description, ''), allowed_origins, created_at
		 from games where developer_id = $1
		 order by created_at desc, id`, developerID)
	if err != nil {
		return nil, fmt.Errorf("select games: %w", err)
	}
	defer rows.Close()

	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.DeveloperID, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		games = append(games, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read games: %w", err)
	}
	return games, nil
}

// GameOfDeveloper returns the game with that ID only if developerID owns it.
// A game that does not exist and a game owned by someone else both give ErrNotFound.
func (r *Repository) GameOfDeveloper(ctx context.Context, gameID, developerID string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, developer_id::text, name, coalesce(description, ''), allowed_origins, created_at
		 from games where id = $1 and developer_id = $2`, gameID, developerID,
	).Scan(&g.ID, &g.DeveloperID, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("select game: %w", err)
	}
	return g, nil
}

// GameByID returns the game with that ID whoever owns it, or ErrNotFound. Only for callers that
// are not a developer (the internal endpoint); every developer-facing lookup uses GameOfDeveloper.
func (r *Repository) GameByID(ctx context.Context, gameID string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, developer_id::text, name, coalesce(description, ''), allowed_origins, created_at
		 from games where id = $1`, gameID,
	).Scan(&g.ID, &g.DeveloperID, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("select game by id: %w", err)
	}
	return g, nil
}

// SetAllowedOrigins replaces the allowed origins of a game that developerID owns and returns the
// game. A game that does not exist and a game owned by someone else both give ErrNotFound.
func (r *Repository) SetAllowedOrigins(ctx context.Context, gameID, developerID string, origins []string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	if origins == nil {
		origins = []string{} // a null would break the not null column
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`update games set allowed_origins = $3
		 where id = $1 and developer_id = $2
		 returning id::text, developer_id::text, name, coalesce(description, ''), allowed_origins, created_at`,
		gameID, developerID, origins,
	).Scan(&g.ID, &g.DeveloperID, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("update allowed origins: %w", err)
	}
	return g, nil
}

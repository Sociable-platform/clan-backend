package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// WidgetDraft is a row of the widget_drafts table: the config a developer is still editing.
type WidgetDraft struct {
	GameID    string
	Config    []byte // JSON text
	UpdatedAt time.Time
}

// WidgetDraftOfGame returns the draft of a game, or ErrNotFound when none was saved yet.
// It does not check who owns the game; the service does that first.
func (r *Repository) WidgetDraftOfGame(ctx context.Context, gameID string) (WidgetDraft, error) {
	if r.pool == nil {
		return WidgetDraft{}, ErrNoDatabase
	}
	d := WidgetDraft{GameID: gameID}
	err := r.pool.QueryRow(ctx,
		`select config, updated_at from widget_drafts where game_id = $1`, gameID,
	).Scan(&d.Config, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WidgetDraft{}, ErrNotFound
	}
	if err != nil {
		return WidgetDraft{}, fmt.Errorf("select widget draft: %w", err)
	}
	return d, nil
}

// SaveWidgetDraft stores config as the game's draft, replacing the previous one.
// There is one draft per game, so saving twice keeps only the last config.
// It returns ErrNotFound when the game does not exist.
func (r *Repository) SaveWidgetDraft(ctx context.Context, gameID string, config []byte) (WidgetDraft, error) {
	if r.pool == nil {
		return WidgetDraft{}, ErrNoDatabase
	}
	d := WidgetDraft{GameID: gameID}
	err := r.pool.QueryRow(ctx,
		`insert into widget_drafts (game_id, config) values ($1, $2)
		 on conflict (game_id) do update set config = excluded.config, updated_at = now()
		 returning config, updated_at`,
		gameID, config,
	).Scan(&d.Config, &d.UpdatedAt)
	if err != nil {
		if pgErrorCode(err) == foreignKeyViolation {
			return WidgetDraft{}, ErrNotFound
		}
		return WidgetDraft{}, fmt.Errorf("upsert widget draft: %w", err)
	}
	return d, nil
}

// WidgetVersion is a row of the widget_versions table: one published config.
// Rows are only ever added, never changed, so a version always keeps the config it was
// published with.
type WidgetVersion struct {
	ID          string
	GameID      string
	Version     int    // 1, 2, 3 ... per game
	Config      []byte // JSON text; nil in lists, which leave the config out
	PublishedAt time.Time
	PublishedBy string // developer ID
}

// AddWidgetVersion publishes config as the next version of a game: one more than the
// highest version so far, or 1 for the first. Publishes of the same game run one after
// the other (the game row is locked), so version numbers never repeat and never skip.
// It returns ErrNotFound when the game or the developer does not exist.
func (r *Repository) AddWidgetVersion(ctx context.Context, gameID, developerID string, config []byte) (WidgetVersion, error) {
	if r.pool == nil {
		return WidgetVersion{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("begin publish: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no effect once committed

	// "no key update" is enough to make two publishes of one game wait for each other,
	// and it does not block other tables that only reference the game, such as api_keys.
	var locked string
	err = tx.QueryRow(ctx, `select id::text from games where id = $1 for no key update`, gameID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return WidgetVersion{}, ErrNotFound
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("lock game: %w", err)
	}

	v := WidgetVersion{GameID: gameID, PublishedBy: developerID, Config: config}
	err = tx.QueryRow(ctx,
		`insert into widget_versions (id, game_id, version, config, published_by)
		 select gen_random_uuid(), $1::uuid, coalesce(max(version), 0) + 1, $2::jsonb, $3::uuid
		 from widget_versions where game_id = $1::uuid
		 returning id::text, version, published_at`,
		gameID, config, developerID,
	).Scan(&v.ID, &v.Version, &v.PublishedAt)
	if err != nil {
		if pgErrorCode(err) == foreignKeyViolation {
			return WidgetVersion{}, ErrNotFound // the developer does not exist
		}
		return WidgetVersion{}, fmt.Errorf("insert widget version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WidgetVersion{}, fmt.Errorf("commit publish: %w", err)
	}
	return v, nil
}

// WidgetVersionsOfGame lists the versions of a game, newest first, without their configs.
// A game that was never published gets an empty, non-nil slice.
func (r *Repository) WidgetVersionsOfGame(ctx context.Context, gameID string) ([]WidgetVersion, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.pool.Query(ctx,
		`select id::text, game_id::text, version, published_at, published_by::text
		 from widget_versions where game_id = $1 order by version desc`, gameID)
	if err != nil {
		return nil, fmt.Errorf("select widget versions: %w", err)
	}
	defer rows.Close()

	versions := []WidgetVersion{}
	for rows.Next() {
		var v WidgetVersion
		if err := rows.Scan(&v.ID, &v.GameID, &v.Version, &v.PublishedAt, &v.PublishedBy); err != nil {
			return nil, fmt.Errorf("scan widget version: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read widget versions: %w", err)
	}
	return versions, nil
}

// WidgetVersionOfGame returns one version of a game, with its config, or ErrNotFound.
func (r *Repository) WidgetVersionOfGame(ctx context.Context, gameID string, version int) (WidgetVersion, error) {
	if r.pool == nil {
		return WidgetVersion{}, ErrNoDatabase
	}
	var v WidgetVersion
	err := r.pool.QueryRow(ctx,
		`select id::text, game_id::text, version, config, published_at, published_by::text
		 from widget_versions where game_id = $1 and version = $2`, gameID, version,
	).Scan(&v.ID, &v.GameID, &v.Version, &v.Config, &v.PublishedAt, &v.PublishedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return WidgetVersion{}, ErrNotFound
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("select widget version: %w", err)
	}
	return v, nil
}

// LatestWidgetVersion returns the newest published version of a game, with its config,
// or ErrNotFound if the game was never published. Drafts are never looked at: they live in
// another table.
func (r *Repository) LatestWidgetVersion(ctx context.Context, gameID string) (WidgetVersion, error) {
	if r.pool == nil {
		return WidgetVersion{}, ErrNoDatabase
	}
	var v WidgetVersion
	err := r.pool.QueryRow(ctx,
		`select id::text, game_id::text, version, config, published_at, published_by::text
		 from widget_versions where game_id = $1 order by version desc limit 1`, gameID,
	).Scan(&v.ID, &v.GameID, &v.Version, &v.Config, &v.PublishedAt, &v.PublishedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return WidgetVersion{}, ErrNotFound
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("select latest widget version: %w", err)
	}
	return v, nil
}

// pgErrorCode returns the Postgres error code inside err, or "" for any other error.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

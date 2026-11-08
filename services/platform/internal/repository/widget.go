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

// pgErrorCode returns the Postgres error code inside err, or "" for any other error.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

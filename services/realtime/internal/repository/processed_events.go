package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// txKey is the context key under which RunOnce passes its transaction to the repository methods
// that fn calls.
type txKey struct{}

// dbtx is what the pool and a transaction have in common.
type dbtx interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// db returns the transaction of RunOnce when ctx carries one, otherwise the pool. Inside that
// transaction a method's own Begin starts a savepoint, so everything the method writes commits, or
// rolls back, together with the record of the event.
func (r *Repository) db(ctx context.Context) dbtx {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return r.pool
}

// RunOnce handles an event at most once. In one transaction it records the event as handled and
// runs fn, which does the event's writes through the repository with the ctx it is given. When fn
// fails, nothing is kept, the record included, so the event can be handled again; fn's error is
// returned as it is. When the event was recorded before, fn is not run and ran is false.
//
// A second delivery that arrives while the first is still being handled waits for it: if the first
// commits, the second is skipped; if it rolls back, the second is handled.
func (r *Repository) RunOnce(ctx context.Context, gameID, eventID string, fn func(ctx context.Context) error) (ran bool, err error) {
	if r.pool == nil {
		return false, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin event %s: %w", eventID, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // does nothing after a commit

	tag, err := tx.Exec(ctx, `
		INSERT INTO processed_events (game_id, event_id) VALUES ($1, $2)
		ON CONFLICT (game_id, event_id) DO NOTHING`, gameID, eventID)
	if err != nil {
		return false, fmt.Errorf("record event %s: %w", eventID, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit event %s: %w", eventID, err)
	}
	return true, nil
}

// DeleteProcessedEvents deletes the records of the events handled before the given time and returns
// how many there were. A later copy of such an event would be handled again.
func (r *Repository) DeleteProcessedEvents(ctx context.Context, before time.Time) (int64, error) {
	if r.pool == nil {
		return 0, ErrNoDatabase
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM processed_events WHERE handled_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete processed events: %w", err)
	}
	return tag.RowsAffected(), nil
}

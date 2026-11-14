package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// errNoTx means AddEvents was called outside InTx, so the events could not commit with a change.
var errNoTx = errors.New("events must be added inside InTx")

// txKey is the context key under which InTx passes its transaction to the repository methods
// that fn calls.
type txKey struct{}

// dbtx is what the pool and a transaction have in common.
type dbtx interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// db returns the transaction of InTx when ctx carries one, otherwise the pool. Inside that
// transaction a method's own Begin starts a savepoint, so what the method writes, and the locks
// it takes, last until the whole InTx commits or rolls back.
func (r *Repository) db(ctx context.Context) dbtx {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return r.pool
}

// InTx runs fn in one transaction. The repository methods fn calls with the ctx it is given join
// that transaction, and AddEvents writes into it, so a change and the events that describe it
// commit together or not at all. fn's error is returned as it is, after the rollback. Called
// inside another InTx, it joins that transaction as a savepoint.
func (r *Repository) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // does nothing after a commit

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// AddEvents writes evs to the outbox table, in order, inside the transaction of InTx. The
// service's relay publishes them after the commit (pkg/events). Outside InTx it fails, so an
// event is never written apart from its change.
func (r *Repository) AddEvents(ctx context.Context, evs ...events.Event) error {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	if !ok {
		return errNoTx
	}
	for _, e := range evs {
		if err := events.Enqueue(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

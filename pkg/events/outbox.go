package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The transactional outbox.
//
// Publishing straight after a change loses the event when the broker is down, or when the service
// stops between the commit and the publish. Instead a service writes the event with Enqueue into its
// own outbox table, inside the transaction of the change: the two commit together or not at all.
// A Relay, running in the background of the same service, publishes the rows that were not sent yet,
// oldest first, and marks each one sent only after the broker has confirmed it.
//
// Delivery is therefore at least once: when the relay stops after the broker confirmed a row but
// before the row was marked sent, the row is sent again. Consumers must handle a repeated event_id.

// outboxTable is the table Enqueue and the relay use, in the schema of the service's database user
// (its search_path). Every service that publishes creates it with its own migration.
const outboxTable = "outbox"

const (
	// DefaultRelayInterval is how long the relay waits before looking again when there was nothing to send.
	DefaultRelayInterval = time.Second
	// DefaultRelayRetention is how long a sent row stays in the outbox before it is deleted.
	DefaultRelayRetention = 7 * 24 * time.Hour

	relayBatchSize    = 100              // rows claimed and sent in one transaction
	relayMaxBackoff   = 30 * time.Second // the longest wait between two failed attempts
	relayCleanupEvery = time.Hour        // how often sent rows past the retention are deleted
	relayFinishWait   = 5 * time.Second  // time to record a batch's result, also during shutdown
	maxLastErrorLen   = 500              // characters of a send error kept in last_error
)

// ErrInvalidEvent means an event cannot be written to the outbox, because it has no ID or no type.
var ErrInvalidEvent = errors.New("events: event needs an ID and a type")

// Enqueue writes e to the service's outbox table inside tx. The relay publishes it once tx has
// committed; if tx rolls back, the event goes with it. It takes a transaction, never a pool, so an
// event cannot be written apart from the change it describes.
func Enqueue(ctx context.Context, tx pgx.Tx, e Event) error {
	return enqueue(ctx, tx, outboxTable, e)
}

// enqueue is Enqueue with the table as an argument, so the integration test can use a table of its own.
func enqueue(ctx context.Context, tx pgx.Tx, table string, e Event) error {
	if e.ID == "" || e.Type == "" {
		return ErrInvalidEvent
	}
	// The row holds the event as it will be published, plus the trace context of the request that
	// caused it (trace.go): the relay publishes later, outside the request, and continues the trace.
	payload, err := json.Marshal(storedEvent{Event: e, Trace: captureTrace(ctx)})
	if err != nil {
		return fmt.Errorf("events: encode %s: %w", e.Type, err)
	}
	// The table name is a constant (or a test's own table), quoted by pgx; every value is a parameter.
	sql := fmt.Sprintf(`insert into %s (event_id, event_type, payload) values ($1, $2, $3)`, pgx.Identifier{table}.Sanitize())
	if _, err := tx.Exec(ctx, sql, e.ID, e.Type, payload); err != nil {
		return fmt.Errorf("events: write %s %s to the outbox: %w", e.Type, e.ID, err)
	}
	return nil
}

// RelayConfig tunes a Relay. A zero field gets its default.
type RelayConfig struct {
	// Interval is how long the relay waits before looking again when there was nothing to send
	// (default DefaultRelayInterval). It is also the first wait after a failure.
	Interval time.Duration
	// Retention is how long a sent row is kept before it is deleted (default DefaultRelayRetention).
	Retention time.Duration
}

// Relay publishes the events a service wrote to its outbox. Run it in one goroutine per service.
// Several relays on the same table (several copies of a service) never send the same row at the
// same time: each claims its rows with FOR UPDATE SKIP LOCKED.
type Relay struct {
	store outboxStore
	pub   Publisher
	cfg   RelayConfig
	log   *slog.Logger

	// Replaced in tests.
	now          func() time.Time
	batchSize    int
	maxBackoff   time.Duration
	cleanupEvery time.Duration
}

// NewRelay returns a relay that reads the outbox through pool and sends with pub. pub must return
// the broker's errors (a RabbitMQ, not one wrapped in Safe), or failed sends are marked sent.
// A negative duration in cfg is an error.
func NewRelay(pool *pgxpool.Pool, pub Publisher, cfg RelayConfig, log *slog.Logger) (*Relay, error) {
	if pool == nil {
		return nil, errors.New("events: the outbox relay needs a database")
	}
	return newRelay(pgStore{pool: pool, table: outboxTable}, pub, cfg, log)
}

func newRelay(store outboxStore, pub Publisher, cfg RelayConfig, log *slog.Logger) (*Relay, error) {
	if cfg.Interval < 0 || cfg.Retention < 0 {
		return nil, fmt.Errorf("events: the outbox interval and retention cannot be negative, got %s and %s", cfg.Interval, cfg.Retention)
	}
	if cfg.Interval == 0 {
		cfg.Interval = DefaultRelayInterval
	}
	if cfg.Retention == 0 {
		cfg.Retention = DefaultRelayRetention
	}
	return &Relay{
		store:        store,
		pub:          pub,
		cfg:          cfg,
		log:          log,
		now:          time.Now,
		batchSize:    relayBatchSize,
		maxBackoff:   max(relayMaxBackoff, cfg.Interval),
		cleanupEvery: relayCleanupEvery,
	}, nil
}

// Run sends the outbox until ctx ends, then returns. After a failure (broker or database down) it
// waits Interval, doubling up to 30 seconds, and tries again; the rows stay in the outbox meanwhile.
// It finishes recording the batch it is sending before it returns.
func (r *Relay) Run(ctx context.Context) {
	r.log.Info("outbox relay started", "interval", r.cfg.Interval.String(), "retention", r.cfg.Retention.String())
	defer r.log.Info("outbox relay stopped")

	var wait time.Duration
	backoff := r.cfg.Interval
	failing := false
	nextCleanup := r.now()
	for {
		if !sleep(ctx, wait) {
			return
		}
		if !r.now().Before(nextCleanup) {
			r.cleanup(ctx)
			nextCleanup = r.now().Add(r.cleanupEvery)
		}

		sent, more, err := r.sendBatch(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.log.Warn("outbox relay could not send, will retry", "sent", sent, "error", err, "retry_in", backoff.String())
			wait, backoff, failing = backoff, min(2*backoff, r.maxBackoff), true
			continue
		}
		if failing {
			r.log.Info("outbox relay is sending again")
			failing = false
		}
		if sent > 0 {
			r.log.Debug("outbox events sent", "count", sent)
		}
		backoff = r.cfg.Interval
		if more {
			wait = 0 // the batch was full: more rows may be waiting
		} else {
			wait = r.cfg.Interval
		}
	}
}

// sendBatch claims up to batchSize unsent rows, oldest first, and publishes them in that order. It
// stops at the first row that fails, so a later event never overtakes an earlier one, and records
// which rows were sent. more reports a full batch with every row sent: there may be more waiting.
func (r *Relay) sendBatch(ctx context.Context) (sent int, more bool, err error) {
	c, err := r.store.claim(ctx, r.batchSize)
	if err != nil {
		return 0, false, fmt.Errorf("claim outbox rows: %w", err)
	}
	rows := c.rows()

	var done []int64
	var failed *failedRow
	var sendErr error
	for _, row := range rows {
		if err := r.pub.Publish(ctx, row.event); err != nil {
			sendErr = fmt.Errorf("publish %s %s: %w", row.event.Type, row.event.ID, err)
			if ctx.Err() == nil { // a send cut short by shutdown is not the row's fault
				failed = &failedRow{id: row.id, reason: truncate(err.Error(), maxLastErrorLen)}
			}
			break
		}
		done = append(done, row.id)
	}

	// Record the result even when ctx has ended: rows the broker confirmed must not be sent again.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relayFinishWait)
	defer cancel()
	if err := c.finish(finishCtx, done, failed); err != nil {
		return 0, false, fmt.Errorf("mark %d outbox rows sent: %w", len(done), err)
	}
	if sendErr != nil {
		return len(done), false, sendErr
	}
	return len(done), len(rows) == r.batchSize, nil
}

// cleanup deletes the sent rows older than the retention period. A failure is only logged: the
// rows are deleted next time.
func (r *Relay) cleanup(ctx context.Context) {
	n, err := r.store.deleteSent(ctx, r.now().Add(-r.cfg.Retention))
	switch {
	case err != nil && ctx.Err() == nil:
		r.log.Warn("outbox cleanup failed, will retry", "error", err)
	case n > 0:
		r.log.Info("outbox cleanup deleted sent events", "count", n)
	}
}

// sleep waits d, or returns false at once when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// pendingRow is an outbox row waiting to be sent.
type pendingRow struct {
	id    int64
	event Event
}

// failedRow is the row whose send failed, and why.
type failedRow struct {
	id     int64
	reason string
}

// outboxStore is the database side of the relay. pgStore is the real one; tests use a fake.
type outboxStore interface {
	// claim locks up to limit unsent rows, oldest first, that no other relay holds.
	claim(ctx context.Context, limit int) (outboxClaim, error)
	// deleteSent deletes the rows sent before the given time and returns how many there were.
	deleteSent(ctx context.Context, before time.Time) (int64, error)
}

// outboxClaim is a batch of locked rows. finish must be called exactly once.
type outboxClaim interface {
	rows() []pendingRow
	// finish marks sent as sent, counts a failed attempt for failed (when not nil), and releases the rows.
	finish(ctx context.Context, sent []int64, failed *failedRow) error
}

// pgStore is the outbox table in PostgreSQL.
type pgStore struct {
	pool  *pgxpool.Pool
	table string
}

func (s pgStore) claim(ctx context.Context, limit int) (outboxClaim, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.lockUnsent(ctx, tx, limit)
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, err
	}
	return &pgClaim{tx: tx, table: s.table, pending: rows}, nil
}

// lockUnsent reads the unsent rows inside tx. The locks last until tx ends; rows another relay has
// locked are skipped, not waited for.
func (s pgStore) lockUnsent(ctx context.Context, tx pgx.Tx, limit int) ([]pendingRow, error) {
	sql := fmt.Sprintf(`select id, payload from %s where sent_at is null order by id limit $1 for update skip locked`,
		pgx.Identifier{s.table}.Sanitize())
	rows, err := tx.Query(ctx, sql, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pendingRow
	for rows.Next() {
		var (
			id      int64
			payload []byte
		)
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		var stored storedEvent
		if err := json.Unmarshal(payload, &stored); err != nil {
			return nil, fmt.Errorf("decode outbox row %d: %w", id, err)
		}
		e := stored.Event
		e.trace = stored.Trace
		out = append(out, pendingRow{id: id, event: e})
	}
	return out, rows.Err()
}

func (s pgStore) deleteSent(ctx context.Context, before time.Time) (int64, error) {
	sql := fmt.Sprintf(`delete from %s where sent_at < $1`, pgx.Identifier{s.table}.Sanitize())
	tag, err := s.pool.Exec(ctx, sql, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// pgClaim holds the transaction that locks a batch of rows.
type pgClaim struct {
	tx      pgx.Tx
	table   string
	pending []pendingRow
}

func (c *pgClaim) rows() []pendingRow { return c.pending }

func (c *pgClaim) finish(ctx context.Context, sent []int64, failed *failedRow) error {
	defer func() { _ = c.tx.Rollback(ctx) }() // does nothing after a commit
	table := pgx.Identifier{c.table}.Sanitize()
	if len(sent) > 0 {
		sql := fmt.Sprintf(`update %s set sent_at = now() where id = any($1)`, table)
		if _, err := c.tx.Exec(ctx, sql, sent); err != nil {
			return err
		}
	}
	if failed != nil {
		sql := fmt.Sprintf(`update %s set attempts = attempts + 1, last_error = $2 where id = $1`, table)
		if _, err := c.tx.Exec(ctx, sql, failed.id, failed.reason); err != nil {
			return err
		}
	}
	return c.tx.Commit(ctx)
}

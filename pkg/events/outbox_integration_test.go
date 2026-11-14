package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
)

// These tests need a real PostgreSQL. They run only when SOCIAL_TEST_DATABASE_URL is set (see
// README "Integration tests"). Each test creates a table of its own with a random name in the
// social schema, built from the outbox migration, and drops it afterwards, so a running social
// service never sees its rows.
func testOutbox(t *testing.T) (*pgxpool.Pool, pgStore) {
	t.Helper()
	url := os.Getenv("SOCIAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SOCIAL_TEST_DATABASE_URL is not set; skipping database test")
	}
	ddl, err := os.ReadFile("../../services/social/migrations/000008_create_outbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	table := "outbox_test_" + strings.ReplaceAll(NewID(), "-", "")[:12]
	// Every "outbox" in the migration (the table and its indexes) gets the test's name.
	if _, err := pool.Exec(ctx, strings.ReplaceAll(string(ddl), "outbox", table)); err != nil {
		pool.Close()
		t.Fatalf("create %s: %v", table, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "drop table "+pgx.Identifier{table}.Sanitize())
		pool.Close()
	})
	return pool, pgStore{pool: pool, table: table}
}

// write enqueues events in one transaction and commits it, or rolls it back when commit is false.
func write(t *testing.T, pool *pgxpool.Pool, table string, commit bool, events ...Event) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, e := range events {
		if err := enqueue(ctx, tx, table, e); err != nil {
			t.Fatal(err)
		}
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table, where string) int {
	t.Helper()
	var n int
	sql := fmt.Sprintf("select count(*) from %s where %s", pgx.Identifier{table}.Sanitize(), where)
	if err := pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOutboxEventsCommitAndRollBackWithTheTransaction(t *testing.T) {
	pool, store := testOutbox(t)
	kept := New("friend.accepted", NewID(), map[string]any{"request_id": NewID(), "n": 3})
	write(t, pool, store.table, true, kept)
	write(t, pool, store.table, false, New("friend.removed", NewID(), nil))

	rec := &Recorder{}
	r := mustRelay(t, store, rec, RelayConfig{})
	sent, _, err := r.sendBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("sent %d events, want only the committed one", sent)
	}
	got := rec.Events()[0]
	if got.ID != kept.ID || got.Type != kept.Type || got.GameID != kept.GameID || !got.OccurredAt.Equal(kept.OccurredAt) ||
		got.Data["request_id"] != kept.Data["request_id"] || got.Data["n"] != float64(3) {
		t.Errorf("published %+v, want %+v as it was written", got, kept)
	}
	if n := countRows(t, pool, store.table, "sent_at is not null"); n != 1 {
		t.Errorf("%d rows marked sent, want 1", n)
	}
}

func TestOutboxRelayKeepsTheOrderAndRetriesAFailure(t *testing.T) {
	pool, store := testOutbox(t)
	events := testEventsWithIDs(5)
	write(t, pool, store.table, true, events[:3]...)
	write(t, pool, store.table, true, events[3:]...)

	pub := &flakyPublisher{failOn: events[2].ID}
	r := mustRelay(t, store, pub, RelayConfig{})
	if _, _, err := r.sendBatch(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if n := countRows(t, pool, store.table, "sent_at is null"); n != 3 {
		t.Errorf("%d rows unsent, want 3 (the failed one and the two after it)", n)
	}
	if n := countRows(t, pool, store.table, "attempts = 1 and last_error like '%unavailable%'"); n != 1 {
		t.Errorf("%d rows with a recorded failure, want 1", n)
	}

	pub.failOn = ""
	if _, _, err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := pub.sentIDs(), ids(events); !slices.Equal(got, want) {
		t.Errorf("published %v, want %v (write order)", got, want)
	}
}

func TestOutboxTwoRelaysNeverSendTheSameRow(t *testing.T) {
	pool, store := testOutbox(t)
	events := testEventsWithIDs(200)
	write(t, pool, store.table, true, events...)

	// Both relays share one publisher, which slows each send a little so their batches overlap.
	var mu sync.Mutex
	count := map[string]int{}
	pub := publisherFunc(func(_ context.Context, e Event) error {
		time.Sleep(100 * time.Microsecond)
		mu.Lock()
		defer mu.Unlock()
		count[e.ID]++
		return nil
	})

	var wg sync.WaitGroup
	for range 2 {
		r := mustRelay(t, store, pub, RelayConfig{})
		r.batchSize = 10
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				sent, _, err := r.sendBatch(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if sent == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()

	if len(count) != len(events) {
		t.Errorf("%d events sent, want %d", len(count), len(events))
	}
	for id, n := range count {
		if n != 1 {
			t.Errorf("event %s sent %d times", id, n)
		}
	}
}

func TestOutboxCleanupDeletesOnlyOldSentRows(t *testing.T) {
	pool, store := testOutbox(t)
	write(t, pool, store.table, true, testEventsWithIDs(3)...)
	table := pgx.Identifier{store.table}.Sanitize()
	ctx := context.Background()
	// Row 1 was sent long ago, row 2 just now, row 3 is still waiting.
	if _, err := pool.Exec(ctx, "update "+table+" set sent_at = now() - interval '10 days' where id = (select min(id) from "+table+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "update "+table+" set sent_at = now() where id = (select min(id) + 1 from "+table+")"); err != nil {
		t.Fatal(err)
	}

	n, err := store.deleteSent(ctx, time.Now().Add(-DefaultRelayRetention))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}
	if left := countRows(t, pool, store.table, "true"); left != 2 {
		t.Errorf("%d rows left, want 2", left)
	}
}

type publisherFunc func(context.Context, Event) error

func (f publisherFunc) Publish(ctx context.Context, e Event) error { return f(ctx, e) }

func testEventsWithIDs(n int) []Event {
	out := make([]Event, n)
	for i := range out {
		out[i] = New("friend.accepted", NewID(), map[string]any{"i": i})
	}
	return out
}

func ids(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

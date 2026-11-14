package repository

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// These tests need the real database (PLATFORM_TEST_DATABASE_URL) with migration 000010 (outbox).
// Their events have a type no queue is bound to, so a platform relay that happens to run sends
// them nowhere.
const testEventType = "test.platform_outbox"

func (r *Repository) outboxHas(t *testing.T, eventID string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from outbox where event_id = $1", eventID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func (r *Repository) versionCount(t *testing.T, gameID string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from widget_versions where game_id = $1", gameID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// outboxCleanup deletes the events of the test's game when the test ends.
func outboxCleanup(t *testing.T, r *Repository, gameID string) {
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), "delete from outbox where payload->>'game_id' = $1", gameID)
	})
}

func TestInTxCommitsTheVersionAndItsEventTogether(t *testing.T) {
	r := testRepo(t)
	devID, gameID := newGame(t, r)
	outboxCleanup(t, r, gameID)
	ctx := context.Background()

	ev := events.New(testEventType, gameID, map[string]any{"version": 1})
	err := r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(`{"schemaVersion": 1}`)); err != nil {
			return err
		}
		return r.AddEvents(ctx, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.versionCount(t, gameID) != 1 || !r.outboxHas(t, ev.ID) {
		t.Error("the version or its event was not committed")
	}
}

func TestInTxRollsTheEventBackWithTheVersion(t *testing.T) {
	r := testRepo(t)
	devID, gameID := newGame(t, r)
	outboxCleanup(t, r, gameID)
	ctx := context.Background()

	boom := errors.New("boom")
	ev := events.New(testEventType, gameID, nil)
	err := r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(`{"schemaVersion": 1}`)); err != nil {
			return err
		}
		if err := r.AddEvents(ctx, ev); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want fn's error as it is", err)
	}
	if r.versionCount(t, gameID) != 0 || r.outboxHas(t, ev.ID) {
		t.Error("the version or its event was kept after a rollback")
	}
}

// A failed method inside InTx (an unknown game) rolls back only its savepoint and returns its error.
func TestInTxAFailedMethodLeavesTheTransactionUsable(t *testing.T) {
	r := testRepo(t)
	devID, gameID := newGame(t, r)
	outboxCleanup(t, r, gameID)
	ctx := context.Background()

	ev := events.New(testEventType, gameID, nil)
	err := r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.AddWidgetVersion(ctx, unknownID, devID, []byte(`{"schemaVersion": 1}`)); !errors.Is(err, ErrNotFound) {
			t.Errorf("AddWidgetVersion of an unknown game = %v, want ErrNotFound", err)
		}
		if _, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(`{"schemaVersion": 1}`)); err != nil {
			return err
		}
		return r.AddEvents(ctx, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.versionCount(t, gameID) != 1 || !r.outboxHas(t, ev.ID) {
		t.Error("the version or its event is missing")
	}
}

func TestAddEventsOutsideInTxFails(t *testing.T) {
	r := testRepo(t)
	_, gameID := newGame(t, r)
	if err := r.AddEvents(context.Background(), events.New(testEventType, gameID, nil)); !errors.Is(err, errNoTx) {
		t.Fatalf("err = %v, want errNoTx", err)
	}
}

func TestInTxWithoutDatabase(t *testing.T) {
	if err := New(nil).InTx(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err = %v, want ErrNoDatabase", err)
	}
}

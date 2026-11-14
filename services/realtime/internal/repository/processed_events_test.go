package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// These tests need the real database (REALTIME_TEST_DATABASE_URL) with migration 000005.

func TestRunOnceRunsAnEventOnceAndCommitsItsWrites(t *testing.T) {
	r, cleanup := testRepo(t)
	game, event := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	var dm DM
	runs := 0
	fn := func(ctx context.Context) error {
		runs++
		var err error
		dm, err = r.UpsertDM(ctx, game, testKey, memberA, memberB) // its own Begin becomes a savepoint
		return err
	}
	ran, err := r.RunOnce(ctx, game, event, fn)
	if err != nil || !ran {
		t.Fatalf("first RunOnce = %v, %v; want ran", ran, err)
	}
	if n := r.countMembers(t, game, dm.ConversationID); n != 2 {
		t.Fatalf("%d members after commit, want 2", n)
	}

	ran, err = r.RunOnce(ctx, game, event, fn)
	if err != nil || ran {
		t.Fatalf("copy = %v, %v; want skipped", ran, err)
	}
	if runs != 1 {
		t.Errorf("fn ran %d times, want 1", runs)
	}

	// The same event ID in another game is another event.
	other := newUUID(t)
	defer cleanup(other)
	if ran, err := r.RunOnce(ctx, other, event, func(context.Context) error { return nil }); err != nil || !ran {
		t.Errorf("same event ID in another game = %v, %v; want ran", ran, err)
	}
}

func TestRunOnceKeepsNothingWhenTheChangeFails(t *testing.T) {
	r, cleanup := testRepo(t)
	game, event := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	boom := errors.New("boom")
	var dm DM
	_, err := r.RunOnce(ctx, game, event, func(ctx context.Context) error {
		var err error
		if dm, err = r.UpsertDM(ctx, game, testKey, memberA, memberB); err != nil {
			return err
		}
		return boom // fails after a write
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want fn's error", err)
	}
	if n := r.countMembers(t, game, dm.ConversationID); n != 0 {
		t.Errorf("%d members kept after the failure, want the write rolled back", n)
	}

	// The event was not marked handled, so it runs again.
	ran, err := r.RunOnce(ctx, game, event, func(ctx context.Context) error {
		_, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
		return err
	})
	if err != nil || !ran {
		t.Fatalf("retry = %v, %v; want ran", ran, err)
	}
}

func TestRunOnceTwoCopiesAtTheSameTimeRunOnce(t *testing.T) {
	r, cleanup := testRepo(t)
	game, event := newUUID(t), newUUID(t)
	defer cleanup(game)

	var mu sync.Mutex
	runs := 0
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := r.RunOnce(context.Background(), game, event, func(context.Context) error {
				mu.Lock()
				runs++
				mu.Unlock()
				time.Sleep(50 * time.Millisecond) // hold the first transaction open while the copy arrives
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if runs != 1 {
		t.Errorf("fn ran %d times, want 1", runs)
	}
}

func TestDeleteProcessedEventsDeletesOnlyOldRecords(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	old, recent := newUUID(t), newUUID(t)
	for _, e := range []string{old, recent} {
		if _, err := r.RunOnce(ctx, game, e, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.pool.Exec(ctx, "update processed_events set handled_at = now() - interval '10 days' where game_id = $1 and event_id = $2", game, old); err != nil {
		t.Fatal(err)
	}

	if _, err := r.DeleteProcessedEvents(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ran, err := r.RunOnce(ctx, game, old, func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Errorf("old event after the cleanup = %v, %v; want handled again (its record is gone)", ran, err)
	}
	ran, err = r.RunOnce(ctx, game, recent, func(context.Context) error { return nil })
	if err != nil || ran {
		t.Errorf("recent event after the cleanup = %v, %v; want still skipped", ran, err)
	}
}

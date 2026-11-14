package repository

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// These tests need the real database (SOCIAL_TEST_DATABASE_URL) with migration 000008 (outbox).
// Their events have a type no queue is bound to, so a social relay that happens to run sends
// them nowhere.
const testEventType = "test.social_outbox"

// outboxHas reports whether the outbox holds the event.
func (r *Repository) outboxHas(t *testing.T, eventID string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from outbox where event_id = $1", eventID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func (r *Repository) clanExists(t *testing.T, gameID, name string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from clans where game_id = $1 and name = $2", gameID, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestInTxCommitsTheChangeAndItsEventTogether(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	owner, err := r.UpsertPlayer(ctx, game, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}

	ev := events.New(testEventType, game, map[string]any{"k": "v"})
	err = r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.CreateClan(ctx, game, owner.ID, NewClan{Name: "Wolves"}); err != nil {
			return err
		}
		return r.AddEvents(ctx, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.clanExists(t, game, "Wolves") || !r.outboxHas(t, ev.ID) {
		t.Errorf("clan %v, event %v; want both committed", r.clanExists(t, game, "Wolves"), r.outboxHas(t, ev.ID))
	}
}

func TestInTxRollsTheEventBackWithTheChange(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	owner, err := r.UpsertPlayer(ctx, game, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	ev := events.New(testEventType, game, nil)
	err = r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.CreateClan(ctx, game, owner.ID, NewClan{Name: "Wolves"}); err != nil {
			return err
		}
		if err := r.AddEvents(ctx, ev); err != nil {
			return err
		}
		return boom // something after the event fails
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want fn's error as it is", err)
	}
	if r.clanExists(t, game, "Wolves") || r.outboxHas(t, ev.ID) {
		t.Error("the clan or its event was kept after a rollback")
	}
}

// A repository method that fails inside InTx (here on a unique rule) rolls back only its own
// savepoint: its error comes back as before and the transaction can go on.
func TestInTxAFailedMethodLeavesTheTransactionUsable(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, err := r.UpsertPlayer(ctx, game, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	aysel, err := r.UpsertPlayer(ctx, game, "u_2", "Aysel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateClan(ctx, game, ali.ID, NewClan{Name: "Wolves"}); err != nil {
		t.Fatal(err)
	}

	ev := events.New(testEventType, game, nil)
	err = r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.CreateClan(ctx, game, aysel.ID, NewClan{Name: "wolves"}); !errors.Is(err, ErrClanNameTaken) {
			t.Errorf("CreateClan = %v, want ErrClanNameTaken", err)
		}
		if _, err := r.CreateClan(ctx, game, aysel.ID, NewClan{Name: "Hawks"}); err != nil {
			return err
		}
		return r.AddEvents(ctx, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.clanExists(t, game, "Hawks") || !r.outboxHas(t, ev.ID) {
		t.Error("the second clan or its event is missing")
	}
}

func TestAddEventsOutsideInTxFails(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ev := events.New(testEventType, game, nil)
	if err := r.AddEvents(context.Background(), ev); !errors.Is(err, errNoTx) {
		t.Fatalf("err = %v, want errNoTx", err)
	}
	if r.outboxHas(t, ev.ID) {
		t.Error("an event was written outside a transaction")
	}
}

func TestInTxWithoutDatabase(t *testing.T) {
	err := New(nil).InTx(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err = %v, want ErrNoDatabase", err)
	}
}

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// These tests need the real database (PLATFORM_TEST_DATABASE_URL) with migrations 000007 to 000009.

// auditCount returns how many audit rows exist for target.
func auditCount(t *testing.T, r *Repository, target string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `select count(*) from admin_audit_log where target_id = $1`, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func suspend(reason, note string) StatusChange {
	return StatusChange{Actor: "admin-1", Action: "developer.suspended", Status: StatusSuspended, Reason: reason, Note: note}
}

func TestSetDeveloperStatusSuspendsAndAudits(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 1)
	dev := ids[0]

	d, changed, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", "Invoice 1042"))
	if err != nil || !changed {
		t.Fatalf("suspend: changed=%v, err=%v", changed, err)
	}
	if d.ID != dev || d.Status != "suspended" || d.StatusReason != "non_payment" || d.StatusChangedAt == nil {
		t.Fatalf("returned developer %+v", d)
	}
	stored, err := r.AdminDeveloperByID(ctx, dev)
	if err != nil || stored.Status != "suspended" || stored.StatusReason != "non_payment" || stored.StatusChangedAt == nil {
		t.Fatalf("stored developer %+v, %v", stored, err)
	}

	entries, err := r.AuditEntries(ctx, AuditFilter{TargetID: dev, Limit: 10})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries %+v, %v", entries, err)
	}
	e := entries[0]
	if e.Actor != "admin-1" || e.Action != "developer.suspended" || e.TargetType != "developer" || e.TargetID != dev ||
		e.Reason != "non_payment" || e.Note != "Invoice 1042" {
		t.Fatalf("audit entry %+v", e)
	}
	if !e.CreatedAt.Equal(*stored.StatusChangedAt) {
		t.Errorf("audit time %s and status_changed_at %s should be the same instant", e.CreatedAt, stored.StatusChangedAt)
	}
}

func TestSetDeveloperStatusSameStatusChangesNothing(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 1)
	dev := ids[0]

	// Already active: resuming is a no-op.
	d, changed, err := r.SetDeveloperStatus(ctx, dev, StatusChange{Actor: "a", Action: "developer.resumed", Status: StatusActive})
	if err != nil || changed || d.Status != "active" || d.StatusChangedAt != nil {
		t.Fatalf("resume of an active developer: %+v, changed=%v, err=%v", d, changed, err)
	}
	if n := auditCount(t, r, dev); n != 0 {
		t.Fatalf("%d audit rows after a no-op", n)
	}

	first, _, err := r.SetDeveloperStatus(ctx, dev, suspend("abuse", ""))
	if err != nil {
		t.Fatal(err)
	}
	// Suspending again, with another reason and note, must not touch anything.
	again, changed, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", "second try"))
	if err != nil || changed {
		t.Fatalf("second suspend: changed=%v, err=%v", changed, err)
	}
	if again.StatusReason != "abuse" || !again.StatusChangedAt.Equal(*first.StatusChangedAt) {
		t.Fatalf("second suspend altered the row: %+v, first %+v", again, first)
	}
	if n := auditCount(t, r, dev); n != 1 {
		t.Fatalf("%d audit rows, want 1", n)
	}
}

func TestSetDeveloperStatusResume(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 1)
	dev := ids[0]
	if _, _, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", "")); err != nil {
		t.Fatal(err)
	}

	// Without a reason the audit row records the suspension that ends.
	d, changed, err := r.SetDeveloperStatus(ctx, dev, StatusChange{Actor: "admin-2", Action: "developer.resumed", Status: StatusActive, Note: "paid"})
	if err != nil || !changed {
		t.Fatalf("resume: changed=%v, err=%v", changed, err)
	}
	if d.Status != "active" || d.StatusReason != "" || d.StatusChangedAt == nil {
		t.Fatalf("resumed developer %+v: the reason must be cleared and the time kept", d)
	}
	entries, err := r.AuditEntries(ctx, AuditFilter{TargetID: dev, Limit: 10})
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit entries %+v, %v", entries, err)
	}
	if e := entries[0]; e.Action != "developer.resumed" || e.Actor != "admin-2" || e.Reason != "non_payment" || e.Note != "paid" {
		t.Errorf("resume entry %+v", e)
	}

	// An explicit reason on resume wins.
	if _, _, err := r.SetDeveloperStatus(ctx, dev, suspend("abuse", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.SetDeveloperStatus(ctx, dev, StatusChange{Actor: "a", Action: "developer.resumed", Status: StatusActive, Reason: "other"}); err != nil {
		t.Fatal(err)
	}
	entries, _ = r.AuditEntries(ctx, AuditFilter{TargetID: dev, Limit: 10})
	if len(entries) != 4 || entries[0].Reason != "other" {
		t.Fatalf("entries %+v", entries)
	}
}

func TestSetStatusIsAllOrNothing(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	// A NUL byte is not valid in Postgres text, so the audit insert fails after the update ran.
	bad := StatusChange{Actor: "bad\x00actor", Action: "developer.suspended", Status: StatusSuspended, Reason: "abuse"}
	if _, _, err := r.SetDeveloperStatus(ctx, dev, bad); err == nil {
		t.Fatal("want an error from the failing audit insert")
	}
	if d, err := r.AdminDeveloperByID(ctx, dev); err != nil || d.Status != "active" || d.StatusChangedAt != nil {
		t.Fatalf("developer after a failed change: %+v, %v (the status update must have been rolled back)", d, err)
	}

	bad.Action = "game.suspended"
	if _, _, err := r.SetGameStatus(ctx, game, bad); err == nil {
		t.Fatal("want an error from the failing audit insert")
	}
	games, err := r.AdminGamesByDeveloper(ctx, dev)
	if err != nil || len(games) != 1 || games[0].Status != "active" || games[0].StatusChangedAt != nil {
		t.Fatalf("game after a failed change: %+v, %v", games, err)
	}
	if n := auditCount(t, r, dev) + auditCount(t, r, game); n != 0 {
		t.Fatalf("%d audit rows after failed changes", n)
	}
}

func TestSetStatusUnknownTarget(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if _, _, err := r.SetDeveloperStatus(ctx, unknownID, suspend("abuse", "")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown developer: %v, want ErrNotFound", err)
	}
	if _, _, err := r.SetGameStatus(ctx, unknownID, suspend("abuse", "")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown game: %v, want ErrNotFound", err)
	}
	if n := auditCount(t, r, unknownID); n != 0 {
		t.Errorf("%d audit rows for an unknown target", n)
	}
}

func TestSetGameStatusLeavesTheDeveloperAndOtherGamesAlone(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)
	other, err := r.CreateGame(ctx, dev, "Other game", "")
	if err != nil {
		t.Fatal(err)
	}
	// Runs before newGame's cleanup (last in, first out): the extra game must go before its developer can.
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), `delete from admin_audit_log where target_id = $1`, game)
		_, _ = r.pool.Exec(context.Background(), `delete from games where id = $1`, other.ID)
	})

	c := StatusChange{Actor: "admin-1", Action: "game.suspended", Status: StatusSuspended, Reason: "abuse", Note: "reported"}
	g, changed, err := r.SetGameStatus(ctx, game, c)
	if err != nil || !changed || g.ID != game || g.Status != "suspended" || g.StatusReason != "abuse" || g.StatusChangedAt == nil {
		t.Fatalf("suspend game: %+v, changed=%v, err=%v", g, changed, err)
	}
	if _, changed, err := r.SetGameStatus(ctx, game, c); err != nil || changed {
		t.Fatalf("second suspend: changed=%v, err=%v", changed, err)
	}
	if n := auditCount(t, r, game); n != 1 {
		t.Fatalf("%d audit rows, want 1", n)
	}
	entries, err := r.AuditEntries(ctx, AuditFilter{TargetID: game, Limit: 10})
	if err != nil || len(entries) != 1 || entries[0].TargetType != "game" || entries[0].Action != "game.suspended" || entries[0].Note != "reported" {
		t.Fatalf("audit entries %+v, %v", entries, err)
	}

	if d, err := r.AdminDeveloperByID(ctx, dev); err != nil || d.Status != "active" {
		t.Errorf("developer %+v, %v: suspending a game must not suspend its developer", d, err)
	}
	games, err := r.AdminGamesByDeveloper(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range games {
		if x.ID == other.ID && x.Status != "active" {
			t.Errorf("the developer's other game is %s", x.Status)
		}
	}

	resumed, changed, err := r.SetGameStatus(ctx, game, StatusChange{Actor: "admin-1", Action: "game.resumed", Status: StatusActive})
	if err != nil || !changed || resumed.Status != "active" || resumed.StatusReason != "" {
		t.Fatalf("resume game: %+v, changed=%v, err=%v", resumed, changed, err)
	}
}

func TestSetDeveloperStatusDoesNotTouchTheirGames(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, _ := newGame(t, r)
	if _, _, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", "")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), `delete from admin_audit_log where target_id = $1`, dev)
	})
	games, err := r.AdminGamesByDeveloper(ctx, dev)
	if err != nil || len(games) != 1 || games[0].Status != "active" {
		t.Fatalf("games %+v, %v: a developer's switch must not rewrite game rows", games, err)
	}
}

func TestSetDeveloperStatusConcurrentRequestsWriteOneAuditRow(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 1)
	dev := ids[0]

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", ""))
			errs <- err
			results <- changed
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changes := 0
	for changed := range results {
		if changed {
			changes++
		}
	}
	if changes != 1 {
		t.Errorf("%d requests reported a change, want exactly 1", changes)
	}
	if n := auditCount(t, r, dev); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
}

func TestGameAccessOf(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), `delete from admin_audit_log where target_id = any($1::uuid[])`, []string{dev, game})
	})

	a, err := r.GameAccessOf(ctx, game)
	if err != nil || a != (GameAccess{GameStatus: "active", DeveloperStatus: "active"}) {
		t.Fatalf("fresh game: %+v, %v", a, err)
	}

	if _, _, err := r.SetGameStatus(ctx, game, StatusChange{Actor: "a", Action: "game.suspended", Status: StatusSuspended, Reason: "abuse"}); err != nil {
		t.Fatal(err)
	}
	if a, err = r.GameAccessOf(ctx, game); err != nil || a != (GameAccess{GameStatus: "suspended", DeveloperStatus: "active"}) {
		t.Fatalf("game suspended: %+v, %v", a, err)
	}

	if _, _, err := r.SetDeveloperStatus(ctx, dev, suspend("non_payment", "")); err != nil {
		t.Fatal(err)
	}
	if a, err = r.GameAccessOf(ctx, game); err != nil || a != (GameAccess{GameStatus: "suspended", DeveloperStatus: "suspended"}) {
		t.Fatalf("both suspended: %+v, %v", a, err)
	}

	// Resuming shows at once: the query reads the rows, nothing is cached.
	if _, _, err := r.SetGameStatus(ctx, game, StatusChange{Actor: "a", Action: "game.resumed", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.SetDeveloperStatus(ctx, dev, StatusChange{Actor: "a", Action: "developer.resumed", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	if a, err = r.GameAccessOf(ctx, game); err != nil || a != (GameAccess{GameStatus: "active", DeveloperStatus: "active"}) {
		t.Fatalf("resumed: %+v, %v", a, err)
	}

	if _, err := r.GameAccessOf(ctx, unknownID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown game: %v, want ErrNotFound", err)
	}
}

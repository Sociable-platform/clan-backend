package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// These tests need the real database like widget_test.go (PLATFORM_TEST_DATABASE_URL), with
// migrations 000007 to 000009 applied. Every test works on rows with its own random tag in the
// email, so other data in the database does not disturb it.

// tagged creates n developers whose email contains a fresh tag, with created_at one minute apart
// (the first is the oldest), and returns the tag and the IDs oldest first.
func tagged(t *testing.T, r *Repository, n int) (tag string, ids []string) {
	t.Helper()
	ctx := context.Background()
	tag = fmt.Sprintf("admintest%d", time.Now().UnixNano())
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for i := 0; i < n; i++ {
		d, err := r.CreateDeveloper(ctx, fmt.Sprintf("%s-%d@example.com", tag, i), "hash", fmt.Sprintf("Tester %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.pool.Exec(ctx, `update developers set created_at = $2 where id = $1`, d.ID, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = r.pool.Exec(c, `delete from admin_audit_log where target_id = any($1::uuid[])`, ids)
		_, _ = r.pool.Exec(c, `delete from games where developer_id = any($1::uuid[])`, ids)
		_, _ = r.pool.Exec(c, `delete from developers where id = any($1::uuid[])`, ids)
	})
	return tag, ids
}

func TestListDevelopersPagesNewestFirstWithoutGapsOrRepeats(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tag, ids := tagged(t, r, 5)

	var seen []string
	var after *Cursor
	for page := 0; page < 10; page++ {
		rows, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, After: after, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range rows {
			seen = append(seen, d.ID)
		}
		if len(rows) < 3 {
			break
		}
		last := rows[len(rows)-1]
		after = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d developers, want 5: %v", len(seen), seen)
	}
	for i, id := range seen {
		if want := ids[len(ids)-1-i]; id != want { // newest first
			t.Fatalf("position %d is %s, want %s", i, id, want)
		}
	}
}

func TestListDevelopersPagesThroughEqualCreatedTimes(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tag, ids := tagged(t, r, 4)
	// All four share one created_at, so only the ID can tell them apart.
	if _, err := r.pool.Exec(ctx, `update developers set created_at = '2026-01-01T00:00:00Z' where id = any($1::uuid[])`, ids); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	var after *Cursor
	for page := 0; page < 4; page++ {
		rows, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("page %d has %d rows", page, len(rows))
		}
		if seen[rows[0].ID] {
			t.Fatalf("%s came twice", rows[0].ID)
		}
		seen[rows[0].ID] = true
		after = &Cursor{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}
	}
	if rows, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, After: after, Limit: 1}); err != nil || len(rows) != 0 {
		t.Fatalf("after the last row: %v, %v", rows, err)
	}
}

func TestListDevelopersFilters(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tag, ids := tagged(t, r, 3)
	if _, err := r.pool.Exec(ctx, `update developers set status = 'suspended' where id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.CreateGame(ctx, ids[2], fmt.Sprintf("Game %d", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	suspended, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, Status: "suspended", Limit: 10})
	if err != nil || len(suspended) != 1 || suspended[0].ID != ids[1] || suspended[0].Status != "suspended" {
		t.Fatalf("suspended filter: %+v, %v", suspended, err)
	}
	active, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, Status: "active", Limit: 10})
	if err != nil || len(active) != 2 {
		t.Fatalf("active filter: %+v, %v", active, err)
	}

	all, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, Limit: 10})
	if err != nil || len(all) != 3 {
		t.Fatalf("no status filter: %+v, %v", all, err)
	}
	counts := map[string]int{}
	for _, d := range all {
		counts[d.ID] = d.GameCount
	}
	if counts[ids[2]] != 2 || counts[ids[0]] != 0 {
		t.Errorf("game counts %v, want 2 for %s and 0 for %s", counts, ids[2], ids[0])
	}

	// The search matches the name too, and ignores case.
	byName, err := r.ListDevelopers(ctx, DeveloperFilter{Query: "TESTER 1", Status: "suspended", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range byName {
		found = found || d.ID == ids[1]
	}
	if !found {
		t.Errorf("search by name did not find %s: %+v", ids[1], byName)
	}
}

func TestListDevelopersSearchTreatsWildcardsAsText(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tag, _ := tagged(t, r, 2)

	// "tag%" would match both developers if % were a wildcard, "tag_" too (the next character is "-").
	for _, q := range []string{tag + "%", tag + "_", tag + `\`} {
		rows, err := r.ListDevelopers(ctx, DeveloperFilter{Query: q, Limit: 100})
		if err != nil {
			t.Fatalf("q=%q: %v", q, err)
		}
		if len(rows) != 0 {
			t.Errorf("q=%q matched %d developers, but no email contains that text", q, len(rows))
		}
	}
	if all, err := r.ListDevelopers(ctx, DeveloperFilter{Query: tag, Limit: 100}); err != nil || len(all) != 2 {
		t.Fatalf("plain tag: %v, %v", all, err)
	}
}

func TestAdminDeveloperAndGames(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 1)
	dev := ids[0]
	g1, err := r.CreateGame(ctx, dev, "First", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateGame(ctx, dev, "Second", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `update games set status = 'suspended', status_reason = 'abuse', status_changed_at = now() where id = $1`, g1.ID); err != nil {
		t.Fatal(err)
	}

	d, err := r.AdminDeveloperByID(ctx, dev)
	if err != nil || d.ID != dev || d.Status != "active" || d.StatusReason != "" || d.StatusChangedAt != nil {
		t.Fatalf("developer %+v, %v", d, err)
	}
	if _, err := r.pool.Exec(ctx, `update developers set status = 'suspended', status_reason = 'non_payment', status_changed_at = now() where id = $1`, dev); err != nil {
		t.Fatal(err)
	}
	if d, err = r.AdminDeveloperByID(ctx, dev); err != nil || d.Status != "suspended" || d.StatusReason != "non_payment" || d.StatusChangedAt == nil {
		t.Fatalf("suspended developer %+v, %v", d, err)
	}

	games, err := r.AdminGamesByDeveloper(ctx, dev)
	if err != nil || len(games) != 2 {
		t.Fatalf("games %+v, %v", games, err)
	}
	byName := map[string]AdminGame{games[0].Name: games[0], games[1].Name: games[1]}
	if g := byName["First"]; g.Status != "suspended" || g.StatusReason != "abuse" || g.StatusChangedAt == nil || g.Description != "desc" {
		t.Errorf("first game %+v", g)
	}
	if g := byName["Second"]; g.Status != "active" || g.StatusReason != "" || g.StatusChangedAt != nil {
		t.Errorf("second game %+v", g)
	}

	if _, err := r.AdminDeveloperByID(ctx, unknownID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown developer: %v, want ErrNotFound", err)
	}
	if none, err := r.AdminGamesByDeveloper(ctx, unknownID); err != nil || none == nil || len(none) != 0 {
		t.Errorf("unknown developer's games: %v, %v (want empty, non-nil)", none, err)
	}
}

func TestAuditEntriesNewestFirstFilteredAndPaged(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, ids := tagged(t, r, 2)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	insert := func(target, action, note string, offset time.Duration) {
		t.Helper()
		_, err := r.pool.Exec(ctx,
			`insert into admin_audit_log (id, actor, action, target_type, target_id, reason, note, created_at)
			 values (gen_random_uuid(), 'a1', $1, 'developer', $2, 'non_payment', nullif($3, ''), $4)`,
			action, target, note, base.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(ids[0], "developer.suspended", "unpaid", 0)
	insert(ids[0], "developer.resumed", "", time.Minute)
	insert(ids[1], "developer.suspended", "other developer", 2*time.Minute)
	insert(ids[0], "developer.suspended", "again", 3*time.Minute)

	rows, err := r.AuditEntries(ctx, AuditFilter{TargetID: ids[0], Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("filtered: %+v, %v", rows, err)
	}
	if rows[0].Note != "again" || rows[1].Action != "developer.resumed" || rows[1].Note != "" || rows[2].Note != "unpaid" {
		t.Errorf("order or mapping wrong: %+v", rows)
	}
	if rows[0].TargetID != ids[0] || rows[0].TargetType != "developer" || rows[0].Actor != "a1" || rows[0].Reason != "non_payment" {
		t.Errorf("fields %+v", rows[0])
	}

	page1, err := r.AuditEntries(ctx, AuditFilter{TargetID: ids[0], Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page 1: %+v, %v", page1, err)
	}
	last := page1[1]
	page2, err := r.AuditEntries(ctx, AuditFilter{TargetID: ids[0], After: &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, Limit: 2})
	if err != nil || len(page2) != 1 || page2[0].Note != "unpaid" {
		t.Fatalf("page 2: %+v, %v", page2, err)
	}

	// Without a target, entries of every developer come back; at least our four.
	everything, err := r.AuditEntries(ctx, AuditFilter{Limit: 1000})
	if err != nil || len(everything) < 4 {
		t.Fatalf("unfiltered: %d entries, %v", len(everything), err)
	}
}

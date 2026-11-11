package backfill

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

const testGame = "a6c1e2f0-1111-4222-8333-944455556666"

// pager serves friendships from a list, pageSize at a time, with cursors "1", "2", ...
// failures is how many calls fail with ErrUnavailable before it answers.
type pager struct {
	all      []social.Friendship
	failures int
	err      error // returned instead of ErrUnavailable while failures > 0
	calls    int
	afters   []string
	limits   []int
	cursor   func(next int) string // nil: the next page number as text
}

func (p *pager) Friendships(_ context.Context, after string, limit int) (social.FriendshipPage, error) {
	p.calls++
	p.afters = append(p.afters, after)
	p.limits = append(p.limits, limit)
	if p.failures > 0 {
		p.failures--
		if p.err != nil {
			return social.FriendshipPage{}, p.err
		}
		return social.FriendshipPage{}, fmt.Errorf("call social: %w", social.ErrUnavailable)
	}
	start := 0
	if after != "" {
		fmt.Sscanf(after, "%d", &start)
	}
	end := start + limit
	page := social.FriendshipPage{}
	if end < len(p.all) {
		page.NextCursor = fmt.Sprint(end)
		if p.cursor != nil {
			page.NextCursor = p.cursor(end)
		}
	} else {
		end = len(p.all)
	}
	page.Friendships = p.all[start:end]
	return page, nil
}

// storeRec records what was stored; fail returns an error for friendships it picks.
type storeRec struct {
	got  []string // "game a b"
	fail func(gameID string, a service.Player) error
}

func (s *storeRec) SyncFriendship(_ context.Context, gameID string, a, b service.Player) error {
	if s.fail != nil {
		if err := s.fail(gameID, a); err != nil {
			return err
		}
	}
	s.got = append(s.got, gameID+" "+a.ID+" "+b.ID)
	return nil
}

func friendships(n int) []social.Friendship {
	out := make([]social.Friendship, n)
	for i := range out {
		out[i] = social.Friendship{
			GameID: testGame,
			A:      social.Player{ID: fmt.Sprintf("a%07d-0000-4000-8000-000000000000", i), DisplayName: "A"},
			B:      social.Player{ID: fmt.Sprintf("b%07d-0000-4000-8000-000000000000", i), DisplayName: "B"},
		}
	}
	return out
}

// noWait records the waits instead of sleeping.
type noWait struct{ waits []time.Duration }

func (n *noWait) wait(_ context.Context, d time.Duration) error {
	n.waits = append(n.waits, d)
	return nil
}

func options(nw *noWait, pageSize int) Options {
	return Options{PageSize: pageSize, wait: nw.wait}
}

func TestRunStoresEveryFriendshipOnEveryPage(t *testing.T) {
	for _, total := range []int{0, 1, 4, 5, 6, 12} {
		t.Run(fmt.Sprintf("%d friendships", total), func(t *testing.T) {
			src, st := &pager{all: friendships(total)}, &storeRec{}
			res, err := Run(context.Background(), src, st, options(&noWait{}, 5))
			if err != nil {
				t.Fatal(err)
			}
			if res.Synced != total || res.Skipped != 0 || len(st.got) != total {
				t.Errorf("result %+v, stored %d; want %d synced", res, len(st.got), total)
			}
			// the first page has no cursor; every other page continues from the last answer
			if src.afters[0] != "" {
				t.Errorf("first page asked after %q, want none", src.afters[0])
			}
			for _, l := range src.limits {
				if l != 5 {
					t.Errorf("asked for %d, want the page size 5", l)
				}
			}
			// Social gives a cursor only when more follows, so a full last page is not followed by an empty one.
			wantPages := (total + 4) / 5
			if wantPages == 0 {
				wantPages = 1 // an empty list is still one (empty) page
			}
			if res.Pages != wantPages {
				t.Errorf("pages = %d, want %d", res.Pages, wantPages)
			}
		})
	}
}

func TestRunPassesGameAndNames(t *testing.T) {
	src := &pager{all: []social.Friendship{{
		GameID: testGame,
		A:      social.Player{ID: "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69", DisplayName: "Aysel"},
		B:      social.Player{ID: "8f1d2c3b-4a5e-4b6c-8d7e-9f0a1b2c3d4e", DisplayName: "Ali"},
	}}}
	var gotA, gotB service.Player
	st := &recording{fn: func(g string, a, b service.Player) { gotA, gotB = a, b }}
	if _, err := Run(context.Background(), src, st, options(&noWait{}, 10)); err != nil {
		t.Fatal(err)
	}
	if gotA != (service.Player{ID: "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69", Name: "Aysel"}) || gotB.Name != "Ali" {
		t.Errorf("stored %+v and %+v", gotA, gotB)
	}
}

type recording struct {
	fn func(gameID string, a, b service.Player)
}

func (r *recording) SyncFriendship(_ context.Context, gameID string, a, b service.Player) error {
	r.fn(gameID, a, b)
	return nil
}

func TestRunSkipsInvalidFriendshipsAndKeepsGoing(t *testing.T) {
	all := friendships(4)
	bad := all[1].A.ID
	st := &storeRec{fail: func(_ string, a service.Player) error {
		if a.ID == bad {
			return fmt.Errorf("%w: a.id is not a UUID", service.ErrInvalidEvent)
		}
		return nil
	}}
	res, err := Run(context.Background(), &pager{all: all}, st, options(&noWait{}, 2))
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced != 3 || res.Skipped != 1 {
		t.Errorf("result %+v, want 3 synced and 1 skipped", res)
	}
}

func TestRunStopsOnDatabaseError(t *testing.T) {
	errDB := errors.New("db down")
	st := &storeRec{fail: func(string, service.Player) error { return errDB }}
	src := &pager{all: friendships(10)}
	res, err := Run(context.Background(), src, st, options(&noWait{}, 5))
	if !errors.Is(err, errDB) {
		t.Fatalf("err = %v, want the database error", err)
	}
	if src.calls != 1 || res.Synced != 0 {
		t.Errorf("calls %d, synced %d: Run went on after a database error", src.calls, res.Synced)
	}
}

func TestRunTriesAgainWhileSocialIsDown(t *testing.T) {
	src := &pager{all: friendships(3), failures: 2}
	nw := &noWait{}
	res, err := Run(context.Background(), src, &storeRec{}, Options{PageSize: 10, Backoff: time.Second, wait: nw.wait})
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced != 3 || src.calls != 3 {
		t.Errorf("synced %d after %d calls, want 3 after 3", res.Synced, src.calls)
	}
	if len(nw.waits) != 2 || nw.waits[0] != time.Second || nw.waits[1] != 2*time.Second {
		t.Errorf("waits = %v, want 1s then 2s", nw.waits)
	}
}

func TestRunGivesUpAfterTheAttempts(t *testing.T) {
	src := &pager{all: friendships(3), failures: 100}
	nw := &noWait{}
	_, err := Run(context.Background(), src, &storeRec{}, Options{PageSize: 10, Attempts: 4, Backoff: 5 * time.Second, wait: nw.wait})
	if !errors.Is(err, social.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if src.calls != 4 {
		t.Errorf("calls = %d, want 4", src.calls)
	}
	// 5 s, then 8 s twice: the wait is capped
	if want := []time.Duration{5 * time.Second, 8 * time.Second, 8 * time.Second}; fmt.Sprint(nw.waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", nw.waits, want)
	}
}

func TestRunDoesNotRetryASetupMistake(t *testing.T) {
	src := &pager{failures: 1, err: social.ErrNoInternalToken}
	_, err := Run(context.Background(), src, &storeRec{}, options(&noWait{}, 10))
	if !errors.Is(err, social.ErrNoInternalToken) || src.calls != 1 {
		t.Errorf("err = %v after %d calls, want ErrNoInternalToken after 1", err, src.calls)
	}
}

func TestRunStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &pager{failures: 100}
	waits := 0
	opts := Options{PageSize: 10, wait: func(ctx context.Context, _ time.Duration) error {
		waits++
		cancel()
		return ctx.Err()
	}}
	_, err := Run(ctx, src, &storeRec{}, opts)
	if !errors.Is(err, context.Canceled) || waits != 1 {
		t.Errorf("err = %v after %d waits, want context.Canceled after 1", err, waits)
	}
}

func TestRunRefusesACursorThatDoesNotAdvance(t *testing.T) {
	src := &pager{all: friendships(10), cursor: func(int) string { return "stuck" }}
	_, err := Run(context.Background(), src, &storeRec{}, options(&noWait{}, 3))
	if err == nil {
		t.Fatal("Run ended without an error although the cursor never advanced")
	}
	if src.calls > 3 {
		t.Errorf("calls = %d, the loop should have been stopped after the repeat", src.calls)
	}
}

func TestSleepStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleep(ctx, time.Minute); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("sleep = %v after %v, want context.Canceled at once", err, time.Since(start))
	}
}

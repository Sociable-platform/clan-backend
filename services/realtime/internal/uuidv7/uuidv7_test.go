package uuidv7

import (
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"
)

var v7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestNewIsAV7UUIDWithTheTimeInside(t *testing.T) {
	at := time.Date(2026, 11, 10, 8, 30, 15, 123_456_789, time.UTC)
	id, got := New(fixed(at)).New()
	if !v7.MatchString(id) {
		t.Fatalf("%q is not a UUIDv7", id)
	}
	if want := at.Truncate(time.Millisecond); !got.Equal(want) {
		t.Errorf("time = %v, want %v", got, want)
	}
	// The first 48 bits are the milliseconds since the epoch.
	var ms int64
	for _, c := range id[:8] + id[9:13] {
		ms = ms<<4 | int64(hexVal(c))
	}
	if ms != at.UnixMilli() {
		t.Errorf("timestamp bits = %d, want %d", ms, at.UnixMilli())
	}
}

func hexVal(c rune) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}

// Many IDs in one millisecond must still sort in the order they were made.
func TestIDsFromOneMillisecondSortInOrder(t *testing.T) {
	g := New(fixed(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)))
	var ids []string
	for i := 0; i < 10000; i++ { // more than the 4096 counter values: the time must move on
		id, _ := g.New()
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("IDs made later sort earlier")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

func TestIDsKeepTheirOrderWhenTheClockGoesBack(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 5, 0, time.UTC)
	g := New(func() time.Time { return now })
	first, _ := g.New()
	now = now.Add(-3 * time.Second) // clock stepped back
	second, at := g.New()
	if second <= first {
		t.Errorf("after the clock went back %s sorts before %s", second, first)
	}
	if !at.After(time.Date(2026, 11, 10, 8, 0, 4, 0, time.UTC)) {
		t.Errorf("time = %v, want it not to follow the clock back", at)
	}
}

func TestIDsFromLaterMillisecondsSortLater(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	g := New(func() time.Time { return now })
	var ids []string
	for i := 0; i < 50; i++ {
		id, _ := g.New()
		ids = append(ids, id)
		now = now.Add(time.Millisecond)
	}
	if !sort.StringsAreSorted(ids) {
		t.Error("IDs of later milliseconds do not sort later")
	}
}

func TestConcurrentUseGivesUniqueIDs(t *testing.T) {
	g := New(time.Now)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Go(func() {
			for i := 0; i < 2000; i++ {
				id, _ := g.New()
				mu.Lock()
				if seen[id] {
					t.Errorf("duplicate %s", id)
				}
				seen[id] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
}

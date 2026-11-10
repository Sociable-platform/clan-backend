package gameconfig

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// fakeSource answers from a map of game ID -> version, counts its calls, and can be told to fail or wait.
type fakeSource struct {
	mu       sync.Mutex
	versions map[string]int
	err      error
	calls    map[string]int
	gate     chan struct{} // when set, Fetch waits for it to be closed

	gateFirstOnly bool // the gate holds back only the first request for a game
}

func newFake(versions map[string]int) *fakeSource {
	return &fakeSource{versions: versions, calls: map[string]int{}}
}

func (f *fakeSource) Fetch(ctx context.Context, game string) (Config, error) {
	f.mu.Lock()
	f.calls[game]++
	gate, err, v, ok := f.gate, f.err, f.versions[game], false
	_, ok = f.versions[game]
	if f.gateFirstOnly && f.calls[game] > 1 {
		gate = nil // only the first request waits
	}
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done(): // like a real request: it stops when its context ends
			return Config{}, ctx.Err()
		}
	}
	if err != nil {
		return Config{}, err
	}
	if !ok {
		return Config{}, ErrNotPublished
	}
	return Config{Version: v, Settings: []byte(`{"v":` + string(rune('0'+v)) + `}`)}, nil
}

func (f *fakeSource) set(game string, v int) {
	f.mu.Lock()
	f.versions[game] = v
	f.mu.Unlock()
}

func (f *fakeSource) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeSource) callCount(game string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[game]
}

const gameB = "22222222-2222-4222-8222-222222222222"

func TestGetLoadsOnceAndKeepsIt(t *testing.T) {
	src := newFake(map[string]int{gameID: 3})
	c := NewCache(src, Options{})
	for i := 0; i < 3; i++ {
		cfg, err := c.Get(context.Background(), gameID)
		if err != nil || cfg.Version != 3 {
			t.Fatalf("Get = %+v, %v", cfg, err)
		}
	}
	if n := src.callCount(gameID); n != 1 {
		t.Errorf("platform was asked %d times, want 1 (lazy, then cached)", n)
	}
}

func TestGetOfManyCallersAtOnceMakesOneRequest(t *testing.T) {
	src := newFake(map[string]int{gameID: 3})
	src.gate = make(chan struct{})
	c := NewCache(src, Options{})

	var wg sync.WaitGroup
	versions := make([]int, 20)
	for i := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			versions[i] = c.Version(context.Background(), gameID)
		}()
	}
	// let every caller reach the shared load, then let the one request answer
	deadline := time.Now().Add(2 * time.Second)
	for src.callCount(gameID) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(src.gate)
	wg.Wait()

	if n := src.callCount(gameID); n != 1 {
		t.Errorf("platform was asked %d times by 20 callers, want 1", n)
	}
	for i, v := range versions {
		if v != 3 {
			t.Errorf("caller %d got version %d, want 3", i, v)
		}
	}
}

func TestGetKeepsGamesApart(t *testing.T) {
	c := NewCache(newFake(map[string]int{gameID: 3, gameB: 9}), Options{})
	if a, b := c.Version(context.Background(), gameID), c.Version(context.Background(), gameB); a != 3 || b != 9 {
		t.Errorf("versions = %d and %d, want 3 and 9", a, b)
	}
}

func TestGetOfAGameWithoutAConfigIsEmptyNotAnError(t *testing.T) {
	src := newFake(map[string]int{})
	now := time.Now()
	c := NewCache(src, Options{FailFor: 5 * time.Second, Now: func() time.Time { return now }})
	for i := 0; i < 3; i++ {
		cfg, err := c.Get(context.Background(), gameID)
		if err != nil || cfg.Version != 0 {
			t.Fatalf("Get = %+v, %v, want an empty config and no error", cfg, err)
		}
	}
	if n := src.callCount(gameID); n != 1 {
		t.Errorf("platform asked %d times within the window, want 1", n)
	}
	// after the window, and after the game published, it is found
	src.set(gameID, 1)
	now = now.Add(6 * time.Second)
	if v := c.Version(context.Background(), gameID); v != 1 {
		t.Errorf("version after publishing = %d, want 1", v)
	}
}

func TestGetRemembersAFailureForAWhileOnly(t *testing.T) {
	src := newFake(map[string]int{gameID: 3})
	src.fail(ErrUnavailable)
	now := time.Now()
	c := NewCache(src, Options{FailFor: 5 * time.Second, Now: func() time.Time { return now }})

	for i := 0; i < 3; i++ {
		if _, err := c.Get(context.Background(), gameID); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Get error = %v, want ErrUnavailable", err)
		}
	}
	if n := src.callCount(gameID); n != 1 {
		t.Errorf("platform asked %d times while it is down, want 1 (the failure is remembered)", n)
	}
	src.fail(nil)
	now = now.Add(6 * time.Second)
	if v := c.Version(context.Background(), gameID); v != 3 {
		t.Errorf("version after Platform is back = %d, want 3", v)
	}
	if v := c.Version(context.Background(), gameID); v != 3 || src.callCount(gameID) != 2 {
		t.Errorf("not cached after the retry: version %d, calls %d", v, src.callCount(gameID))
	}
}

// A caller that stops waiting (the hello frame gives up after a second) must not lose the load for the others.
func TestALoadFinishesEvenWhenItsFirstCallerGivesUp(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	src.gate = make(chan struct{})
	c := NewCache(src, Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, gameID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get that timed out: error = %v, want ErrUnavailable", err)
	}
	close(src.gate)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v := c.Version(context.Background(), gameID); v == 4 {
			if n := src.callCount(gameID); n != 1 {
				t.Errorf("platform asked %d times, want 1: the answer of the first load is kept", n)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the version never arrived in the cache")
}

func TestVersionIsZeroWhenItIsNotKnown(t *testing.T) {
	src := newFake(map[string]int{gameID: 3})
	src.fail(ErrUnavailable)
	c := NewCache(src, Options{})
	if v := c.Version(context.Background(), gameID); v != 0 {
		t.Errorf("version = %d, want 0 while Platform is down", v)
	}
	if v := c.Version(context.Background(), "not-a-game"); v != 0 {
		t.Errorf("version of an invalid game = %d, want 0", v)
	}
}

func TestPublishedSwapsInTheNewConfig(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	c := NewCache(src, Options{})
	if v := c.Version(context.Background(), gameID); v != 4 {
		t.Fatalf("first version = %d", v)
	}
	src.set(gameID, 5)
	cur, send, err := c.Published(context.Background(), gameID, 5)
	if err != nil || cur != 5 || !send {
		t.Fatalf("Published = %d, %v, %v, want 5, true, nil", cur, send, err)
	}
	cfg, _ := c.Get(context.Background(), gameID)
	if cfg.Version != 5 || string(cfg.Settings) != `{"v":5}` {
		t.Errorf("the cache holds %d %s, want version 5 and its settings", cfg.Version, cfg.Settings)
	}
	if n := src.callCount(gameID); n != 2 {
		t.Errorf("platform asked %d times, want 2 (the first load and the refresh)", n)
	}
}

func TestPublishedWorksWhenNothingWasCachedYet(t *testing.T) {
	c := NewCache(newFake(map[string]int{gameID: 2}), Options{})
	cur, send, err := c.Published(context.Background(), gameID, 2)
	if err != nil || cur != 2 || !send {
		t.Fatalf("Published = %d, %v, %v, want 2, true, nil", cur, send, err)
	}
}

// The cache may already hold the version (a player's hello filled it) while players who connected earlier
// have not been told; so the same version is announced.
func TestPublishedAnnouncesAVersionTheCacheAlreadyHas(t *testing.T) {
	c := NewCache(newFake(map[string]int{gameID: 5}), Options{})
	c.Version(context.Background(), gameID)
	cur, send, err := c.Published(context.Background(), gameID, 5)
	if err != nil || cur != 5 || !send {
		t.Errorf("Published = %d, %v, %v, want 5, true, nil", cur, send, err)
	}
}

func TestPublishedTellsTheNewestVersionWhenTwoPublishesWereQuick(t *testing.T) {
	c := NewCache(newFake(map[string]int{gameID: 6}), Options{})
	cur, send, err := c.Published(context.Background(), gameID, 5) // the event of 5 arrives, Platform already has 6
	if err != nil || cur != 6 || !send {
		t.Errorf("Published = %d, %v, %v, want 6, true, nil", cur, send, err)
	}
}

func TestPublishedIgnoresAnEventOlderThanTheCache(t *testing.T) {
	src := newFake(map[string]int{gameID: 6})
	c := NewCache(src, Options{})
	if _, _, err := c.Published(context.Background(), gameID, 6); err != nil {
		t.Fatal(err)
	}
	cur, send, err := c.Published(context.Background(), gameID, 5) // the event of 5 arrives late
	if err != nil || send || cur != 6 {
		t.Errorf("Published = %d, %v, %v, want 6, false, nil: players were told about 6 already", cur, send, err)
	}
	if v := c.Version(context.Background(), gameID); v != 6 {
		t.Errorf("the cache went back to version %d", v)
	}
}

func TestPublishedKeepsTheOldConfigWhenPlatformFails(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	c := NewCache(src, Options{})
	c.Version(context.Background(), gameID)

	src.set(gameID, 5)
	src.fail(ErrUnavailable)
	if _, send, err := c.Published(context.Background(), gameID, 5); !errors.Is(err, ErrUnavailable) || send {
		t.Fatalf("Published = %v, %v, want ErrUnavailable and no announcement", send, err)
	}
	if v := c.Version(context.Background(), gameID); v != 4 {
		t.Errorf("version after the failed refresh = %d, want the old 4", v)
	}
	// the retry works
	src.fail(nil)
	if cur, send, err := c.Published(context.Background(), gameID, 5); err != nil || cur != 5 || !send {
		t.Errorf("retry = %d, %v, %v, want 5, true, nil", cur, send, err)
	}
}

func TestPublishedWantsTheVersionPlatformIsNotYetAt(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	c := NewCache(src, Options{})
	c.Version(context.Background(), gameID)
	_, send, err := c.Published(context.Background(), gameID, 5) // Platform still says 4
	if !errors.Is(err, ErrStale) || send {
		t.Fatalf("Published = %v, %v, want ErrStale and no announcement", send, err)
	}
	if v := c.Version(context.Background(), gameID); v != 4 {
		t.Errorf("version = %d, want 4", v)
	}
}

// A lazy load that started before the publish must not answer for the refresh.
func TestPublishedAsksPlatformAgainEvenWhenALoadIsRunning(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	src.gate = make(chan struct{})
	c := NewCache(src, Options{})
	go c.Version(context.Background(), gameID) // a lazy load waits at the gate with version 4
	deadline := time.Now().Add(2 * time.Second)
	for src.callCount(gameID) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	src.set(gameID, 5)
	done := make(chan struct{})
	var cur int
	var err error
	go func() { defer close(done); cur, _, err = c.Published(context.Background(), gameID, 5) }()
	time.Sleep(50 * time.Millisecond)
	close(src.gate)
	<-done
	// The gate made both calls read the data after the change; what matters is that two requests were made.
	if err != nil || cur != 5 {
		t.Errorf("Published = %d, %v, want 5, nil", cur, err)
	}
	if n := src.callCount(gameID); n != 2 {
		t.Errorf("platform asked %d times, want 2: the refresh is its own request", n)
	}
}

func TestPublishedOfOtherGamesStaysApart(t *testing.T) {
	c := NewCache(newFake(map[string]int{gameID: 5, gameB: 2}), Options{})
	c.Published(context.Background(), gameID, 5)
	if v := c.Version(context.Background(), gameB); v != 2 {
		t.Errorf("the other game has version %d, want 2", v)
	}
}

func TestPublishedOfAnInvalidGameIsInvalidGame(t *testing.T) {
	src, calls := newSource(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	c := NewCache(src, Options{})
	if _, _, err := c.Published(context.Background(), "../x", 1); !errors.Is(err, ErrInvalidGame) {
		t.Errorf("error = %v, want ErrInvalidGame", err)
	}
	if calls.Load() != 0 {
		t.Error("platform was called for an invalid game ID")
	}
}

// A lazy load that read version 4 before a publish, and finishes after the publish of 5 was swapped in,
// must not take the cache back to 4.
func TestALateLoadNeverTakesTheCacheBackToAnOlderVersion(t *testing.T) {
	src := newFake(map[string]int{gameID: 4})
	src.gate, src.gateFirstOnly = make(chan struct{}), true
	c := NewCache(src, Options{})
	go c.Version(context.Background(), gameID) // reads 4, then waits at the gate
	deadline := time.Now().Add(2 * time.Second)
	for src.callCount(gameID) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	src.set(gameID, 5)
	if cur, send, err := c.Published(context.Background(), gameID, 5); err != nil || cur != 5 || !send {
		t.Fatalf("Published = %d, %v, %v, want 5, true, nil", cur, send, err)
	}
	close(src.gate) // now the old load finishes
	time.Sleep(50 * time.Millisecond)
	if v := c.Version(context.Background(), gameID); v != 5 {
		t.Errorf("version = %d, want 5: an older load took the cache back", v)
	}
}

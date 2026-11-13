package gameconfig

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a time source a test can move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newStatusCache returns a Cache over a fake source with a clock the test moves.
func newStatusCache(versions map[string]int) (*Cache, *fakeSource, *clock) {
	src := newFake(versions)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	return NewCache(src, Options{Now: clk.now}), src, clk
}

func TestASuspendedGameIsSuspended(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	src.fail(ErrSuspended)
	if !c.Suspended(context.Background(), gameID) {
		t.Fatal("a game Platform refuses with game_suspended must be suspended")
	}
}

func TestAServedGameIsNotSuspendedAndItsConfigIsKept(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	ctx := context.Background()
	if c.Suspended(ctx, gameID) {
		t.Fatal("a served game is not suspended")
	}
	// the same answer gave the config, so the hello that follows asks Platform nothing more
	if v := c.Version(ctx, gameID); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	if n := src.callCount(gameID); n != 1 {
		t.Fatalf("platform asked %d times, want 1", n)
	}
}

func TestAGameThatNeverPublishedIsNotSuspended(t *testing.T) {
	c, _, _ := newStatusCache(map[string]int{})
	if c.Suspended(context.Background(), gameID) {
		t.Fatal("no published config is a normal state, not a suspension")
	}
}

func TestTheStatusIsTrustedForTheTTLThenAskedAgain(t *testing.T) {
	c, src, clk := newStatusCache(map[string]int{gameID: 3})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		c.Suspended(ctx, gameID)
	}
	if n := src.callCount(gameID); n != 1 {
		t.Fatalf("platform asked %d times within the TTL, want 1", n)
	}

	// An event never came, but the game was suspended: the next check after the TTL finds out.
	src.fail(ErrSuspended)
	clk.advance(29 * time.Second)
	if c.Suspended(ctx, gameID) {
		t.Fatal("suspended before the TTL was over")
	}
	clk.advance(2 * time.Second)
	if !c.Suspended(ctx, gameID) {
		t.Fatal("not suspended after the TTL")
	}
	if n := src.callCount(gameID); n != 2 {
		t.Fatalf("platform asked %d times, want 2", n)
	}
}

func TestEachGameHasItsOwnStatus(t *testing.T) {
	c, _, _ := newStatusCache(map[string]int{gameID: 1, gameB: 1})
	c.SetSuspended(gameID, true)
	if !c.Suspended(context.Background(), gameID) || c.Suspended(context.Background(), gameB) {
		t.Fatal("suspending one game must not touch another")
	}
}

func TestAnEventChangesTheStatusAtOnceWithoutAskingPlatform(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	ctx := context.Background()
	c.Suspended(ctx, gameID) // known and served
	calls := src.callCount(gameID)

	c.SetSuspended(gameID, true)
	if !c.Suspended(ctx, gameID) {
		t.Fatal("game.suspended did not take effect")
	}
	c.SetSuspended(gameID, false)
	if c.Suspended(ctx, gameID) {
		t.Fatal("the status did not go back")
	}
	if src.callCount(gameID) != calls {
		t.Fatal("an event must not cost a request to Platform")
	}
}

// Platform being down never locks players out, and never lets a suspended game back in.
func TestPlatformTroubleKeepsTheLastStatus(t *testing.T) {
	for _, troubled := range []error{ErrUnavailable, errors.New("boom")} {
		for _, wasSuspended := range []bool{true, false} {
			c, src, clk := newStatusCache(map[string]int{gameID: 3})
			ctx := context.Background()
			c.SetSuspended(gameID, wasSuspended)
			src.fail(troubled)

			clk.advance(time.Minute) // the status is stale, so Platform is asked, and fails
			if got := c.Suspended(ctx, gameID); got != wasSuspended {
				t.Fatalf("%v, was suspended %v: got %v, want the last known status", troubled, wasSuspended, got)
			}
			// and it is not asked again on every connection while it is down
			calls := src.callCount(gameID)
			c.Suspended(ctx, gameID)
			c.Suspended(ctx, gameID)
			if src.callCount(gameID) != calls {
				t.Fatalf("a failing platform was asked again at once (%d calls)", src.callCount(gameID)-calls)
			}
		}
	}
}

func TestAGameNeverSeenIsServedWhilePlatformIsDown(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	src.fail(ErrUnavailable)
	if c.Suspended(context.Background(), gameID) {
		t.Fatal("with nothing known the check must fail open")
	}
}

func TestACallerWhoGivesUpGetsTheLastKnownStatus(t *testing.T) {
	c, src, clk := newStatusCache(map[string]int{gameID: 3})
	src.gate = make(chan struct{})
	t.Cleanup(func() { close(src.gate) })
	clk.advance(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if c.Suspended(ctx, gameID) {
		t.Fatal("a game never seen, and a platform too slow, must be served")
	}
}

func TestManyNewConnectionsAskPlatformOnce(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	src.gate = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Suspended(context.Background(), gameID)
		}()
	}
	time.Sleep(50 * time.Millisecond) // let them all reach the cache
	close(src.gate)
	wg.Wait()
	if n := src.callCount(gameID); n != 1 {
		t.Fatalf("platform asked %d times, want 1", n)
	}
}

func TestResumedOpensTheGameAndKeepsTheNewestConfig(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	ctx := context.Background()
	src.fail(ErrSuspended)
	if !c.Suspended(ctx, gameID) {
		t.Fatal("setup: want suspended")
	}

	// the game published version 4 while it was suspended, then was resumed
	src.fail(nil)
	src.set(gameID, 4)
	if err := c.Resumed(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	if c.Suspended(ctx, gameID) {
		t.Fatal("the game is still suspended after game.resumed")
	}
	if v := c.Version(ctx, gameID); v != 4 {
		t.Fatalf("version = %d, want the newest 4", v)
	}
}

func TestResumedWhilePlatformIsDownIsRetriedAndChangesNothing(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	c.SetSuspended(gameID, true)
	src.fail(ErrUnavailable)

	err := c.Resumed(context.Background(), gameID)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want one wrapping ErrUnavailable so the event is tried again", err)
	}
	if !c.Suspended(context.Background(), gameID) {
		t.Fatal("a resume that could not be confirmed must not open the game")
	}
}

// An old game.resumed that is delivered again after a newer suspension must not open the game:
// Platform has the final say.
func TestResumedIsOverruledByPlatform(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	src.fail(ErrSuspended)
	if err := c.Resumed(context.Background(), gameID); err != nil {
		t.Fatalf("error = %v, want the event acknowledged", err)
	}
	if !c.Suspended(context.Background(), gameID) {
		t.Fatal("platform says suspended, so the game must be")
	}
}

func TestResumedRefusesAGameIDThatIsNotAGame(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{})
	src.fail(ErrInvalidGame)
	if err := c.Resumed(context.Background(), "nope"); !errors.Is(err, ErrInvalidGame) {
		t.Fatalf("error = %v, want ErrInvalidGame", err)
	}
}

// A config.published of a suspended game says so, so the dispatcher can tell it from trouble.
func TestPublishedOfASuspendedGameSaysSuspended(t *testing.T) {
	c, src, _ := newStatusCache(map[string]int{gameID: 3})
	src.fail(ErrSuspended)
	_, _, err := c.Published(context.Background(), gameID, 4)
	if !errors.Is(err, ErrSuspended) {
		t.Fatalf("error = %v, want one wrapping ErrSuspended", err)
	}
	if !c.Suspended(context.Background(), gameID) {
		t.Fatal("the answer also tells the game is suspended")
	}
}

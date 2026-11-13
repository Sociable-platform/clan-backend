package gameconfig

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const suspendedBody = `{"error":{"code":"game_suspended","message":"This game is suspended"}}`

func TestASuspendedGameIsReported(t *testing.T) {
	c, fake, _, logs := newClient(t, http.StatusForbidden, suspendedBody)
	ctx := context.Background()

	got := c.Rules(ctx, game)
	want := Rules{MaxMembers: DefaultMaxMembers, Suspended: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// a suspension is Platform's normal answer, not trouble: no warning, one line saying it changed
	if strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "game suspension changed") {
		t.Fatalf("log = %q, want one info line and no warning", logs.String())
	}
	// and it is kept for the TTL like any other answer
	c.Rules(ctx, game)
	c.Rules(ctx, game)
	if fake.count() != 1 {
		t.Fatalf("platform asked %d times within the TTL, want 1", fake.count())
	}
}

func TestSuspendingAndResumingTakeEffectWithinTheTTL(t *testing.T) {
	c, fake, now, _ := newClient(t, http.StatusOK, published)
	ctx := context.Background()
	if c.Rules(ctx, game).Suspended {
		t.Fatal("an active game must not be suspended")
	}

	// An admin suspends the game: the kept answer is used until the TTL is over, then it flips.
	fake.set(http.StatusForbidden, suspendedBody)
	*now = now.Add(29 * time.Second)
	if c.Rules(ctx, game).Suspended {
		t.Fatal("suspended before the TTL was over")
	}
	*now = now.Add(2 * time.Second)
	if !c.Rules(ctx, game).Suspended {
		t.Fatal("not suspended after the TTL")
	}

	// The admin resumes it: same again, and the clan rules come back with it.
	fake.set(http.StatusOK, published)
	*now = now.Add(29 * time.Second)
	if !c.Rules(ctx, game).Suspended {
		t.Fatal("resumed before the TTL was over")
	}
	*now = now.Add(2 * time.Second)
	got := c.Rules(ctx, game)
	if got.Suspended || got.MaxMembers != 30 || !got.Published {
		t.Fatalf("after the TTL: %+v, want active with the published rules", got)
	}
}

func TestResumingAGameThatNeverPublishedIsNotSuspended(t *testing.T) {
	c, fake, now, _ := newClient(t, http.StatusForbidden, suspendedBody)
	ctx := context.Background()
	if !c.Rules(ctx, game).Suspended {
		t.Fatal("want suspended")
	}
	fake.set(http.StatusNotFound, `{"error":{"code":"widget_not_published","message":"This game has no published widget yet"}}`)
	*now = now.Add(time.Minute)
	if got := c.Rules(ctx, game); got.Suspended || !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("got %+v, want the defaults and not suspended", got)
	}
}

// If Platform cannot be asked, the last status stays: a suspended game stays refused and an
// active game stays served. Only a 403 that is not game_suspended counts as trouble too.
func TestPlatformTroubleKeepsTheLastStatus(t *testing.T) {
	trouble := map[string]struct {
		status int
		body   string
	}{
		"platform error":             {http.StatusInternalServerError, `{"error":{"code":"internal_error"}}`},
		"a 403 that is not ours":     {http.StatusForbidden, `{"error":{"code":"origin_not_allowed"}}`},
		"a 403 that is not an error": {http.StatusForbidden, `<html>forbidden</html>`},
		"broken JSON":                {http.StatusOK, `{"version":3,"config":`},
	}
	for name, b := range trouble {
		for _, wasSuspended := range []bool{true, false} {
			t.Run(name+"/was suspended="+map[bool]string{true: "true", false: "false"}[wasSuspended], func(t *testing.T) {
				c, fake, now, logs := newClient(t, http.StatusOK, published)
				if wasSuspended {
					fake.set(http.StatusForbidden, suspendedBody)
				}
				ctx := context.Background()
				if got := c.Rules(ctx, game).Suspended; got != wasSuspended {
					t.Fatalf("setup: suspended = %v, want %v", got, wasSuspended)
				}

				fake.set(b.status, b.body)
				*now = now.Add(time.Minute)
				if got := c.Rules(ctx, game).Suspended; got != wasSuspended {
					t.Fatalf("suspended = %v, want the last known %v", got, wasSuspended)
				}
				if !strings.Contains(logs.String(), "level=WARN") {
					t.Fatalf("want a warning in the log, got %q", logs)
				}
			})
		}
	}
}

func TestAGameNeverSeenIsServedWhilePlatformIsDown(t *testing.T) {
	c, fake, _, _ := newClient(t, http.StatusInternalServerError, `oops`)
	if got := c.Rules(context.Background(), game); got.Suspended {
		t.Fatalf("got %+v: with nothing known the check must fail open", got)
	}
	if fake.count() != 1 {
		t.Fatalf("platform asked %d times, want 1", fake.count())
	}
}

// A caller that hangs up makes the lookup fail for a reason that has nothing to do with Platform.
// That guess must not be remembered, or a suspended game nobody asked about yet would be served
// for a whole TTL.
func TestAHungUpCallerDoesNotCacheAGuess(t *testing.T) {
	c, fake, _, _ := newClient(t, http.StatusForbidden, suspendedBody)
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	if got := c.Rules(gone, game); got.Suspended {
		t.Fatalf("got %+v, want the unknown-game default", got)
	}
	// The next caller asks again at once instead of getting the remembered guess.
	if got := c.Rules(context.Background(), game); !got.Suspended {
		t.Fatalf("got %+v, want suspended: the failed lookup was cached", got)
	}
	if fake.count() != 1 {
		t.Fatalf("platform answered %d calls, want only the one that was not cancelled", fake.count())
	}
}

func TestFixedIsNeverSuspended(t *testing.T) {
	if (Fixed{}).Rules(context.Background(), game).Suspended {
		t.Fatal("without a platform URL nothing can be suspended")
	}
}

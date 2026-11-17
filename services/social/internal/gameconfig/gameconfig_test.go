package gameconfig

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const game = "7f3a2c10-5b8e-4d21-9a47-0c6e1b2d3f45"

const published = `{"version":3,"config":{"schemaVersion":1,"clan":{"maxMembers":30,"roles":[` +
	`{"id":"member","name":"Member","canPromote":false,"canKick":false},` +
	`{"id":"leader","name":"Leader","canPromote":true,"canKick":true}]}}}`

// fakePlatform answers the public widget-config endpoint with status and body, and counts calls.
type fakePlatform struct {
	mu     sync.Mutex
	status int
	body   string
	calls  int
	paths  []string
}

func (f *fakePlatform) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakePlatform) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

// newClient returns a Client for a fake Platform, a clock the test moves, and the log output.
func newClient(t *testing.T, status int, body string) (*Client, *fakePlatform, *time.Time, *bytes.Buffer) {
	t.Helper()
	fake := &fakePlatform{status: status, body: body}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	c, err := NewClient(srv.URL+"/", time.Second, 30*time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	return c, fake, &now, &logs
}

func TestRulesOfAPublishedConfig(t *testing.T) {
	c, fake, _, _ := newClient(t, http.StatusOK, published)
	got := c.Rules(context.Background(), game)
	want := Rules{MaxMembers: 30, Published: true, Roles: []Role{
		{ID: "member", Name: "Member"},
		{ID: "leader", Name: "Leader", CanPromote: true, CanKick: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if want := "GET /api/v1/public/games/" + game + "/widget-config"; len(fake.paths) != 1 || fake.paths[0] != want {
		t.Fatalf("platform got %v, want one %s", fake.paths, want)
	}
}

// canHandleRequests is optional: true and false are kept, a role without it reads as nil (Social then
// follows canKick; see roles.Ladder.CanHandleRequests).
func TestRolesKeepCanHandleRequests(t *testing.T) {
	body := `{"version":4,"config":{"schemaVersion":1,"clan":{"maxMembers":30,"roles":[` +
		`{"id":"member","name":"Member","canPromote":false,"canKick":false,"canHandleRequests":true},` +
		`{"id":"elder","name":"Elder","canPromote":false,"canKick":true},` +
		`{"id":"leader","name":"Leader","canPromote":true,"canKick":true,"canHandleRequests":false}]}}}`
	c, _, _, _ := newClient(t, http.StatusOK, body)
	got := c.Rules(context.Background(), game).Roles
	if len(got) != 3 {
		t.Fatalf("roles = %+v, want 3", got)
	}
	if got[0].CanHandleRequests == nil || !*got[0].CanHandleRequests {
		t.Errorf("member: CanHandleRequests = %v, want true", got[0].CanHandleRequests)
	}
	if got[1].CanHandleRequests != nil {
		t.Errorf("elder: CanHandleRequests = %v, want nil (not in the config)", *got[1].CanHandleRequests)
	}
	if got[2].CanHandleRequests == nil || *got[2].CanHandleRequests {
		t.Errorf("leader: CanHandleRequests = %v, want false", got[2].CanHandleRequests)
	}
}

func TestNotPublishedGivesTheDefaults(t *testing.T) {
	c, _, _, logs := newClient(t, http.StatusNotFound,
		`{"error":{"code":"widget_not_published","message":"This game has no published widget yet"}}`)
	if got := c.Rules(context.Background(), game); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("got %+v, want the defaults", got)
	}
	if logs.Len() != 0 {
		t.Fatalf("a game that has not published yet is normal, nothing to warn about: %s", logs)
	}
}

func TestAnswersAreKeptForTheTTL(t *testing.T) {
	c, fake, now, _ := newClient(t, http.StatusOK, published)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		c.Rules(ctx, game)
	}
	if fake.count() != 1 {
		t.Fatalf("platform asked %d times within the TTL, want 1", fake.count())
	}

	// the developer publishes a new limit; it shows once the TTL is over
	fake.set(http.StatusOK, strings.Replace(published, `"maxMembers":30`, `"maxMembers":50`, 1))
	*now = now.Add(29 * time.Second)
	if got := c.Rules(ctx, game).MaxMembers; got != 30 {
		t.Fatalf("before the TTL is over: %d, want the kept 30", got)
	}
	*now = now.Add(2 * time.Second)
	if got := c.Rules(ctx, game).MaxMembers; got != 50 {
		t.Fatalf("after the TTL: %d, want the new 50", got)
	}
	if fake.count() != 2 {
		t.Fatalf("platform asked %d times, want 2", fake.count())
	}

	// each game has its own entry
	c.Rules(ctx, "11111111-1111-4111-8111-111111111111")
	if fake.count() != 3 {
		t.Fatalf("another game should be asked for on its own, platform asked %d times", fake.count())
	}
}

func TestPlatformTroubleKeepsTheLastKnownRules(t *testing.T) {
	bad := map[string]struct {
		status int
		body   string
	}{
		"platform error":          {http.StatusInternalServerError, `{"error":{"code":"internal_error"}}`},
		"404 that is not ours":    {http.StatusNotFound, `404 page not found`},
		"broken JSON":             {http.StatusOK, `{"version":3,"config":`},
		"no maxMembers":           {http.StatusOK, `{"version":3,"config":{"clan":{}}}`},
		"maxMembers out of range": {http.StatusOK, `{"version":3,"config":{"clan":{"maxMembers":5000}}}`},
		"maxMembers not a number": {http.StatusOK, `{"version":3,"config":{"clan":{"maxMembers":"30"}}}`},
	}
	for name, b := range bad {
		t.Run(name, func(t *testing.T) {
			c, fake, now, logs := newClient(t, http.StatusOK, published)
			ctx := context.Background()
			c.Rules(ctx, game) // known: 30

			fake.set(b.status, b.body)
			*now = now.Add(time.Minute)
			if got := c.Rules(ctx, game).MaxMembers; got != 30 {
				t.Fatalf("got %d, want the last known 30", got)
			}
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Fatalf("want a warning in the log, got %q", logs)
			}
			// it is not asked again on every request while it is broken
			calls := fake.count()
			c.Rules(ctx, game)
			if fake.count() != calls {
				t.Fatal("a broken platform was asked again within the TTL")
			}
		})
	}
}

func TestPlatformDownWithNothingKnownGivesTheDefaults(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there any more
	var logs bytes.Buffer
	c, err := NewClient(url, 200*time.Millisecond, 30*time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Rules(context.Background(), game); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("got %+v, want the defaults", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("want a warning in the log, got %q", logs.String())
	}
}

func TestNewClientRefusesABadURL(t *testing.T) {
	for _, u := range []string{"", "platform:8081", "ftp://platform", "http://"} {
		if _, err := NewClient(u, time.Second, time.Second, slog.Default()); err == nil {
			t.Errorf("%q: want an error", u)
		}
	}
}

func TestFixedGivesTheDefaults(t *testing.T) {
	if got := (Fixed{}).Rules(context.Background(), game); !reflect.DeepEqual(got, Defaults()) || got.MaxMembers != 10 {
		t.Fatalf("got %+v", got)
	}
}

func TestManyRequestsAtOnce(t *testing.T) {
	c, _, _, _ := newClient(t, http.StatusOK, published)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := c.Rules(context.Background(), game).MaxMembers; got != 30 {
				t.Errorf("got %d", got)
			}
		}()
	}
	wg.Wait()
}

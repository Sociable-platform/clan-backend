package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
)

// These tests need a real PostgreSQL with the platform migrations applied. They only run
// when PLATFORM_TEST_DATABASE_URL is set, for example
//
//	PLATFORM_TEST_DATABASE_URL=postgres://platform_user:PASSWORD@localhost:5432/clan go test ./services/platform/...
//
// Each test creates its own developer and game, and removes its rows when it ends.

func testRepo(t *testing.T) *Repository {
	t.Helper()
	url := os.Getenv("PLATFORM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PLATFORM_TEST_DATABASE_URL is not set")
	}
	pool, err := pg.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

// newGame creates a developer and a game in the database and returns their IDs.
func newGame(t *testing.T, r *Repository) (developerID, gameID string) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("widget-test-%d@example.com", time.Now().UnixNano())
	dev, err := r.CreateDeveloper(ctx, email, "hash", "Test")
	if err != nil {
		t.Fatal(err)
	}
	game, err := r.CreateGame(ctx, dev.ID, "Widget test game", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = r.pool.Exec(c, `delete from widget_versions where game_id = $1`, game.ID)
		_, _ = r.pool.Exec(c, `delete from widget_drafts where game_id = $1`, game.ID)
		_, _ = r.pool.Exec(c, `delete from games where id = $1`, game.ID)
		_, _ = r.pool.Exec(c, `delete from developers where id = $1`, dev.ID)
	})
	return dev.ID, game.ID
}

const unknownID = "00000000-0000-4000-8000-00000000dead"

func TestWidgetDraft_SaveAndRead(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	_, gameID := newGame(t, r)

	if _, err := r.WidgetDraftOfGame(ctx, gameID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no draft yet: want ErrNotFound, got %v", err)
	}

	first, err := r.SaveWidgetDraft(ctx, gameID, []byte(`{"schemaVersion": 1, "a": "x"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.WidgetDraftOfGame(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, got.Config, `{"schemaVersion":1,"a":"x"}`) || got.GameID != gameID {
		t.Fatalf("read back %s", got.Config)
	}

	// a second save replaces the first and moves updated_at forward
	time.Sleep(10 * time.Millisecond)
	second, err := r.SaveWidgetDraft(ctx, gameID, []byte(`{"schemaVersion":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = r.WidgetDraftOfGame(ctx, gameID)
	if !jsonEqual(t, got.Config, `{"schemaVersion":1,"b":2}`) {
		t.Fatalf("second save should replace the first, got %s", got.Config)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("updated_at did not move: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
	var rows int
	_ = r.pool.QueryRow(ctx, `select count(*) from widget_drafts where game_id = $1`, gameID).Scan(&rows)
	if rows != 1 {
		t.Fatalf("one draft per game, found %d rows", rows)
	}
}

func TestWidgetDraft_UnknownGame(t *testing.T) {
	r := testRepo(t)
	if _, err := r.SaveWidgetDraft(context.Background(), unknownID, []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestAddWidgetVersion_NumbersCountUpFromOne(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	devID, gameID := newGame(t, r)

	for want := 1; want <= 3; want++ {
		v, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(fmt.Sprintf(`{"n": %d}`, want)))
		if err != nil {
			t.Fatal(err)
		}
		if v.Version != want || v.ID == "" || v.PublishedBy != devID || v.PublishedAt.IsZero() {
			t.Fatalf("version %d: got %+v", want, v)
		}
	}

	list, err := r.WidgetVersionsOfGame(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Version != 3 || list[2].Version != 1 {
		t.Fatalf("want 3, 2, 1, got %+v", list)
	}
	if list[0].Config != nil {
		t.Fatal("lists leave the config out")
	}

	one, err := r.WidgetVersionOfGame(ctx, gameID, 2)
	if err != nil || !jsonEqual(t, one.Config, `{"n":2}`) {
		t.Fatalf("version 2: %+v, %v", one, err)
	}
	if _, err := r.WidgetVersionOfGame(ctx, gameID, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown version: want ErrNotFound, got %v", err)
	}
}

func TestAddWidgetVersion_EachGameCountsOnItsOwn(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	devA, gameA := newGame(t, r)
	devB, gameB := newGame(t, r)

	for i := 0; i < 2; i++ {
		if _, err := r.AddWidgetVersion(ctx, gameA, devA, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	v, err := r.AddWidgetVersion(ctx, gameB, devB, []byte(`{}`))
	if err != nil || v.Version != 1 {
		t.Fatalf("game B starts at 1, got %+v, %v", v, err)
	}
	if _, err := r.WidgetVersionOfGame(ctx, gameB, 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("game B must not see game A's versions")
	}
}

func TestAddWidgetVersion_UnknownGameOrDeveloper(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	devID, gameID := newGame(t, r)

	if _, err := r.AddWidgetVersion(ctx, unknownID, devID, []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown game: want ErrNotFound, got %v", err)
	}
	if _, err := r.AddWidgetVersion(ctx, gameID, unknownID, []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown developer: want ErrNotFound, got %v", err)
	}
	if list, _ := r.WidgetVersionsOfGame(ctx, gameID); len(list) != 0 {
		t.Fatalf("a failed publish must leave no version behind, got %+v", list)
	}
}

// Many publishes of one game at the same moment must get the numbers 1..N exactly once.
func TestAddWidgetVersion_ConcurrentPublishesNeverRepeatANumber(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	devID, gameID := newGame(t, r)

	const n = 20
	var wg sync.WaitGroup
	numbers := make(chan int, n)
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(`{}`))
			if err != nil {
				errs <- err
				return
			}
			numbers <- v.Version
		}()
	}
	close(start)
	wg.Wait()
	close(numbers)
	close(errs)

	for err := range errs {
		t.Fatalf("a publish failed: %v", err)
	}
	seen := map[int]bool{}
	for v := range numbers {
		if seen[v] {
			t.Fatalf("version %d was given twice", v)
		}
		seen[v] = true
	}
	for want := 1; want <= n; want++ {
		if !seen[want] {
			t.Fatalf("version %d is missing, got %v", want, seen)
		}
	}
}

func jsonEqual(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("not json: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestLatestWidgetVersion(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	devID, gameID := newGame(t, r)

	if _, err := r.LatestWidgetVersion(ctx, gameID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("never published: want ErrNotFound, got %v", err)
	}

	// a draft is not a published config
	if _, err := r.SaveWidgetDraft(ctx, gameID, []byte(`{"draft": true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LatestWidgetVersion(ctx, gameID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft only: want ErrNotFound, got %v", err)
	}

	for n := 1; n <= 3; n++ {
		if _, err := r.AddWidgetVersion(ctx, gameID, devID, []byte(fmt.Sprintf(`{"n": %d}`, n))); err != nil {
			t.Fatal(err)
		}
	}
	// the draft changes after publishing; the latest version must not
	if _, err := r.SaveWidgetDraft(ctx, gameID, []byte(`{"draft": "newer"}`)); err != nil {
		t.Fatal(err)
	}
	v, err := r.LatestWidgetVersion(ctx, gameID)
	if err != nil || v.Version != 3 || !jsonEqual(t, v.Config, `{"n":3}`) {
		t.Fatalf("latest: %+v, %v", v, err)
	}

	if _, err := r.LatestWidgetVersion(ctx, unknownID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown game: want ErrNotFound, got %v", err)
	}
}

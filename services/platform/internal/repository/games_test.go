package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// These tests need the real database like widget_test.go (PLATFORM_TEST_DATABASE_URL), with migration
// 000006 (games.allowed_origins) applied.

func TestNewGameHasAnEmptyNonNilAllowedOriginsList(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	g, err := r.GameOfDeveloper(ctx, game, dev)
	if err != nil {
		t.Fatal(err)
	}
	if g.AllowedOrigins == nil || len(g.AllowedOrigins) != 0 {
		t.Errorf("allowed origins = %#v, want an empty, non-nil list", g.AllowedOrigins)
	}
}

func TestSetAllowedOriginsKeepsTheOrderAndShowsEverywhere(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)
	want := []string{"https://play.example.com", "http://localhost:5174"}

	set, err := r.SetAllowedOrigins(ctx, game, dev, want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(set.AllowedOrigins, want) || set.ID != game || set.DeveloperID != dev {
		t.Errorf("set = %+v, want the game with origins %v", set, want)
	}

	owned, err := r.GameOfDeveloper(ctx, game, dev)
	if err != nil || !reflect.DeepEqual(owned.AllowedOrigins, want) {
		t.Errorf("GameOfDeveloper = %+v, %v", owned, err)
	}
	byID, err := r.GameByID(ctx, game)
	if err != nil || !reflect.DeepEqual(byID.AllowedOrigins, want) {
		t.Errorf("GameByID = %+v, %v", byID, err)
	}
	list, err := r.GamesByDeveloper(ctx, dev)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0].AllowedOrigins, want) {
		t.Errorf("GamesByDeveloper = %+v, %v", list, err)
	}
}

func TestSetAllowedOriginsReplacesAndClears(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	if _, err := r.SetAllowedOrigins(ctx, game, dev, []string{"https://a.example.com", "https://b.example.com"}); err != nil {
		t.Fatal(err)
	}
	replaced, err := r.SetAllowedOrigins(ctx, game, dev, []string{"https://c.example.com"})
	if err != nil || !reflect.DeepEqual(replaced.AllowedOrigins, []string{"https://c.example.com"}) {
		t.Fatalf("replace = %+v, %v", replaced, err)
	}
	for _, in := range [][]string{{}, nil} { // nil must not break the not null column
		if _, err := r.SetAllowedOrigins(ctx, game, dev, []string{"https://c.example.com"}); err != nil {
			t.Fatal(err)
		}
		cleared, err := r.SetAllowedOrigins(ctx, game, dev, in)
		if err != nil {
			t.Fatalf("clear with %#v: %v", in, err)
		}
		if cleared.AllowedOrigins == nil || len(cleared.AllowedOrigins) != 0 {
			t.Errorf("after clearing with %#v: %#v, want an empty, non-nil list", in, cleared.AllowedOrigins)
		}
	}
}

func TestSetAllowedOriginsOnlyForTheOwner(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)
	otherDev, _ := newGame(t, r)

	if _, err := r.SetAllowedOrigins(ctx, game, otherDev, []string{"https://evil.example.com"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another developer: err = %v, want ErrNotFound", err)
	}
	if _, err := r.SetAllowedOrigins(ctx, unknownID, dev, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown game: err = %v, want ErrNotFound", err)
	}
	g, err := r.GameByID(ctx, game)
	if err != nil || len(g.AllowedOrigins) != 0 {
		t.Errorf("the game changed: %+v, %v", g, err)
	}
}

func TestGameByIDUnknownIsNotFound(t *testing.T) {
	r := testRepo(t)
	if _, err := r.GameByID(context.Background(), unknownID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGamesWithoutDatabase(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	if _, err := r.GameByID(ctx, unknownID); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("GameByID: err = %v, want ErrNoDatabase", err)
	}
	if _, err := r.SetAllowedOrigins(ctx, unknownID, unknownID, nil); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("SetAllowedOrigins: err = %v, want ErrNoDatabase", err)
	}
}

package repository

import (
	"context"
	"errors"
	"testing"
)

func TestABlockStopsFriendRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	if _, err := r.Block(ctx, game, ali, aysel); err != nil {
		t.Fatal(err)
	}

	// the blocked player is told the blocker does not exist
	if _, err := r.SendFriendRequest(ctx, game, aysel, ali); !errors.Is(err, ErrPlayerNotFound) {
		t.Fatalf("blocked player asks: %v, want ErrPlayerNotFound", err)
	}
	// the blocker must unblock first
	if _, err := r.SendFriendRequest(ctx, game, ali, aysel); !errors.Is(err, ErrPlayerBlocked) {
		t.Fatalf("blocker asks: %v, want ErrPlayerBlocked", err)
	}
	if requests, friendships := pairState(t, r, game, ali, aysel); requests != 0 || friendships != 0 {
		t.Fatalf("%d requests, %d friendships; want none", requests, friendships)
	}

	// after the unblock both may ask again
	if _, err := r.Unblock(ctx, game, ali, aysel); err != nil {
		t.Fatal(err)
	}
	sendRequest(t, r, game, aysel, ali)
}

func TestSearchAndBlocks(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	me := newPlayer(t, r, game, "me")
	alina := newPlayer(t, r, game, "alina")   // I blocked her
	alibek := newPlayer(t, r, game, "alibek") // he blocked me
	alim := newPlayer(t, r, game, "alim")     // nothing
	if _, err := r.Block(ctx, game, me, alina); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Block(ctx, game, alibek, me); err != nil {
		t.Fatal(err)
	}

	got, err := r.SearchPlayers(ctx, game, me, "ali", 10)
	if err != nil {
		t.Fatal(err)
	}
	relations := map[string]string{}
	for _, p := range got {
		relations[p.ID] = p.Relation
	}
	if len(got) != 2 || relations[alina] != RelationBlocked || relations[alim] != RelationNone {
		t.Fatalf("got %+v; want alina (blocked) and alim (none)", got)
	}
	if _, shown := relations[alibek]; shown {
		t.Fatal("a player who blocked me must not be shown to me")
	}

	// the blocked player does not see the blocker; the blocker of me sees me as blocked
	got, _ = r.SearchPlayers(ctx, game, alina, "me", 10)
	if len(got) != 0 {
		t.Fatalf("alina sees %+v, want nobody", got)
	}
	got, _ = r.SearchPlayers(ctx, game, alibek, "me", 10)
	if len(got) != 1 || got[0].Relation != RelationBlocked {
		t.Fatalf("alibek sees %+v, want me as blocked", got)
	}
}

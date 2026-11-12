package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// blocked reports whether blocker has blocked target.
func blocked(t *testing.T, r *Repository, gameID, blocker, target string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(),
		"select count(*) from blocks where game_id = $1 and blocker_id = $2 and blocked_id = $3",
		gameID, blocker, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestBlockSavesTheBlock(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")

	got, err := r.Block(context.Background(), game, ali, strings.ToUpper(aysel))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created || got.Blocker.ID != ali || got.Blocked.ID != aysel || got.Blocked.DisplayName != "aysel" ||
		got.RemovedFriendship != nil || got.RemovedRequest != nil {
		t.Fatalf("got %+v", got)
	}
	if !blocked(t, r, game, ali, aysel) || blocked(t, r, game, aysel, ali) {
		t.Fatal("want ali → aysel only: blocking is one-way")
	}

	// blocking again changes nothing
	again, err := r.Block(context.Background(), game, ali, aysel)
	if err != nil || again.Created {
		t.Fatalf("second block: %+v, %v; want Created false", again, err)
	}

	// the other way is a separate block
	if back, err := r.Block(context.Background(), game, aysel, ali); err != nil || !back.Created {
		t.Fatalf("aysel blocks ali: %+v, %v", back, err)
	}
}

func TestBlockEndsTheFriendship(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	makeFriends(t, r, game, ali, aysel)

	got, err := r.Block(context.Background(), game, aysel, ali)
	if err != nil {
		t.Fatal(err)
	}
	f := got.RemovedFriendship
	if f == nil || got.RemovedRequest != nil {
		t.Fatalf("got %+v, want the friendship removed", got)
	}
	if f.PlayerA.ID > f.PlayerB.ID || (f.PlayerA.ID != ali && f.PlayerA.ID != aysel) {
		t.Fatalf("removed friendship %+v: want the pair, smaller ID first", f)
	}
	if req, friends := pairState(t, r, game, ali, aysel); req != 0 || friends != 0 {
		t.Fatalf("after the block: %d requests, %d friendships; want none", req, friends)
	}
	if ids := friendsOf(t, r, game, ali); len(ids) != 0 {
		t.Fatalf("ali still has friends %v", ids)
	}
}

func TestBlockDeletesThePendingRequestEitherWay(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel, leyla := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel"), newPlayer(t, r, game, "leyla")

	// the blocker had asked: the request was the blocker's
	sent := sendRequest(t, r, game, ali, aysel)
	got, err := r.Block(context.Background(), game, ali, aysel)
	if err != nil {
		t.Fatal(err)
	}
	if req := got.RemovedRequest; req == nil || req.ID != sent.ID || req.Sender.ID != ali || req.Receiver.ID != aysel {
		t.Fatalf("removed %+v, want ali's request to aysel", got.RemovedRequest)
	}

	// the blocked player had asked: the request was theirs
	sent = sendRequest(t, r, game, leyla, ali)
	got, err = r.Block(context.Background(), game, ali, leyla)
	if err != nil {
		t.Fatal(err)
	}
	if req := got.RemovedRequest; req == nil || req.ID != sent.ID || req.Sender.ID != leyla || req.Receiver.ID != ali {
		t.Fatalf("removed %+v, want leyla's request to ali", got.RemovedRequest)
	}
	for _, p := range []string{aysel, leyla} {
		if req, friends := pairState(t, r, game, ali, p); req != 0 || friends != 0 {
			t.Fatalf("pair ali/%s: %d requests, %d friendships", p, req, friends)
		}
	}
}

func TestBlockRefusals(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ali := newPlayer(t, r, game, "ali")
	stranger := newPlayer(t, r, other, "stranger")
	cases := []struct {
		name, target string
		want         error
	}{
		{"themselves", ali, ErrBlockSelf},
		{"themselves in upper case", strings.ToUpper(ali), ErrBlockSelf},
		{"a player of another game", stranger, ErrPlayerNotFound},
		{"an unknown player", "00000000-0000-4000-8000-000000000000", ErrPlayerNotFound},
		{"an ID that is not a UUID", "u_42", ErrPlayerNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := r.Block(context.Background(), game, ali, c.target); !errors.Is(err, c.want) {
				t.Fatalf("block: %v, want %v", err, c.want)
			}
			if _, err := r.Unblock(context.Background(), game, ali, c.target); !errors.Is(err, c.want) {
				t.Fatalf("unblock: %v, want %v", err, c.want)
			}
		})
	}
	if n := countRows(t, r, "blocks", game); n != 0 {
		t.Fatalf("%d blocks saved", n)
	}
}

func TestUnblock(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	makeFriends(t, r, game, ali, aysel)
	if _, err := r.Block(ctx, game, ali, aysel); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Block(ctx, game, aysel, ali); err != nil {
		t.Fatal(err)
	}

	got, err := r.Unblock(ctx, game, ali, aysel)
	if err != nil || !got.Removed || got.Blocker.ID != ali || got.Blocked.ID != aysel {
		t.Fatalf("unblock: %+v, %v", got, err)
	}
	if blocked(t, r, game, ali, aysel) || !blocked(t, r, game, aysel, ali) {
		t.Fatal("only ali's block should be gone; aysel's stays")
	}
	// the friendship the block ended stays ended
	if ids := friendsOf(t, r, game, ali); len(ids) != 0 {
		t.Fatalf("ali has friends %v again", ids)
	}
	// unblocking again changes nothing
	if again, err := r.Unblock(ctx, game, ali, aysel); err != nil || again.Removed {
		t.Fatalf("second unblock: %+v, %v; want Removed false", again, err)
	}
}

func TestListBlocks(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali := newPlayer(t, r, game, "ali")
	var targets []string
	for i := 0; i < 3; i++ {
		p := newPlayer(t, r, game, fmt.Sprintf("p%d", i))
		targets = append(targets, p)
		if _, err := r.Block(ctx, game, ali, p); err != nil {
			t.Fatal(err)
		}
		// newest first needs distinct times
		if _, err := r.pool.Exec(ctx, "update blocks set created_at = now() - make_interval(hours => $3) where blocker_id = $1 and blocked_id = $2",
			ali, p, 10-i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Block(ctx, game, targets[0], ali); err != nil { // someone blocking ali is not in ali's list
		t.Fatal(err)
	}

	got, err := r.ListBlocks(ctx, game, ali)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != targets[2] || got[2].ID != targets[0] || got[0].DisplayName != "p2" || got[0].Since.IsZero() {
		t.Fatalf("got %+v, want p2, p1, p0", got)
	}
	if none, _ := r.ListBlocks(ctx, game, targets[1]); none == nil || len(none) != 0 {
		t.Fatalf("a player who blocked nobody: %+v, want an empty list", none)
	}
}

// A block and an accept of the pair's request at the same moment: the pair lock puts one after
// the other, so they never end up both blocked and friends.
func TestBlockWhileTheRequestIsAccepted(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		a, b := newPlayer(t, r, game, fmt.Sprintf("a%d", round)), newPlayer(t, r, game, fmt.Sprintf("b%d", round))
		req := sendRequest(t, r, game, a, b)
		var wg sync.WaitGroup
		var blockRes BlockResult
		var blockErr, acceptErr error
		wg.Add(2)
		go func() { defer wg.Done(); blockRes, blockErr = r.Block(ctx, game, a, b) }()
		go func() { defer wg.Done(); _, acceptErr = r.AcceptFriendRequest(ctx, game, req.ID, b) }()
		wg.Wait()
		if blockErr != nil {
			t.Fatalf("round %d: block = %v", round, blockErr)
		}
		switch {
		case acceptErr == nil: // accepted first, then the block ended the friendship
			if blockRes.RemovedFriendship == nil {
				t.Fatalf("round %d: accepted first, but the block removed no friendship", round)
			}
		case errors.Is(acceptErr, ErrFriendRequestNotFound): // blocked first: the request was gone
			if blockRes.RemovedRequest == nil {
				t.Fatalf("round %d: blocked first, but the block removed no request", round)
			}
		default:
			t.Fatalf("round %d: accept = %v", round, acceptErr)
		}
		if requests, friendships := pairState(t, r, game, a, b); requests != 0 || friendships != 0 {
			t.Fatalf("round %d: %d requests, %d friendships after the block", round, requests, friendships)
		}
	}
}

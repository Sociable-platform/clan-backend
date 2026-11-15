package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// friends makes a and b friends through a request and its acceptance.
func friends(t *testing.T, r *Repository, game, a, b string) {
	t.Helper()
	ctx := context.Background()
	sent, err := r.SendFriendRequest(ctx, game, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptFriendRequest(ctx, game, sent.Request.ID, b); err != nil {
		t.Fatal(err)
	}
}

func TestDeletePlayerRemovesEverythingAndHidesThePlayer(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	ali := newPlayer(t, r, game, "ali")
	vera := newPlayer(t, r, game, "vera")
	omar := newPlayer(t, r, game, "omar")
	zed := newPlayer(t, r, game, "zed")
	kim := newPlayer(t, r, game, "kim")

	clan, err := r.CreateClan(ctx, game, ali, NewClan{Name: "Wolves"}) // ali owns it
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClan(ctx, game, clan.ID, vera, testMaxMembers, "member"); err != nil {
		t.Fatal(err)
	}
	friends(t, r, game, ali, omar)
	if _, err := r.SendFriendRequest(ctx, game, ali, zed); err != nil { // pending, sent by ali
		t.Fatal(err)
	}
	if _, err := r.Block(ctx, game, kim, ali); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute)

	res, err := r.DeletePlayer(ctx, game, "ali", "Deleted player")
	if err != nil {
		t.Fatal(err)
	}

	// What it reports
	if res.Player.ID != ali || res.Player.DisplayName != "ali" {
		t.Errorf("player = %+v, want ali with the old name", res.Player)
	}
	if res.Clan == nil || res.Clan.ClanID != clan.ID || res.Clan.Disbanded || res.Clan.NewOwner == nil || res.Clan.NewOwner.ID != vera {
		t.Errorf("clan = %+v, want Wolves handed to vera", res.Clan)
	}
	if len(res.Friendships) != 1 || len(res.Requests) != 1 || len(res.Blocks) != 1 {
		t.Fatalf("removed %d friendships, %d requests, %d blocks; want 1 each", len(res.Friendships), len(res.Requests), len(res.Blocks))
	}
	if res.Requests[0].Sender.ID != ali || res.Requests[0].Receiver.ID != zed {
		t.Errorf("request = %+v, want ali -> zed", res.Requests[0])
	}
	if res.Blocks[0].Blocker.ID != kim || res.Blocks[0].Blocked.ID != ali {
		t.Errorf("block = %+v, want kim blocked ali", res.Blocks[0])
	}

	// What it saved
	if mine, err := r.MyClan(ctx, game, "vera"); err != nil || mine.Role != "owner" {
		t.Errorf("vera's clan = %+v, %v; want her as the new owner", mine, err)
	}
	if fs, err := r.ListFriends(ctx, game, omar); err != nil || len(fs) != 0 {
		t.Errorf("omar's friends = %+v, %v; want none", fs, err)
	}
	for _, q := range []string{"ali", "Deleted"} {
		found, err := r.SearchPlayers(ctx, game, omar, q, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 0 {
			t.Errorf("search %q found %+v; a deleted player is never found", q, found)
		}
	}
	if _, err := r.MyClan(ctx, game, "ali"); !errors.Is(err, ErrNotInClan) {
		t.Errorf("ali's clan: err = %v, want ErrNotInClan", err)
	}

	// Tokens issued before are refused; one issued after is not.
	if gone, err := r.DeletedAfter(ctx, game, "ali", before); err != nil || !gone {
		t.Errorf("DeletedAfter(before) = %v, %v; want true", gone, err)
	}
	if gone, err := r.DeletedAfter(ctx, game, "ali", time.Now().Add(time.Minute)); err != nil || gone {
		t.Errorf("DeletedAfter(later) = %v, %v; want false", gone, err)
	}
	if gone, err := r.DeletedAfter(ctx, game, "vera", before); err != nil || gone {
		t.Errorf("DeletedAfter(vera) = %v, %v; want false: she was not deleted", gone, err)
	}
}

// A game that gives the same ID to a new account gets a new, empty player, and only that one is found.
func TestDeletedPlayerIDCanBeReused(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	old, err := r.UpsertPlayer(ctx, game, "u_1", "test")
	if err != nil {
		t.Fatal(err)
	}
	searcher := newPlayer(t, r, game, "searcher")
	if _, err := r.DeletePlayer(ctx, game, "u_1", "Deleted player"); err != nil {
		t.Fatal(err)
	}
	fresh, err := r.UpsertPlayer(ctx, game, "u_1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old.ID {
		t.Fatal("the new account got the deleted player's record")
	}
	again, err := r.UpsertPlayer(ctx, game, "u_1", "test")
	if err != nil || again.ID != fresh.ID {
		t.Fatalf("second upsert = %+v, %v; want the new record again", again, err)
	}
	found, err := r.SearchPlayers(ctx, game, searcher, "test", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != fresh.ID {
		t.Fatalf("search found %+v, want only the new account", found)
	}
}

func TestDeletePlayerLastMemberDisbandsTheClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	clan, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "solo"), NewClan{Name: "Lonely"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.DeletePlayer(ctx, game, "solo", "Deleted player")
	if err != nil {
		t.Fatal(err)
	}
	if res.Clan == nil || !res.Clan.Disbanded || res.Clan.ClanID != clan.ID {
		t.Fatalf("clan = %+v, want Lonely disbanded", res.Clan)
	}
	if _, err := r.ListMembers(ctx, game, clan.ID); !errors.Is(err, ErrClanNotFound) {
		t.Fatalf("members of the disbanded clan: err = %v, want ErrClanNotFound", err)
	}
}

// A player social never saw (or one already deleted) gets a deleted record, so the tokens issued
// before the call are refused all the same.
func TestDeletePlayerNeverSeenWritesADeletedRecord(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	before := time.Now().Add(-time.Minute)

	for range 2 { // the second call finds no live player either
		res, err := r.DeletePlayer(ctx, game, "ghost", "Deleted player")
		if err != nil {
			t.Fatal(err)
		}
		if res.Player.ID != "" || res.Clan != nil {
			t.Fatalf("result = %+v, want nothing removed", res)
		}
	}
	if gone, err := r.DeletedAfter(ctx, game, "ghost", before); err != nil || !gone {
		t.Fatalf("DeletedAfter = %v, %v; want true", gone, err)
	}
	if _, err := r.MyClan(ctx, game, "ghost"); !errors.Is(err, ErrNotInClan) {
		t.Fatalf("err = %v, want ErrNotInClan", err)
	}
}

func TestADeletedPlayerCannotBeFriendedOrBlocked(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	gone := newPlayer(t, r, game, "gone")
	me := newPlayer(t, r, game, "me")
	if _, err := r.DeletePlayer(ctx, game, "gone", "Deleted player"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SendFriendRequest(ctx, game, me, gone); !errors.Is(err, ErrPlayerNotFound) {
		t.Errorf("friend request: err = %v, want ErrPlayerNotFound", err)
	}
	if _, err := r.Block(ctx, game, me, gone); !errors.Is(err, ErrPlayerNotFound) {
		t.Errorf("block: err = %v, want ErrPlayerNotFound", err)
	}
}

// The same player ID in another game is another player and stays.
func TestDeletePlayerKeepsOtherGames(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()

	newPlayer(t, r, game, "u_1")
	kept := newPlayer(t, r, other, "u_1")
	before := time.Now().Add(-time.Minute)
	if _, err := r.DeletePlayer(ctx, game, "u_1", "Deleted player"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := r.DeletedAfter(ctx, other, "u_1", before); err != nil || deleted {
		t.Fatalf("other game: DeletedAfter = %v, %v; want false", deleted, err)
	}
	p, err := r.UpsertPlayer(ctx, other, "u_1", "u_1")
	if err != nil || p.ID != kept {
		t.Fatalf("other game's player = %+v, %v; want the same record", p, err)
	}
}

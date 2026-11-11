package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestKickMemberRemovesThePlayer(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	stay := joinAs(t, r, game, clan, "u_stay")
	kicked := joinAs(t, r, game, clan, "u_kicked")

	got, err := r.KickMember(ctx, game, clan, "owner_Wolves", kicked)
	if err != nil {
		t.Fatal(err)
	}
	want := LeaveResult{ClanID: clan, ClanName: "Wolves", Player: ClanPlayer{ID: kicked, DisplayName: "u_kicked"},
		KickedBy: &ClanPlayer{ID: owner, DisplayName: "owner_Wolves"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v (kicked by %+v)\nwant %+v (kicked by %+v)", got, got.KickedBy, want, want.KickedBy)
	}
	if memberOf(t, r, clan, kicked) || !memberOf(t, r, clan, stay) || ownerOf(t, r, clan) != owner {
		t.Fatal("only the kicked player should be gone, and the owner should stay the owner")
	}
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
	if count != 2 {
		t.Fatalf("member_count = %d, want 2", count)
	}
	checkClanIsConsistent(t, r, clan)

	// the kicked player is free: they can join again (there is no ban)
	if _, err := r.JoinClan(ctx, game, clan, kicked, testMaxMembers); err != nil {
		t.Fatalf("joining again after a kick: %v", err)
	}
	checkClanIsConsistent(t, r, clan)
}

func TestKickMemberRefusals(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	otherGame := newGameID(t)
	defer cleanup(game)
	defer cleanup(otherGame)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	member := joinAs(t, r, game, clan, "u_member")
	joinAs(t, r, game, clan, "u_member2")
	bears := ownClan(t, r, game, "Bears")
	bear := joinAs(t, r, game, bears, "u_bear")
	foreign := ownClan(t, r, otherGame, "Wolves")

	cases := []struct {
		name               string
		game, clan, caller string
		player             string
		want               error
	}{
		{"unknown clan", game, "00000000-0000-4000-8000-000000000000", "owner_Wolves", member, ErrClanNotFound},
		{"clan ID is not a UUID", game, "not-a-uuid", "owner_Wolves", member, ErrClanNotFound},
		{"clan of another game", game, foreign, "owner_Wolves", member, ErrClanNotFound},
		{"a member is not the owner", game, clan, "u_member2", member, ErrNotClanOwner},
		{"an outsider is not the owner", game, clan, "u_nobody", member, ErrNotClanOwner},
		{"another clan's owner is not the owner", game, clan, "owner_Bears", member, ErrNotClanOwner},
		{"the owner kicks themselves", game, clan, "owner_Wolves", owner, ErrKickSelf},
		{"the player is in another clan", game, clan, "owner_Wolves", bear, ErrMemberNotFound},
		{"unknown player", game, clan, "owner_Wolves", "00000000-0000-4000-8000-000000000000", ErrMemberNotFound},
		{"player ID is not a UUID", game, clan, "owner_Wolves", "u_member", ErrMemberNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := r.KickMember(ctx, c.game, c.clan, c.caller, c.player); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	// nothing was changed by any of them
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
	if count != 3 || !memberOf(t, r, clan, member) || !memberOf(t, r, bears, bear) {
		t.Fatalf("a refused kick changed the clan: member_count = %d", count)
	}
	checkClanIsConsistent(t, r, clan)
	checkClanIsConsistent(t, r, bears)
}

// A kick and the same player leaving at the same time: exactly one of them removes the
// player, and member_count stays right.
func TestKickMemberWhileThePlayerLeaves(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Race%d", round))
		ext := fmt.Sprintf("u_%d", round)
		player := joinAs(t, r, game, clan, ext)

		var wg sync.WaitGroup
		var kickErr, leaveErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, kickErr = r.KickMember(ctx, game, clan, fmt.Sprintf("owner_Race%d", round), player)
		}()
		go func() { defer wg.Done(); _, leaveErr = r.LeaveClan(ctx, game, clan, ext) }()
		wg.Wait()

		switch {
		case kickErr == nil && errors.Is(leaveErr, ErrNotInClan):
		case leaveErr == nil && errors.Is(kickErr, ErrMemberNotFound):
		default:
			t.Fatalf("round %d: kick = %v, leave = %v; want exactly one to succeed", round, kickErr, leaveErr)
		}
		var count int
		_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
		if count != 1 {
			t.Fatalf("round %d: member_count = %d, want 1 (the owner)", round, count)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

// The owner kicks while leaving at the same time: the leave hands the clan to the oldest
// member, so a kick after it is no longer the owner's to make. Either order keeps the clan right.
func TestKickMemberWhileTheOwnerLeaves(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		name := fmt.Sprintf("Shift%d", round)
		clan := ownClan(t, r, game, name)
		joinAs(t, r, game, clan, fmt.Sprintf("heir_%d", round))
		target := joinAs(t, r, game, clan, fmt.Sprintf("t_%d", round))

		var wg sync.WaitGroup
		var kickErr, leaveErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, kickErr = r.KickMember(ctx, game, clan, "owner_"+name, target) }()
		go func() { defer wg.Done(); _, leaveErr = r.LeaveClan(ctx, game, clan, "owner_"+name) }()
		wg.Wait()

		if leaveErr != nil {
			t.Fatalf("round %d: leave = %v", round, leaveErr)
		}
		want := 1 // kick first: heir is alone
		if kickErr != nil {
			if !errors.Is(kickErr, ErrNotClanOwner) {
				t.Fatalf("round %d: kick = %v, want nil or ErrNotClanOwner", round, kickErr)
			}
			want = 2 // leave first: heir and target remain
		}
		var count int
		_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
		if count != want {
			t.Fatalf("round %d: member_count = %d, want %d", round, count, want)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

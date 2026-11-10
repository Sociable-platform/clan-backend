package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func clanName(t *testing.T, r *Repository, clanID string) string {
	t.Helper()
	var name string
	if err := r.pool.QueryRow(context.Background(), "select name from clans where id = $1", clanID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRenameClanSavesTheNewName(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	joinAs(t, r, game, clan, "u_1")

	got, err := r.RenameClan(ctx, game, clan, "owner_Wolves", "Grey Wolves")
	if err != nil {
		t.Fatal(err)
	}
	want := RenameResult{Clan: Clan{ID: clan, Name: "Grey Wolves", MemberCount: 2, MaxMembers: 10}, PreviousName: "Wolves", Changed: true}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if n := clanName(t, r, clan); n != "Grey Wolves" {
		t.Fatalf("saved name = %q", n)
	}
	// the old name is free for another clan now
	if _, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "u_new"), "Wolves"); err != nil {
		t.Fatalf("creating a clan with the old name: %v", err)
	}
	checkClanIsConsistent(t, r, clan)
}

func TestRenameClanSameNameChangesNothing(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")

	got, err := r.RenameClan(ctx, game, clan, "owner_Wolves", "Wolves")
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed || got.PreviousName != "Wolves" || got.Clan.Name != "Wolves" {
		t.Fatalf("same name: got %+v, want unchanged", got)
	}

	// only the case changes: that is a new name, and it does not clash with itself
	got, err = r.RenameClan(ctx, game, clan, "owner_Wolves", "WOLVES")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.PreviousName != "Wolves" || clanName(t, r, clan) != "WOLVES" {
		t.Fatalf("case change: got %+v, saved %q", got, clanName(t, r, clan))
	}
}

func TestRenameClanRefusals(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	otherGame := newGameID(t)
	defer cleanup(game)
	defer cleanup(otherGame)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	joinAs(t, r, game, clan, "u_member")
	ownClan(t, r, game, "Bears")
	foreign := ownClan(t, r, otherGame, "Lions")

	cases := []struct {
		name, clan, caller, newName string
		want                        error
	}{
		{"unknown clan", "00000000-0000-4000-8000-000000000000", "owner_Wolves", "Foxes", ErrClanNotFound},
		{"clan ID is not a UUID", "nope", "owner_Wolves", "Foxes", ErrClanNotFound},
		{"clan of another game", foreign, "owner_Lions", "Foxes", ErrClanNotFound},
		{"a member is not the owner", clan, "u_member", "Foxes", ErrNotClanOwner},
		{"an outsider is not the owner", clan, "u_nobody", "Foxes", ErrNotClanOwner},
		{"another clan's owner is not the owner", clan, "owner_Bears", "Foxes", ErrNotClanOwner},
		{"the name is taken", clan, "owner_Wolves", "Bears", ErrClanNameTaken},
		{"the name is taken in other case", clan, "owner_Wolves", "bEARS", ErrClanNameTaken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := r.RenameClan(ctx, game, c.clan, c.caller, c.newName); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if n := clanName(t, r, clan); n != "Wolves" {
		t.Fatalf("a refused rename changed the name to %q", n)
	}
	// another game may use the same name
	if _, err := r.RenameClan(ctx, otherGame, foreign, "owner_Lions", "Wolves"); err != nil {
		t.Fatalf("a name used in another game: %v", err)
	}
}

// Two clans take the same new name at the same time: exactly one gets it.
func TestRenameClanSameNameAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 10; round++ {
		a := ownClan(t, r, game, fmt.Sprintf("A%d", round))
		b := ownClan(t, r, game, fmt.Sprintf("B%d", round))
		target := fmt.Sprintf("Same%d", round)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = r.RenameClan(ctx, game, a, fmt.Sprintf("owner_A%d", round), target)
		}()
		go func() {
			defer wg.Done()
			_, errs[1] = r.RenameClan(ctx, game, b, fmt.Sprintf("owner_B%d", round), target)
		}()
		wg.Wait()
		ok, taken := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrClanNameTaken):
				taken++
			default:
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
		if ok != 1 || taken != 1 {
			t.Fatalf("round %d: %d renamed, %d refused; want 1 and 1", round, ok, taken)
		}
	}
}

// The owner renames while leaving at the same time: after the leave the old owner is no
// longer allowed to rename, so either the rename happened first or it is refused.
func TestRenameClanWhileTheOwnerLeaves(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		name := fmt.Sprintf("Old%d", round)
		clan := ownClan(t, r, game, name)
		joinAs(t, r, game, clan, fmt.Sprintf("heir_%d", round))
		var wg sync.WaitGroup
		var renameErr, leaveErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, renameErr = r.RenameClan(ctx, game, clan, "owner_"+name, fmt.Sprintf("New%d", round))
		}()
		go func() { defer wg.Done(); _, leaveErr = r.LeaveClan(ctx, game, clan, "owner_"+name) }()
		wg.Wait()
		if leaveErr != nil {
			t.Fatalf("round %d: leave = %v", round, leaveErr)
		}
		want := fmt.Sprintf("New%d", round)
		if renameErr != nil {
			if !errors.Is(renameErr, ErrNotClanOwner) {
				t.Fatalf("round %d: rename = %v", round, renameErr)
			}
			want = name
		}
		if got := clanName(t, r, clan); got != want {
			t.Fatalf("round %d: name = %q, want %q", round, got, want)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

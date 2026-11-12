package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// roleOf returns a member's stored role.
func roleOf(t *testing.T, r *Repository, clanID, playerID string) string {
	t.Helper()
	var role string
	if err := r.pool.QueryRow(context.Background(),
		"select role from clan_members where clan_id = $1 and player_id = $2", clanID, playerID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	return role
}

// setRole stores a role without any rules, to set up a test.
func setRole(t *testing.T, r *Repository, clanID, playerID, role string) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		"update clan_members set role = $3 where clan_id = $1 and player_id = $2", clanID, playerID, role); err != nil {
		t.Fatal(err)
	}
}

// to returns a decide func that always gives role, and records what it was shown.
func to(role string, seen *[2]MemberRole) func(actor, target MemberRole) (string, error) {
	return func(actor, target MemberRole) (string, error) {
		if seen != nil {
			*seen = [2]MemberRole{actor, target}
		}
		return role, nil
	}
}

func TestJoinClanStoresTheRoleItIsGiven(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clan := ownClan(t, r, game, "Wolves")
	p := newPlayer(t, r, game, "u_1")
	if _, err := r.JoinClan(context.Background(), game, clan, p, testMaxMembers, "recruit"); err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, r, clan, p); got != "recruit" {
		t.Fatalf("role = %q, want recruit", got)
	}
}

func TestTheTableOnlyTakesRoleIDs(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clan := ownClan(t, r, game, "Wolves")
	p := joinAs(t, r, game, clan, "u_1")
	for _, bad := range []string{"", "Elder", "co--leader", "-elder", "9lives", "elder!", "abcdefghijklmnopqrstuvwxy"} { // the last one is 25 characters
		if _, err := r.pool.Exec(context.Background(),
			"update clan_members set role = $3 where clan_id = $1 and player_id = $2", clan, p, bad); err == nil {
			t.Errorf("role %q was accepted", bad)
		}
	}
	for _, ok := range []string{"elder", "co-leader", "rank2", "abcdefghijklmnopqrstuvwx"} { // the last one is 24 characters
		setRole(t, r, clan, p, ok)
	}
}

func TestChangeRoleSavesTheNewRole(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	p := joinAs(t, r, game, clan, "u_1")

	var seen [2]MemberRole
	got, err := r.ChangeRole(ctx, game, clan, "owner_Wolves", p, to("elder", &seen))
	if err != nil {
		t.Fatal(err)
	}
	want := RoleChange{Clan: ClanRef{ID: clan, Name: "Wolves"}, Player: ClanPlayer{ID: p, DisplayName: "u_1"},
		PreviousRoleID: "member", NewRoleID: "elder", ChangedBy: ClanPlayer{ID: owner, DisplayName: "owner_Wolves"}}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// decide saw both members as they are, with who owns the clan
	if a, tg := seen[0], seen[1]; !a.IsOwner || a.Player.ID != owner || a.RoleID != "owner" || tg.IsOwner || tg.RoleID != "member" {
		t.Fatalf("decide was shown actor %+v, target %+v", a, tg)
	}
	if roleOf(t, r, clan, p) != "elder" {
		t.Fatal("the new role was not saved")
	}
	checkClanIsConsistent(t, r, clan)
}

func TestChangeRoleRefusals(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	p := joinAs(t, r, game, clan, "u_1")
	bears := ownClan(t, r, game, "Bears")
	bear := joinAs(t, r, game, bears, "u_bear")
	foreign := ownClan(t, r, other, "Lions")
	no := errors.New("the roles say no")
	refuse := func(MemberRole, MemberRole) (string, error) { return "", no }

	cases := []struct {
		name                 string
		clan, caller, player string
		decide               func(MemberRole, MemberRole) (string, error)
		want                 error
	}{
		{"unknown clan", "00000000-0000-4000-8000-000000000000", "owner_Wolves", p, to("elder", nil), ErrClanNotFound},
		{"clan of another game", foreign, "owner_Lions", p, to("elder", nil), ErrClanNotFound},
		{"the caller is not in the clan", clan, "u_bear", p, to("elder", nil), ErrNotAllowed},
		{"the caller names themselves", clan, "u_1", p, to("elder", nil), ErrNotAllowed},
		{"the player is in another clan", clan, "owner_Wolves", bear, to("elder", nil), ErrMemberNotFound},
		{"player ID is not a UUID", clan, "owner_Wolves", "u_1", to("elder", nil), ErrMemberNotFound},
		{"decide refuses: its error comes back", clan, "owner_Wolves", p, refuse, no},
		{"nobody else gets the owner's mark", clan, "owner_Wolves", p, to("owner", nil), ErrNotAllowed},
		{"the owner's role never changes", clan, "u_1", owner, to("elder", nil), ErrNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := r.ChangeRole(ctx, game, c.clan, c.caller, c.player, c.decide); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if roleOf(t, r, clan, p) != "member" || roleOf(t, r, clan, owner) != "owner" {
		t.Fatal("a refused change was saved")
	}
	checkClanIsConsistent(t, r, clan)
}

// Rename and the owner check follow clans.owner_id, not the role text: a member whose role
// happens to be stored as "owner" (a game named a role so) is not the owner.
func TestOnlyTheRealOwnerCountsAsOwner(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clan := ownClan(t, r, game, "Wolves")
	p := joinAs(t, r, game, clan, "u_1")
	setRole(t, r, clan, p, "owner")
	if _, err := r.RenameClan(context.Background(), game, clan, "u_1", "Foxes"); !errors.Is(err, ErrNotClanOwner) {
		t.Fatalf("rename by a member stored as owner: %v, want ErrNotClanOwner", err)
	}
	var seen [2]MemberRole
	_, _ = r.ChangeRole(context.Background(), game, clan, "u_1", ownerOf(t, r, clan), func(a, tg MemberRole) (string, error) {
		seen = [2]MemberRole{a, tg}
		return "", errors.New("stop")
	})
	if seen[0].IsOwner || !seen[1].IsOwner {
		t.Fatalf("the rules were told actor %+v, target %+v", seen[0], seen[1])
	}
}

// Kick follows the rules it is given; here: a co-leader may kick a member.
func TestKickMemberByRole(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	co := joinAs(t, r, game, clan, "u_co")
	setRole(t, r, clan, co, "co-leader")
	m := joinAs(t, r, game, clan, "u_m")
	coLeaderKicksMembers := func(a, tg MemberRole) bool { return a.RoleID == "co-leader" && tg.RoleID == "member" }

	if _, err := r.KickMember(ctx, game, clan, "u_m", co, coLeaderKicksMembers); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("member kicks co-leader: %v, want ErrNotAllowed", err)
	}
	got, err := r.KickMember(ctx, game, clan, "u_co", m, coLeaderKicksMembers)
	if err != nil {
		t.Fatal(err)
	}
	if got.KickedBy == nil || got.KickedBy.ID != co || got.Player.ID != m {
		t.Fatalf("got %+v (kicked by %+v)", got, got.KickedBy)
	}
	checkClanIsConsistent(t, r, clan)
}

// A role change and a kick of the same member at the same moment: the lock puts one after
// the other. Either the member is gone (kick first, then "member not found"), or they were
// changed and then kicked.
func TestChangeRoleWhileTheMemberIsKicked(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		name := fmt.Sprintf("K%d", round)
		clan := ownClan(t, r, game, name)
		p := joinAs(t, r, game, clan, fmt.Sprintf("u_%d", round))
		var wg sync.WaitGroup
		var changeErr, kickErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, changeErr = r.ChangeRole(ctx, game, clan, "owner_"+name, p, to("elder", nil))
		}()
		go func() { defer wg.Done(); _, kickErr = r.KickMember(ctx, game, clan, "owner_"+name, p, ownerOnly) }()
		wg.Wait()
		if kickErr != nil {
			t.Fatalf("round %d: kick = %v", round, kickErr)
		}
		if changeErr != nil && !errors.Is(changeErr, ErrMemberNotFound) {
			t.Fatalf("round %d: change = %v, want nil or ErrMemberNotFound", round, changeErr)
		}
		if memberOf(t, r, clan, p) {
			t.Fatalf("round %d: the kicked member is still in the clan", round)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

// The owner changes a role while leaving: after the leave the old owner is not in the clan,
// so the change is refused; before it, the change is kept. Either way the clan is whole.
func TestChangeRoleWhileTheActorLeaves(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		name := fmt.Sprintf("L%d", round)
		clan := ownClan(t, r, game, name)
		heir := joinAs(t, r, game, clan, fmt.Sprintf("h_%d", round))
		p := joinAs(t, r, game, clan, fmt.Sprintf("u_%d", round))
		setJoinedAt(t, r, clan, heir, 10)
		var wg sync.WaitGroup
		var changeErr, leaveErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, changeErr = r.ChangeRole(ctx, game, clan, "owner_"+name, p, to("elder", nil))
		}()
		go func() { defer wg.Done(); _, leaveErr = r.LeaveClan(ctx, game, clan, "owner_"+name) }()
		wg.Wait()
		if leaveErr != nil {
			t.Fatalf("round %d: leave = %v", round, leaveErr)
		}
		want := "elder"
		if changeErr != nil {
			if !errors.Is(changeErr, ErrNotAllowed) {
				t.Fatalf("round %d: change = %v, want nil or ErrNotAllowed", round, changeErr)
			}
			want = "member"
		}
		if got := roleOf(t, r, clan, p); got != want {
			t.Fatalf("round %d: role = %s, want %s", round, got, want)
		}
		if ownerOf(t, r, clan) != heir {
			t.Fatalf("round %d: the oldest member should own the clan now", round)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

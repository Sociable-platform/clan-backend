package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// Who may decide, as the tests ask for it: only the owner, or anybody.
var (
	onlyOwner = func(m MemberRole) bool { return m.IsOwner }
	anyone    = func(MemberRole) bool { return true }
)

// closedClan creates a closed clan owned by a new player "owner_<name>" and returns its ID.
func closedClan(t *testing.T, r *Repository, game, name string) string {
	t.Helper()
	c, err := r.CreateClan(context.Background(), game, newPlayer(t, r, game, "owner_"+name), NewClan{Name: name, JoinPolicy: PolicyClosed})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// ask makes player ext (created if needed) request to join clanID, and returns the request's ID.
func ask(t *testing.T, r *Repository, game, clanID, ext string) string {
	t.Helper()
	res, err := r.RequestJoin(context.Background(), game, clanID, newPlayer(t, r, game, ext), testMaxMembers, 50, 5)
	if err != nil {
		t.Fatalf("%s asks to join: %v", ext, err)
	}
	return res.Request.ID
}

func pending(t *testing.T, r *Repository, game string) int {
	t.Helper()
	return countRows(t, r, "clan_join_requests", game)
}

func TestCreateClanTakesAJoinPolicyAndDefaultsToOpen(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	open, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "a"), NewClan{Name: "Open Ones"})
	if err != nil || open.JoinPolicy != PolicyOpen {
		t.Fatalf("a clan made without a policy: %+v, %v; want open", open, err)
	}
	closed, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "b"), NewClan{Name: "Closed Ones", JoinPolicy: PolicyClosed})
	if err != nil || closed.JoinPolicy != PolicyClosed {
		t.Fatalf("a closed clan: %+v, %v", closed, err)
	}
	if _, err := r.pool.Exec(ctx, `update clans set join_policy = 'secret' where id = $1`, open.ID); err == nil {
		t.Error("the table accepted a policy that is neither open nor closed")
	}
	pc, err := r.MyClan(ctx, game, "b")
	if err != nil || pc.Clan.JoinPolicy != PolicyClosed {
		t.Errorf("MyClan = %+v, %v; want the policy", pc, err)
	}
}

func TestJoinClanRefusesAClosedClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	closed := closedClan(t, r, game, "Wolves")

	_, err := r.JoinClan(ctx, game, closed, newPlayer(t, r, game, "p1"), testMaxMembers, "member")
	if !errors.Is(err, ErrClanClosed) {
		t.Fatalf("join a closed clan: %v, want ErrClanClosed", err)
	}
	if got, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 10}); len(got) != 1 || got[0].MemberCount != 1 {
		t.Errorf("the clan = %+v, want it unchanged with its owner only", got)
	}
}

func TestRequestJoinRules(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	closed := closedClan(t, r, game, "Wolves")
	open := ownClan(t, r, game, "Foxes")

	res, err := r.RequestJoin(ctx, game, closed, newPlayer(t, r, game, "rashad"), testMaxMembers, 50, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Request.ID == "" || res.Request.Player.DisplayName != "rashad" || res.Request.CreatedAt.IsZero() ||
		res.Clan.ID != closed || res.Clan.Name != "Wolves" {
		t.Errorf("result = %+v", res)
	}
	if len(res.Members) != 1 || !res.Members[0].IsOwner || res.Members[0].RoleID != "owner" {
		t.Errorf("members = %+v, want the owner, so the service can tell who may decide", res.Members)
	}

	for name, tc := range map[string]struct {
		clan, player string
		game         string
		want         error
	}{
		"the same player again":               {closed, "rashad", game, ErrAlreadyRequested},
		"an open clan":                        {open, "leyla", game, ErrClanOpen},
		"a player who has a clan":             {closed, "owner_Foxes", game, ErrAlreadyInClan},
		"a clan that does not exist":          {"00000000-0000-4000-8000-000000000000", "leyla", game, ErrClanNotFound},
		"an ID that is not a UUID":            {"not-a-uuid", "leyla", game, ErrClanNotFound},
		"a clan of another game is not found": {closed, "leyla", other, ErrClanNotFound},
	} {
		_, err := r.RequestJoin(ctx, tc.game, tc.clan, newPlayer(t, r, tc.game, tc.player), testMaxMembers, 50, 5)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests, want the one", n)
	}
}

func TestRequestJoinLimits(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	// A full clan takes no requests: there would be nothing to accept.
	full := closedClan(t, r, game, "Tiny")
	if _, err := r.RequestJoin(ctx, game, full, newPlayer(t, r, game, "late"), 1, 50, 5); !errors.Is(err, ErrClanFull) {
		t.Errorf("a full clan: %v, want ErrClanFull", err)
	}

	// A clan takes at most perClan requests.
	crowded := closedClan(t, r, game, "Crowded")
	for i := 0; i < 2; i++ {
		if _, err := r.RequestJoin(ctx, game, crowded, newPlayer(t, r, game, fmt.Sprintf("c%d", i)), testMaxMembers, 2, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.RequestJoin(ctx, game, crowded, newPlayer(t, r, game, "c2"), testMaxMembers, 2, 5); !errors.Is(err, ErrClanRequestsFull) {
		t.Errorf("a clan with too many requests: %v, want ErrClanRequestsFull", err)
	}

	// A player asks at most perPlayer clans.
	rashad := newPlayer(t, r, game, "rashad")
	for i := 0; i < 3; i++ {
		clan := closedClan(t, r, game, fmt.Sprintf("Clan%d", i))
		if _, err := r.RequestJoin(ctx, game, clan, rashad, testMaxMembers, 50, 3); err != nil {
			t.Fatal(err)
		}
	}
	extra := closedClan(t, r, game, "OneMore")
	if _, err := r.RequestJoin(ctx, game, extra, rashad, testMaxMembers, 50, 3); !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("a player with too many requests: %v, want ErrTooManyRequests", err)
	}
}

func TestAcceptJoinRequestMakesAMemberAndEndsAllTheirRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	foxes := closedClan(t, r, game, "Foxes")
	reqWolves := ask(t, r, game, wolves, "rashad")
	ask(t, r, game, foxes, "rashad") // the same player asks another clan too
	ask(t, r, game, wolves, "leyla")

	d, err := r.AcceptJoinRequest(ctx, game, wolves, reqWolves, "owner_Wolves", testMaxMembers, "member", onlyOwner)
	if err != nil {
		t.Fatal(err)
	}
	if d.Clan.ID != wolves || d.Player.DisplayName != "rashad" || d.By.DisplayName != "owner_Wolves" || d.Joined.MemberCount != 2 {
		t.Errorf("decision = %+v", d)
	}
	if !memberOf(t, r, wolves, d.Player.ID) {
		t.Error("the player is not a member")
	}
	var role string
	if err := r.pool.QueryRow(ctx, `select role from clan_members where clan_id = $1 and player_id = $2`, wolves, d.Player.ID).Scan(&role); err != nil || role != "member" {
		t.Errorf("role = %q, %v; want the role the service chose", role, err)
	}
	checkClanIsConsistent(t, r, wolves)
	// Rashad's other request went with it; Leyla's stays.
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests, want only Leyla's", n)
	}
	// Accepting it again: it is gone.
	if _, err := r.AcceptJoinRequest(ctx, game, wolves, reqWolves, "owner_Wolves", testMaxMembers, "member", onlyOwner); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("accepting twice: %v, want ErrRequestNotFound", err)
	}
}

func TestAcceptJoinRequestChecks(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	foxes := closedClan(t, r, game, "Foxes")
	req := ask(t, r, game, wolves, "rashad")
	foxReq := ask(t, r, game, foxes, "leyla")
	joinableAs(t, r, game, wolves, "member1") // a plain member: the owner lets them in through an open window below

	for name, tc := range map[string]struct {
		game, clan, req, actor string
		allowed                func(MemberRole) bool
		want                   error
	}{
		"a member the rule does not allow": {game, wolves, req, "member1", onlyOwner, ErrNotAllowed},
		"a player who is not in the clan":  {game, wolves, req, "owner_Foxes", anyone, ErrNotAllowed},
		"a request of another clan":        {game, wolves, foxReq, "owner_Wolves", onlyOwner, ErrRequestNotFound},
		"a request ID that is not a UUID":  {game, wolves, "nope", "owner_Wolves", onlyOwner, ErrRequestNotFound},
		"a clan that does not exist":       {game, "00000000-0000-4000-8000-000000000000", req, "owner_Wolves", onlyOwner, ErrClanNotFound},
		"the same IDs from another game":   {other, wolves, req, "owner_Wolves", onlyOwner, ErrClanNotFound},
	} {
		_, err := r.AcceptJoinRequest(ctx, tc.game, tc.clan, tc.req, tc.actor, testMaxMembers, "member", tc.allowed)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if n := pending(t, r, game); n != 2 {
		t.Errorf("%d pending requests after the refusals, want both still there", n)
	}
}

// joinableAs puts a player into a closed clan the way the tests need a plain member: through a request.
func joinableAs(t *testing.T, r *Repository, game, clanID, ext string) {
	t.Helper()
	id := ask(t, r, game, clanID, ext)
	if _, err := r.AcceptJoinRequest(context.Background(), game, clanID, id, "owner_"+nameOfClan(t, r, clanID), testMaxMembers, "member", onlyOwner); err != nil {
		t.Fatal(err)
	}
}

func nameOfClan(t *testing.T, r *Repository, clanID string) string {
	t.Helper()
	var n string
	if err := r.pool.QueryRow(context.Background(), `select name from clans where id = $1`, clanID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAcceptJoinRequestOfAFullClanKeepsTheRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	req := ask(t, r, game, wolves, "rashad")

	// The game lowered clan.maxMembers to 1 meanwhile: the owner fills it.
	if _, err := r.AcceptJoinRequest(ctx, game, wolves, req, "owner_Wolves", 1, "member", onlyOwner); !errors.Is(err, ErrClanFull) {
		t.Fatalf("a full clan: %v, want ErrClanFull", err)
	}
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests, want the request kept", n)
	}
}

func TestAcceptJoinRequestOfAPlayerWhoHasAClanNowDropsIt(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	req := ask(t, r, game, wolves, "rashad")
	// A clan was joined without going through joinClan: only a race could leave the request behind.
	if _, err := r.pool.Exec(ctx, `insert into clans (game_id, name, owner_id) values ($1, 'Elsewhere', $2)`, game, newPlayer(t, r, game, "boss")); err != nil {
		t.Fatal(err)
	}
	var elsewhere string
	if err := r.pool.QueryRow(ctx, `select id::text from clans where name = 'Elsewhere'`).Scan(&elsewhere); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `insert into clan_members (clan_id, player_id, game_id, role) values ($1, $2, $3, 'member')`,
		elsewhere, newPlayer(t, r, game, "rashad"), game); err != nil {
		t.Fatal(err)
	}

	if _, err := r.AcceptJoinRequest(ctx, game, wolves, req, "owner_Wolves", testMaxMembers, "member", onlyOwner); !errors.Is(err, ErrAlreadyInClan) {
		t.Fatalf("accept: %v, want ErrAlreadyInClan", err)
	}
	if n := pending(t, r, game); n != 0 {
		t.Errorf("%d pending requests, want the stale request gone", n)
	}
}

// Five players asked, one place is free, and the leaders accept all five at the same moment: the clan lock lets one in.
func TestAcceptJoinRequestsAtTheSameTimeNeverOverfill(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	wolves := closedClan(t, r, game, "Wolves")
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, ask(t, r, game, wolves, fmt.Sprintf("p%d", i)))
	}

	ok, full := race(t, len(ids), func(i int) error {
		_, err := r.AcceptJoinRequest(context.Background(), game, wolves, ids[i], "owner_Wolves", 2, "member", onlyOwner)
		return err
	}, ErrClanFull)
	if ok != 1 || full != 4 {
		t.Fatalf("%d accepted and %d refused as full, want 1 and 4", ok, full)
	}
	checkClanIsConsistent(t, r, wolves)
	var members int
	if err := r.pool.QueryRow(context.Background(), `select member_count from clans where id = $1`, wolves).Scan(&members); err != nil || members != 2 {
		t.Errorf("member_count = %d, %v; want 2", members, err)
	}
}

func TestDeclineAndCancelDeleteTheRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	req := ask(t, r, game, wolves, "rashad")
	ask(t, r, game, wolves, "leyla")

	d, err := r.DeclineJoinRequest(ctx, game, wolves, req, "owner_Wolves", onlyOwner)
	if err != nil || d.Player.DisplayName != "rashad" || d.By.DisplayName != "owner_Wolves" || d.Clan.Name != "Wolves" {
		t.Fatalf("decline = %+v, %v", d, err)
	}
	if _, err := r.DeclineJoinRequest(ctx, game, wolves, req, "owner_Wolves", onlyOwner); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("declining twice: %v, want ErrRequestNotFound", err)
	}
	// The player may ask again.
	if _, err := r.RequestJoin(ctx, game, wolves, d.Player.ID, testMaxMembers, 50, 5); err != nil {
		t.Errorf("asking again after a decline: %v", err)
	}

	if err := r.CancelJoinRequest(ctx, game, wolves, "leyla"); err != nil {
		t.Fatal(err)
	}
	if err := r.CancelJoinRequest(ctx, game, wolves, "leyla"); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("cancelling twice: %v, want ErrRequestNotFound", err)
	}
	if err := r.CancelJoinRequest(ctx, game, "00000000-0000-4000-8000-000000000000", "leyla"); !errors.Is(err, ErrClanNotFound) {
		t.Errorf("cancelling at a clan that does not exist: %v, want ErrClanNotFound", err)
	}
	if err := r.CancelJoinRequest(ctx, game, "not-a-uuid", "leyla"); !errors.Is(err, ErrClanNotFound) {
		t.Errorf("cancelling at an ID that is not a UUID: %v, want ErrClanNotFound", err)
	}
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests, want Rashad's new one only", n)
	}
}

func TestListJoinRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	first := ask(t, r, game, wolves, "rashad")
	second := ask(t, r, game, wolves, "leyla")
	joinableAs(t, r, game, wolves, "member1")

	got, err := r.ListJoinRequests(ctx, game, wolves, "owner_Wolves", onlyOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first || got[1].ID != second || got[0].Player.DisplayName != "rashad" || got[0].CreatedAt.IsZero() {
		t.Errorf("list = %+v, want Rashad's then Leyla's", got)
	}
	if _, err := r.ListJoinRequests(ctx, game, wolves, "member1", onlyOwner); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a member the rule does not allow: %v, want ErrNotAllowed", err)
	}
	if _, err := r.ListJoinRequests(ctx, game, wolves, "stranger", anyone); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a player who is not in the clan: %v, want ErrNotAllowed", err)
	}
	if _, err := r.ListJoinRequests(ctx, game, "not-a-uuid", "owner_Wolves", onlyOwner); !errors.Is(err, ErrClanNotFound) {
		t.Errorf("a bad clan ID: %v, want ErrClanNotFound", err)
	}
	if got, err := r.ListJoinRequests(ctx, game, wolves, "member1", anyone); err != nil || len(got) != 2 {
		t.Errorf("with a rule that allows a plain member: %v, %v; want the two requests", got, err)
	}
}

func TestListClansMarksOnlyTheViewersRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	closedClan(t, r, game, "Foxes")
	ask(t, r, game, wolves, "rashad")

	for viewer, want := range map[string]map[string]bool{
		"rashad":  {"Wolves": true, "Foxes": false},
		"leyla":   {"Wolves": false, "Foxes": false},
		"":        {"Wolves": false, "Foxes": false},
		"unknown": {"Wolves": false, "Foxes": false},
	} {
		got, err := r.ListClans(ctx, game, "", ListClansOptions{Limit: 10, ViewerExternalID: viewer})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range got {
			if c.Requested != want[c.Name] || c.JoinPolicy != PolicyClosed {
				t.Errorf("viewer %q sees %s requested=%v policy=%q, want requested=%v and closed", viewer, c.Name, c.Requested, c.JoinPolicy, want[c.Name])
			}
		}
	}
}

func TestHavingAClanEndsPendingRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := closedClan(t, r, game, "Wolves")
	foxes := closedClan(t, r, game, "Foxes")
	open := ownClan(t, r, game, "Bears")

	// Joining an open clan.
	joiner := newPlayer(t, r, game, "joiner")
	for _, c := range []string{wolves, foxes} {
		if _, err := r.RequestJoin(ctx, game, c, joiner, testMaxMembers, 50, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.JoinClan(ctx, game, open, joiner, testMaxMembers, "member"); err != nil {
		t.Fatal(err)
	}
	if n := pending(t, r, game); n != 0 {
		t.Errorf("%d pending requests after joining an open clan, want none", n)
	}

	// Creating a clan.
	maker := newPlayer(t, r, game, "maker")
	if _, err := r.RequestJoin(ctx, game, wolves, maker, testMaxMembers, 50, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateClan(ctx, game, maker, NewClan{Name: "Mine"}); err != nil {
		t.Fatal(err)
	}
	if n := pending(t, r, game); n != 0 {
		t.Errorf("%d pending requests after creating a clan, want none", n)
	}
}

func TestSetJoinPolicy(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	wolves := ownClan(t, r, game, "Wolves")
	joinAs(t, r, game, wolves, "member1")

	c, err := r.SetJoinPolicy(ctx, game, wolves, "owner_Wolves", PolicyClosed)
	if err != nil || c.JoinPolicy != PolicyClosed || c.Name != "Wolves" || c.MemberCount != 2 {
		t.Fatalf("close: %+v, %v", c, err)
	}
	if _, err := r.JoinClan(ctx, game, wolves, newPlayer(t, r, game, "late"), testMaxMembers, "member"); !errors.Is(err, ErrClanClosed) {
		t.Errorf("joining after it closed: %v, want ErrClanClosed", err)
	}
	if _, err := r.SetJoinPolicy(ctx, game, wolves, "member1", PolicyOpen); !errors.Is(err, ErrNotClanOwner) {
		t.Errorf("a member opens it: %v, want ErrNotClanOwner", err)
	}
	if _, err := r.SetJoinPolicy(ctx, game, wolves, "stranger", PolicyOpen); !errors.Is(err, ErrNotClanOwner) {
		t.Errorf("a stranger opens it: %v, want ErrNotClanOwner", err)
	}
	if _, err := r.SetJoinPolicy(ctx, other, wolves, "owner_Wolves", PolicyOpen); !errors.Is(err, ErrClanNotFound) {
		t.Errorf("from another game: %v, want ErrClanNotFound", err)
	}
	if _, err := r.SetJoinPolicy(ctx, game, "not-a-uuid", "owner_Wolves", PolicyOpen); !errors.Is(err, ErrClanNotFound) {
		t.Errorf("a bad clan ID: %v, want ErrClanNotFound", err)
	}

	// Opening it again keeps the pending requests; the same policy changes nothing.
	req := ask(t, r, game, wolves, "rashad")
	if c, err = r.SetJoinPolicy(ctx, game, wolves, "owner_Wolves", PolicyOpen); err != nil || c.JoinPolicy != PolicyOpen {
		t.Fatalf("open: %+v, %v", c, err)
	}
	if c, err = r.SetJoinPolicy(ctx, game, wolves, "owner_Wolves", PolicyOpen); err != nil || c.JoinPolicy != PolicyOpen {
		t.Fatalf("open again: %+v, %v", c, err)
	}
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests after opening, want the one kept (%s)", n, req)
	}
	// A rename keeps the policy.
	if _, err := r.SetJoinPolicy(ctx, game, wolves, "owner_Wolves", PolicyClosed); err != nil {
		t.Fatal(err)
	}
	res, err := r.RenameClan(ctx, game, wolves, "owner_Wolves", "Grey Wolves")
	if err != nil || res.Clan.JoinPolicy != PolicyClosed {
		t.Errorf("rename = %+v, %v; want the policy kept", res.Clan, err)
	}
}

func TestDeletingAPlayerEndsTheirRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	wolves := closedClan(t, r, game, "Wolves")
	ask(t, r, game, wolves, "rashad")
	ask(t, r, game, wolves, "leyla")

	if _, err := r.DeletePlayer(context.Background(), game, "rashad", "Deleted player"); err != nil {
		t.Fatal(err)
	}
	if n := pending(t, r, game); n != 1 {
		t.Errorf("%d pending requests after deleting a player, want only the other player's", n)
	}
}

func TestClosedClanRowsWithoutDatabase(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	for name, err := range map[string]error{
		"RequestJoin":        func() error { _, err := r.RequestJoin(ctx, "g", "c", "p", 1, 1, 1); return err }(),
		"CancelJoinRequest":  r.CancelJoinRequest(ctx, "g", "c", "p"),
		"ListJoinRequests":   func() error { _, err := r.ListJoinRequests(ctx, "g", "c", "p", anyone); return err }(),
		"AcceptJoinRequest":  func() error { _, err := r.AcceptJoinRequest(ctx, "g", "c", "r", "p", 1, "m", anyone); return err }(),
		"DeclineJoinRequest": func() error { _, err := r.DeclineJoinRequest(ctx, "g", "c", "r", "p", anyone); return err }(),
		"SetJoinPolicy":      func() error { _, err := r.SetJoinPolicy(ctx, "g", "c", "p", PolicyOpen); return err }(),
	} {
		if !errors.Is(err, ErrNoDatabase) {
			t.Errorf("%s without a database: %v, want ErrNoDatabase", name, err)
		}
	}
}

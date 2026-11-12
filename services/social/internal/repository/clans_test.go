package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// testMaxMembers is the clan size the tests use where the limit does not matter: the default.
const testMaxMembers = 10

// addClan creates an owner player and a clan in gameID, the way the create-clan
// endpoint will later. Only the columns without defaults are set.
func addClan(t *testing.T, r *Repository, gameID, name string) string {
	t.Helper()
	ctx := context.Background()
	owner, err := r.UpsertPlayer(ctx, gameID, "owner_"+name, "Owner "+name)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = r.pool.QueryRow(ctx,
		`insert into clans (game_id, name, owner_id) values ($1, $2, $3) returning id::text`,
		gameID, name, owner.ID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func names(clans []Clan) []string {
	out := make([]string, 0, len(clans))
	for _, c := range clans {
		out = append(out, c.Name)
	}
	return out
}

func TestListClansOnlySameGame(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newGameID(t), newGameID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()

	addClan(t, r, gameA, "Wolves")
	addClan(t, r, gameB, "Bears")
	addClan(t, r, gameB, "Wolves") // same name, other game

	got, err := r.ListClans(ctx, gameA, "", ListClansOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Wolves" {
		t.Fatalf("game A sees %v, want only its own [Wolves]", names(got))
	}
	if got, _ := r.ListClans(ctx, gameA, "bear", ListClansOptions{Limit: 100}); len(got) != 0 {
		t.Fatalf("searching game B's clan name from game A found %v", names(got))
	}
	if got[0].ID == "" || got[0].MemberCount != 1 {
		t.Fatalf("fields: %+v (want id set, memberCount 1, maxMembers 10 by default)", got[0])
	}
}

func TestListClansSearchAndOrder(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for _, n := range []string{"wolf pack", "Hawks", "WOLVES", "100%_club", "Bears"} {
		addClan(t, r, game, n)
	}
	tests := []struct {
		q    string
		want string
	}{
		{"", "[100%_club Bears Hawks wolf pack WOLVES]"}, // sorted by name, ignoring case
		{"WoL", "[wolf pack WOLVES]"},                    // case-insensitive "contains"
		{"awk", "[Hawks]"},
		{"%", "[100%_club]"}, // % is plain text, not "anything"
		{"_", "[100%_club]"}, // _ is plain text, not "one character"
		{"zebra", "[]"},
	}
	for _, tc := range tests {
		got, err := r.ListClans(ctx, game, tc.q, ListClansOptions{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprint(names(got)); s != tc.want {
			t.Errorf("q=%q: got %s, want %s", tc.q, s, tc.want)
		}
	}

	if got, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 2}); len(got) != 2 {
		t.Errorf("limit 2 returned %d clans", len(got))
	}
	if got, _ := r.ListClans(ctx, newGameID(t), "", ListClansOptions{Limit: 100}); got == nil {
		t.Error("a game without clans must give an empty list, not nil")
	}
}

// countRows counts rows of a table for one game, so tests can check nothing extra was saved.
func countRows(t *testing.T, r *Repository, table, gameID string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(),
		"select count(*) from "+table+" where game_id = $1", gameID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newPlayer(t *testing.T, r *Repository, gameID, externalID string) string {
	t.Helper()
	p, err := r.UpsertPlayer(context.Background(), gameID, externalID, externalID)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestCreateClanSavesClanAndOwner(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	owner := newPlayer(t, r, game, "u_1")

	c, err := r.CreateClan(ctx, game, owner, NewClan{Name: "Wolves"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Name != "Wolves" || c.MemberCount != 1 {
		t.Fatalf("clan: %+v", c)
	}
	var role, gotGame string
	err = r.pool.QueryRow(ctx,
		"select role, game_id::text from clan_members where clan_id = $1 and player_id = $2",
		c.ID, owner).Scan(&role, &gotGame)
	if err != nil || role != "owner" || gotGame != game {
		t.Fatalf("owner membership: role=%q game=%q err=%v", role, gotGame, err)
	}
	if list, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 100}); len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("the new clan should be listed: %+v", list)
	}
}

func TestCreateClanNameTaken(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()

	if _, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "u_1"), NewClan{Name: "Wolves"}); err != nil {
		t.Fatal(err)
	}
	second := newPlayer(t, r, game, "u_2")
	if _, err := r.CreateClan(ctx, game, second, NewClan{Name: "WOLVES"}); !errors.Is(err, ErrClanNameTaken) {
		t.Fatalf("same name, other case: err = %v, want ErrClanNameTaken", err)
	}
	// the failed attempt must not leave anything behind (the transaction was rolled back)
	if n := countRows(t, r, "clans", game); n != 1 {
		t.Fatalf("clans = %d, want 1", n)
	}
	if n := countRows(t, r, "clan_members", game); n != 1 {
		t.Fatalf("members = %d, want 1 (the failed creator must not be a member)", n)
	}
	// the same name in another game is fine
	if _, err := r.CreateClan(ctx, other, newPlayer(t, r, other, "u_1"), NewClan{Name: "Wolves"}); err != nil {
		t.Fatalf("same name in another game: %v", err)
	}
}

func TestCreateClanOwnerAlreadyInClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	owner := newPlayer(t, r, game, "u_1")

	if _, err := r.CreateClan(ctx, game, owner, NewClan{Name: "Wolves"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateClan(ctx, game, owner, NewClan{Name: "Bears"}); !errors.Is(err, ErrAlreadyInClan) {
		t.Fatalf("err = %v, want ErrAlreadyInClan", err)
	}
	if n := countRows(t, r, "clans", game); n != 1 {
		t.Fatalf("clans = %d, want 1 (Bears must not be saved)", n)
	}
	// being in a clan in one game does not matter in another game
	if _, err := r.CreateClan(ctx, other, newPlayer(t, r, other, "u_1"), NewClan{Name: "Bears"}); err != nil {
		t.Fatalf("other game: %v", err)
	}
}

// The early "already in a clan?" check can be passed by two requests at once.
// The database rule must still stop all but one, and leave no clan without its owner.
func TestCreateClanSamePlayerAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	owner := newPlayer(t, r, game, "u_1")

	ok, inClan := race(t, 10, func(i int) error {
		_, err := r.CreateClan(context.Background(), game, owner, NewClan{Name: fmt.Sprintf("Clan %d", i)})
		return err
	}, ErrAlreadyInClan)
	if ok != 1 || inClan != 9 {
		t.Fatalf("ok=%d alreadyInClan=%d, want 1 and 9", ok, inClan)
	}
	if n := countRows(t, r, "clans", game); n != 1 {
		t.Fatalf("clans = %d, want 1 (failed attempts must be rolled back)", n)
	}
}

func TestCreateClanSameNameAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	owners := make([]string, 10)
	for i := range owners {
		owners[i] = newPlayer(t, r, game, fmt.Sprintf("u_%d", i))
	}

	ok, taken := race(t, 10, func(i int) error {
		_, err := r.CreateClan(context.Background(), game, owners[i], NewClan{Name: "Wolves"})
		return err
	}, ErrClanNameTaken)
	if ok != 1 || taken != 9 {
		t.Fatalf("ok=%d nameTaken=%d, want 1 and 9", ok, taken)
	}
	if n := countRows(t, r, "clan_members", game); n != 1 {
		t.Fatalf("members = %d, want 1", n)
	}
}

// race runs fn n times at once and counts successes and errors matching want.
// Any other error fails the test.
func race(t *testing.T, n int, fn func(i int) error, want error) (ok, matched int) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- fn(i)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, want):
			matched++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	return ok, matched
}

// ownClan creates a clan in gameID with a new owner player and returns its ID.
func ownClan(t *testing.T, r *Repository, gameID, name string) string {
	t.Helper()
	c, err := r.CreateClan(context.Background(), gameID, newPlayer(t, r, gameID, "owner_"+name), NewClan{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestJoinClanAddsMember(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	player := newPlayer(t, r, game, "u_2")

	c, err := r.JoinClan(ctx, game, clan, player, testMaxMembers, "member")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != clan || c.Name != "Wolves" || c.MemberCount != 2 {
		t.Fatalf("clan after join: %+v", c)
	}
	var role string
	if err := r.pool.QueryRow(ctx, "select role from clan_members where clan_id = $1 and player_id = $2",
		clan, player).Scan(&role); err != nil || role != "member" {
		t.Fatalf("membership: role=%q err=%v", role, err)
	}
	if list, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 100}); len(list) != 1 || list[0].MemberCount != 2 {
		t.Fatalf("the list should show 2 members: %+v", list)
	}
}

func TestJoinClanNotFound(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newGameID(t), newGameID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()
	clanOfA := ownClan(t, r, gameA, "Wolves")
	playerOfB := newPlayer(t, r, gameB, "u_9")

	for name, clanID := range map[string]string{
		"clan of another game": clanOfA,
		"unknown clan":         newGameID(t), // any random UUID
		"not a UUID":           "abc",
	} {
		if _, err := r.JoinClan(ctx, gameB, clanID, playerOfB, testMaxMembers, "member"); !errors.Is(err, ErrClanNotFound) {
			t.Errorf("%s: err = %v, want ErrClanNotFound", name, err)
		}
	}
	if n := countRows(t, r, "clan_members", gameB); n != 0 {
		t.Fatalf("game B members = %d, want 0", n)
	}
}

func TestJoinClanAlreadyInClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves, bears := ownClan(t, r, game, "Wolves"), ownClan(t, r, game, "Bears")
	player := newPlayer(t, r, game, "u_2")
	if _, err := r.JoinClan(ctx, game, wolves, player, testMaxMembers, "member"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.JoinClan(ctx, game, wolves, player, testMaxMembers, "member"); !errors.Is(err, ErrAlreadyInClan) {
		t.Errorf("same clan again: err = %v, want ErrAlreadyInClan", err)
	}
	if _, err := r.JoinClan(ctx, game, bears, player, testMaxMembers, "member"); !errors.Is(err, ErrAlreadyInClan) {
		t.Errorf("second clan: err = %v, want ErrAlreadyInClan", err)
	}
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", bears).Scan(&count)
	if count != 1 {
		t.Fatalf("Bears member_count = %d, want 1 (failed joins must not count)", count)
	}
}

func TestJoinClanFull(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Duo")
	// the game allows 2 members: the owner and one more
	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_2"), 2, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_3"), 2, "member"); !errors.Is(err, ErrClanFull) {
		t.Fatalf("err = %v, want ErrClanFull", err)
	}
	if n := countRows(t, r, "clan_members", game); n != 2 {
		t.Fatalf("members = %d, want 2", n)
	}
}

// 15 players press "Join" at the same moment on a clan with 9 free places (owner + 9 = 10).
// The lock must let exactly 9 in.
func TestJoinClanAtTheSameTimeNeverOverfills(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clan := ownClan(t, r, game, "Wolves")
	players := make([]string, 15)
	for i := range players {
		players[i] = newPlayer(t, r, game, fmt.Sprintf("u_%d", i))
	}

	ok, full := race(t, len(players), func(i int) error {
		_, err := r.JoinClan(context.Background(), game, clan, players[i], testMaxMembers, "member")
		return err
	}, ErrClanFull)
	if ok != 9 || full != 6 {
		t.Fatalf("joined=%d full=%d, want 9 and 6", ok, full)
	}
	var count int
	_ = r.pool.QueryRow(context.Background(), "select member_count from clans where id = $1", clan).Scan(&count)
	if members := countRows(t, r, "clan_members", game); count != 10 || members != 10 {
		t.Fatalf("member_count=%d, member rows=%d, want 10 and 10", count, members)
	}
}

// One player presses "Join" on two different clans at the same moment: only one may succeed.
func TestJoinTwoClansAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clans := []string{ownClan(t, r, game, "Wolves"), ownClan(t, r, game, "Bears")}
	player := newPlayer(t, r, game, "u_2")

	ok, inClan := race(t, 2, func(i int) error {
		_, err := r.JoinClan(context.Background(), game, clans[i], player, testMaxMembers, "member")
		return err
	}, ErrAlreadyInClan)
	if ok != 1 || inClan != 1 {
		t.Fatalf("ok=%d alreadyInClan=%d, want 1 and 1", ok, inClan)
	}
	var total int
	_ = r.pool.QueryRow(context.Background(),
		"select sum(member_count) from clans where game_id = $1", game).Scan(&total)
	if total != 3 {
		t.Fatalf("member counts add up to %d, want 3 (two owners + one join)", total)
	}
}

func TestMyClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()

	// never seen, and seen but not in a clan
	if _, err := r.MyClan(ctx, game, "u_nobody"); !errors.Is(err, ErrNotInClan) {
		t.Fatalf("unknown player: err = %v, want ErrNotInClan", err)
	}
	ali := newPlayer(t, r, game, "u_ali")
	if _, err := r.MyClan(ctx, game, "u_ali"); !errors.Is(err, ErrNotInClan) {
		t.Fatalf("player without clan: err = %v, want ErrNotInClan", err)
	}

	created, err := r.CreateClan(ctx, game, ali, NewClan{Name: "Wolves"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.MyClan(ctx, game, "u_ali")
	if err != nil || got.Role != "owner" || got.Clan != created {
		t.Fatalf("owner: got %+v, %v; want %+v as owner", got, err, created)
	}

	if _, err := r.JoinClan(ctx, game, created.ID, newPlayer(t, r, game, "u_leyla"), testMaxMembers, "member"); err != nil {
		t.Fatal(err)
	}
	got, err = r.MyClan(ctx, game, "u_leyla")
	if err != nil || got.Role != "member" || got.Clan.ID != created.ID || got.Clan.MemberCount != 2 {
		t.Fatalf("member: got %+v, %v", got, err)
	}

	// the same game player ID in another game is another player, not in a clan
	newPlayer(t, r, other, "u_ali")
	if _, err := r.MyClan(ctx, other, "u_ali"); !errors.Is(err, ErrNotInClan) {
		t.Fatalf("other game: err = %v, want ErrNotInClan", err)
	}
}

func TestListMembers(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	owner := newPlayer(t, r, game, "u_owner")
	c, err := r.CreateClan(ctx, game, owner, NewClan{Name: "Wolves"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"u_b", "u_a", "u_c"} { // joined in this order
		if _, err := r.JoinClan(ctx, game, c.ID, newPlayer(t, r, game, p), testMaxMembers, "member"); err != nil {
			t.Fatal(err)
		}
	}
	// a member who joined before the owner's row (only possible by hand) must still list after the owner
	if _, err := r.pool.Exec(ctx, "update clan_members set joined_at = joined_at - interval '1 hour' where clan_id = $1 and role = 'member' and player_id = (select id from players where game_id = $2 and external_id = 'u_c')", c.ID, game); err != nil {
		t.Fatal(err)
	}

	got, err := r.ListMembers(ctx, game, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range got {
		order = append(order, m.DisplayName+":"+m.Role)
		if m.PlayerID == "" || m.JoinedAt.IsZero() {
			t.Errorf("member without ID or join time: %+v", m)
		}
	}
	if want := "[u_owner:owner u_c:member u_b:member u_a:member]"; fmt.Sprint(order) != want {
		t.Fatalf("order %v, want %s (owner first, then by join time)", order, want)
	}
	if got[0].PlayerID != owner {
		t.Fatalf("owner's playerId = %q, want %q", got[0].PlayerID, owner)
	}
}

func TestListMembersNotFound(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	clanOfGame := ownClan(t, r, game, "Wolves")

	for name, clanID := range map[string]string{
		"clan of another game": clanOfGame,
		"unknown clan":         newGameID(t),
		"not a UUID":           "abc",
	} {
		if _, err := r.ListMembers(context.Background(), other, clanID); !errors.Is(err, ErrClanNotFound) {
			t.Errorf("%s: err = %v, want ErrClanNotFound", name, err)
		}
	}
}

// A clan with exactly one free place and two players pressing "Join" at the same moment:
// one gets in, the other is told the clan is full. Repeated, because a race only shows up
// on some runs.
func TestJoinLastFreePlaceAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Duo%d", round))
		// the game allows 2 members: room for the owner and one more
		players := []string{
			newPlayer(t, r, game, fmt.Sprintf("a_%d", round)),
			newPlayer(t, r, game, fmt.Sprintf("b_%d", round)),
		}

		ok, full := race(t, len(players), func(i int) error {
			_, err := r.JoinClan(ctx, game, clan, players[i], 2, "member")
			return err
		}, ErrClanFull)
		if ok != 1 || full != 1 {
			t.Fatalf("round %d: joined=%d full=%d, want 1 and 1", round, ok, full)
		}

		var count, members int
		_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
		_ = r.pool.QueryRow(ctx, "select count(*) from clan_members where clan_id = $1", clan).Scan(&members)
		if count != 2 || members != 2 {
			t.Fatalf("round %d: member_count=%d, member rows=%d, want 2 and 2", round, count, members)
		}
	}
}

// ---- LeaveClan ----

// checkClanIsConsistent fails the test unless the clan has member_count equal to its member
// rows and exactly one owner, and clans.owner_id is that owner.
func checkClanIsConsistent(t *testing.T, r *Repository, clanID string) {
	t.Helper()
	ctx := context.Background()
	var count, rows, owners int
	var ownerID, ownerRow string
	err := r.pool.QueryRow(ctx,
		`select c.member_count, c.owner_id::text,
		        (select count(*) from clan_members where clan_id = c.id),
		        (select count(*) from clan_members where clan_id = c.id and role = 'owner'),
		        coalesce((select player_id::text from clan_members where clan_id = c.id and role = 'owner'), '')
		 from clans c where c.id = $1`, clanID).Scan(&count, &ownerID, &rows, &owners, &ownerRow)
	if err != nil {
		t.Fatalf("clan %s: %v", clanID, err)
	}
	if count != rows || owners != 1 || ownerID != ownerRow {
		t.Fatalf("clan is inconsistent: member_count=%d member rows=%d owner rows=%d clans.owner_id=%s owner row=%s",
			count, rows, owners, ownerID, ownerRow)
	}
}

func clanExists(t *testing.T, r *Repository, clanID string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from clans where id = $1", clanID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// memberOf reports whether the player (internal ID) has a membership row in the clan.
func memberOf(t *testing.T, r *Repository, clanID, playerID string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(),
		"select count(*) from clan_members where clan_id = $1 and player_id = $2", clanID, playerID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// ownerOf returns the clan's owner_id.
func ownerOf(t *testing.T, r *Repository, clanID string) string {
	t.Helper()
	var id string
	if err := r.pool.QueryRow(context.Background(), "select owner_id::text from clans where id = $1", clanID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// setJoinedAt makes a member's join time deterministic ("hours ago"), because members
// added within the same millisecond would otherwise tie.
func setJoinedAt(t *testing.T, r *Repository, clanID, playerID string, hoursAgo int) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		`update clan_members set joined_at = now() - make_interval(hours => $3) where clan_id = $1 and player_id = $2`,
		clanID, playerID, hoursAgo); err != nil {
		t.Fatal(err)
	}
}

// joinAs joins a new player (external ID ext) to the clan and returns the player's internal ID.
func joinAs(t *testing.T, r *Repository, game, clanID, ext string) string {
	t.Helper()
	id := newPlayer(t, r, game, ext)
	if _, err := r.JoinClan(context.Background(), game, clanID, id, testMaxMembers, "member"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLeaveClanNormalMember(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	joinAs(t, r, game, clan, "u_1")
	leaver := joinAs(t, r, game, clan, "u_2")

	if _, err := r.LeaveClan(ctx, game, clan, "u_2"); err != nil {
		t.Fatal(err)
	}
	if memberOf(t, r, clan, leaver) {
		t.Fatal("the member is still in the clan")
	}
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
	if count != 2 || ownerOf(t, r, clan) != owner {
		t.Fatalf("member_count=%d (want 2), owner changed=%v (want unchanged)", count, ownerOf(t, r, clan) != owner)
	}
	checkClanIsConsistent(t, r, clan)
	if _, err := r.MyClan(ctx, game, "u_2"); !errors.Is(err, ErrNotInClan) {
		t.Fatalf("MyClan after leaving: %v, want ErrNotInClan", err)
	}

	// the player can join another clan afterwards, and can come back to the same one
	other := ownClan(t, r, game, "Bears")
	if _, err := r.JoinClan(ctx, game, other, leaver, testMaxMembers, "member"); err != nil {
		t.Fatalf("joining another clan after leaving: %v", err)
	}
	if _, err := r.LeaveClan(ctx, game, other, "u_2"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClan(ctx, game, clan, leaver, testMaxMembers, "member"); err != nil {
		t.Fatalf("joining the old clan again: %v", err)
	}
	checkClanIsConsistent(t, r, clan)
	checkClanIsConsistent(t, r, other)
}

// LeaveClan tells what it did, so the service can publish clan.member_left or clan.disbanded.
func TestLeaveClanReturnsWhatHappened(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	owner := ownerOf(t, r, clan)
	a := joinAs(t, r, game, clan, "u_a")
	b := joinAs(t, r, game, clan, "u_b")
	setJoinedAt(t, r, clan, a, 20) // a joined before b
	setJoinedAt(t, r, clan, b, 10)

	// a normal member leaves: no new owner, the clan stays
	got, err := r.LeaveClan(ctx, game, clan, "u_b")
	if err != nil {
		t.Fatal(err)
	}
	want := LeaveResult{ClanID: clan, ClanName: "Wolves", Player: ClanPlayer{ID: b, DisplayName: "u_b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("member leaves:\n got %+v\nwant %+v", got, want)
	}

	// the owner leaves: the oldest member takes over and is named
	got, err = r.LeaveClan(ctx, game, clan, "owner_Wolves")
	if err != nil {
		t.Fatal(err)
	}
	want = LeaveResult{ClanID: clan, ClanName: "Wolves", Player: ClanPlayer{ID: owner, DisplayName: "owner_Wolves"},
		NewOwner: &ClanPlayer{ID: a, DisplayName: "u_a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owner leaves:\n got %+v (new owner %+v)\nwant %+v (new owner %+v)", got, got.NewOwner, want, want.NewOwner)
	}

	// the last member leaves: the clan is disbanded, nobody takes over
	got, err = r.LeaveClan(ctx, game, clan, "u_a")
	if err != nil {
		t.Fatal(err)
	}
	want = LeaveResult{ClanID: clan, ClanName: "Wolves", Player: ClanPlayer{ID: a, DisplayName: "u_a"}, Disbanded: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("last member leaves:\n got %+v\nwant %+v", got, want)
	}
	if clanExists(t, r, clan) {
		t.Fatal("the clan should be deleted")
	}
}

func TestLeaveClanOwnerLeavesAndTheOldestMemberTakesOver(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	a := joinAs(t, r, game, clan, "u_a")
	b := joinAs(t, r, game, clan, "u_b")
	c := joinAs(t, r, game, clan, "u_c")
	// b joined first of the three even though a was added to the database first
	setJoinedAt(t, r, clan, b, 30)
	setJoinedAt(t, r, clan, a, 20)
	setJoinedAt(t, r, clan, c, 10)

	if _, err := r.LeaveClan(ctx, game, clan, "owner_Wolves"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, r, clan); got != b {
		t.Fatalf("new owner = %s, want the oldest member %s", got, b)
	}
	var role string
	for _, m := range []string{a, c} {
		_ = r.pool.QueryRow(ctx, "select role from clan_members where clan_id = $1 and player_id = $2", clan, m).Scan(&role)
		if role != "member" {
			t.Fatalf("player %s has role %q, want member", m, role)
		}
	}
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
	if count != 3 {
		t.Fatalf("member_count = %d, want 3", count)
	}
	checkClanIsConsistent(t, r, clan)
	// the old owner is a normal player again: free to start a new clan
	if _, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "owner_Wolves"), NewClan{Name: "Wolves Reborn"}); err != nil {
		t.Fatalf("old owner creating a clan: %v", err)
	}
	// the new owner can leave too: the next oldest takes over
	if _, err := r.LeaveClan(ctx, game, clan, "u_b"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, r, clan); got != a {
		t.Fatalf("second new owner = %s, want %s", got, a)
	}
	checkClanIsConsistent(t, r, clan)
}

func TestLeaveClanOwnerLeavesTiesGoToTheLowerPlayerID(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	clan := ownClan(t, r, game, "Wolves")
	a := joinAs(t, r, game, clan, "u_a")
	b := joinAs(t, r, game, clan, "u_b")
	if _, err := r.pool.Exec(context.Background(),
		`update clan_members set joined_at = '2026-01-01 00:00:00+00' where clan_id = $1 and role = 'member'`, clan); err != nil {
		t.Fatal(err)
	}
	want := a
	if b < a {
		want = b
	}

	if _, err := r.LeaveClan(context.Background(), game, clan, "owner_Wolves"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, r, clan); got != want {
		t.Fatalf("new owner = %s, want the lower ID %s", got, want)
	}
	checkClanIsConsistent(t, r, clan)
}

func TestLeaveClanOwnerAloneDeletesTheClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	other := ownClan(t, r, game, "Bears")

	if _, err := r.LeaveClan(ctx, game, clan, "owner_Wolves"); err != nil {
		t.Fatal(err)
	}
	if clanExists(t, r, clan) {
		t.Fatal("the clan should be deleted")
	}
	var n int
	_ = r.pool.QueryRow(ctx, "select count(*) from clan_members where clan_id = $1", clan).Scan(&n)
	if n != 0 {
		t.Fatalf("%d member rows left behind", n)
	}
	if list, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 100}); len(list) != 1 || list[0].ID != other {
		t.Fatalf("only the other clan should be listed, got %+v", list)
	}
	checkClanIsConsistent(t, r, other)
	// the name and the player are free again
	if _, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "owner_Wolves"), NewClan{Name: "Wolves"}); err != nil {
		t.Fatalf("creating the clan again: %v", err)
	}
	// and the deleted clan is gone for everyone
	if _, err := r.LeaveClan(ctx, game, clan, "owner_Wolves"); !errors.Is(err, ErrClanNotFound) {
		t.Fatalf("leaving the deleted clan: %v, want ErrClanNotFound", err)
	}
}

func TestLeaveClanNotAMember(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	wolves := ownClan(t, r, game, "Wolves")
	bears := ownClan(t, r, game, "Bears")
	joinAs(t, r, game, bears, "u_bear")
	newPlayer(t, r, game, "u_free") // a player without a clan

	for _, ext := range []string{
		"u_free",      // in no clan
		"u_bear",      // in another clan
		"owner_Bears", // the owner of another clan
		"u_never_seen",
	} {
		if _, err := r.LeaveClan(ctx, game, wolves, ext); !errors.Is(err, ErrNotInClan) {
			t.Fatalf("%s leaving Wolves: %v, want ErrNotInClan", ext, err)
		}
	}
	checkClanIsConsistent(t, r, wolves)
	checkClanIsConsistent(t, r, bears)
	var wolfCount, bearCount int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", wolves).Scan(&wolfCount)
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", bears).Scan(&bearCount)
	if wolfCount != 1 || bearCount != 2 {
		t.Fatalf("counts changed: Wolves=%d Bears=%d, want 1 and 2", wolfCount, bearCount)
	}
	// a player we have never seen must not be created by trying to leave
	var n int
	_ = r.pool.QueryRow(ctx, "select count(*) from players where game_id = $1 and external_id = 'u_never_seen'", game).Scan(&n)
	if n != 0 {
		t.Fatal("leaving created a player record")
	}
}

func TestLeaveClanNotFound(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	foreign := ownClan(t, r, otherGame, "Wolves")
	newPlayer(t, r, game, "owner_Wolves") // same external ID as the foreign clan's owner

	for name, id := range map[string]string{
		"unknown":        "00000000-0000-4000-8000-00000000dead",
		"another game's": foreign,
		"not a UUID":     "nope",
		"empty":          "",
		"not hex":        "ZZZZZZZZ-0000-4000-8000-00000000dead",
	} {
		if _, err := r.LeaveClan(ctx, game, id, "owner_Wolves"); !errors.Is(err, ErrClanNotFound) {
			t.Errorf("%s: %v, want ErrClanNotFound", name, err)
		}
	}
	// the foreign clan is untouched
	checkClanIsConsistent(t, r, foreign)
	if n := countRows(t, r, "clan_members", otherGame); n != 1 {
		t.Fatalf("the other game's members changed: %d rows", n)
	}
}

// Two members press "Leave" at the same moment. Both must succeed, and the counts must
// end up right. Repeated, because a race only shows up on some runs.
func TestLeaveClanTwoMembersAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Duo%d", round))
		exts := []string{fmt.Sprintf("a_%d", round), fmt.Sprintf("b_%d", round)}
		for _, e := range exts {
			joinAs(t, r, game, clan, e)
		}

		ok, _ := race(t, 2, func(i int) error { _, err := r.LeaveClan(ctx, game, clan, exts[i]); return err }, ErrNotInClan)
		if ok != 2 {
			t.Fatalf("round %d: %d of 2 left", round, ok)
		}
		var count int
		_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
		if count != 1 {
			t.Fatalf("round %d: member_count = %d, want 1 (the owner)", round, count)
		}
		checkClanIsConsistent(t, r, clan)
	}
}

// The same player leaves twice at the same moment (a double click): one succeeds, the
// other is told they are not a member, and the count goes down only once.
func TestLeaveClanSamePlayerTwiceAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := ownClan(t, r, game, "Wolves")
	joinAs(t, r, game, clan, "u_1")
	joinAs(t, r, game, clan, "u_2")

	ok, notIn := race(t, 6, func(int) error { _, err := r.LeaveClan(ctx, game, clan, "u_1"); return err }, ErrNotInClan)
	if ok != 1 || notIn != 5 {
		t.Fatalf("left=%d notInClan=%d, want 1 and 5", ok, notIn)
	}
	var count int
	_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
	if count != 2 {
		t.Fatalf("member_count = %d, want 2", count)
	}
	checkClanIsConsistent(t, r, clan)
}

// Everybody, the owner included, leaves at once. Whatever the order, nobody is left
// behind and the clan is deleted exactly when its last member goes.
func TestLeaveClanEveryoneAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 10; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Crowd%d", round))
		exts := []string{fmt.Sprintf("owner_Crowd%d", round)}
		for i := 0; i < 5; i++ {
			e := fmt.Sprintf("m_%d_%d", round, i)
			joinAs(t, r, game, clan, e)
			exts = append(exts, e)
		}

		ok, _ := race(t, len(exts), func(i int) error { _, err := r.LeaveClan(ctx, game, clan, exts[i]); return err }, ErrNotInClan)
		if ok != len(exts) {
			t.Fatalf("round %d: %d of %d left", round, ok, len(exts))
		}
		if clanExists(t, r, clan) {
			t.Fatalf("round %d: the empty clan still exists", round)
		}
	}
	if n := countRows(t, r, "clan_members", game); n != 0 {
		t.Fatalf("%d member rows left behind", n)
	}
}

// The owner leaves while another player joins. Either order is fine, but the clan must
// end up consistent with exactly one owner, and the oldest member must be that owner.
func TestLeaveClanOwnerLeavesWhileSomeoneJoins(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 20; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Race%d", round))
		oldest := joinAs(t, r, game, clan, fmt.Sprintf("old_%d", round))
		setJoinedAt(t, r, clan, oldest, 5)
		joiner := newPlayer(t, r, game, fmt.Sprintf("new_%d", round))

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = r.LeaveClan(ctx, game, clan, fmt.Sprintf("owner_Race%d", round)) }()
		go func() { defer wg.Done(); _, errs[1] = r.JoinClan(ctx, game, clan, joiner, testMaxMembers, "member") }()
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: leave: %v, join: %v", round, errs[0], errs[1])
		}
		checkClanIsConsistent(t, r, clan)
		if got := ownerOf(t, r, clan); got != oldest {
			t.Fatalf("round %d: owner = %s, want the oldest member %s", round, got, oldest)
		}
		var count int
		_ = r.pool.QueryRow(ctx, "select member_count from clans where id = $1", clan).Scan(&count)
		if count != 2 {
			t.Fatalf("round %d: member_count = %d, want 2 (oldest + joiner)", round, count)
		}
	}
}

// The owner is alone and leaves while someone joins. If the leave wins, the clan is gone
// and the join says "not found". If the join wins, the newcomer becomes the owner.
func TestLeaveClanAloneOwnerLeavesWhileSomeoneJoins(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	var deleted, kept int

	for round := 0; round < 30; round++ {
		clan := ownClan(t, r, game, fmt.Sprintf("Solo%d", round))
		joiner := newPlayer(t, r, game, fmt.Sprintf("j_%d", round))

		var wg sync.WaitGroup
		var leaveErr, joinErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, leaveErr = r.LeaveClan(ctx, game, clan, fmt.Sprintf("owner_Solo%d", round))
		}()
		go func() { defer wg.Done(); _, joinErr = r.JoinClan(ctx, game, clan, joiner, testMaxMembers, "member") }()
		wg.Wait()
		if leaveErr != nil {
			t.Fatalf("round %d: leave: %v", round, leaveErr)
		}

		if clanExists(t, r, clan) {
			kept++
			if joinErr != nil {
				t.Fatalf("round %d: the clan exists but the join failed: %v", round, joinErr)
			}
			checkClanIsConsistent(t, r, clan)
			if got := ownerOf(t, r, clan); got != joiner {
				t.Fatalf("round %d: owner = %s, want the newcomer %s", round, got, joiner)
			}
		} else {
			deleted++
			if !errors.Is(joinErr, ErrClanNotFound) {
				t.Fatalf("round %d: the clan is gone, so the join should say not found, got %v", round, joinErr)
			}
			if memberOf(t, r, clan, joiner) {
				t.Fatalf("round %d: the newcomer is a member of a deleted clan", round)
			}
		}
	}
	t.Logf("leave first: %d times, join first: %d times", deleted, kept)
}

// clanWith creates a clan with `members` members in all (the owner included).
func clanWith(t *testing.T, r *Repository, game, name string, members int) string {
	t.Helper()
	ctx := context.Background()
	clan := ownClan(t, r, game, name)
	for i := 1; i < members; i++ {
		if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, fmt.Sprintf("%s_%d", name, i)), testMaxMembers, "member"); err != nil {
			t.Fatal(err)
		}
	}
	return clan
}

// "Featured" clans are the biggest ones that still have room; a search looks at all clans.
func TestListClansFeaturedAndSearch(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()

	// Echo is made before Bravo on purpose: they are the same size, and the name, not the
	// order they were made in, must put Bravo first.
	// the game allows 6 members per clan
	clanWith(t, r, game, "Echo", 5)
	clanWith(t, r, game, "Alpha", 3)
	clanWith(t, r, game, "Bravo", 5)
	clanWith(t, r, game, "Charlie", 6) // the biggest, and full
	clanWith(t, r, game, "Delta", 1)
	clanWith(t, r, other, "Zulu", 8) // another game: never shows up

	featured := ListClansOptions{Limit: 3, ByMembers: true, OnlyWithSpace: true, MaxMembers: 6}
	got, err := r.ListClans(ctx, game, "", featured)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Bravo", "Echo", "Alpha"}; !equalStrings(names(got), want) {
		t.Fatalf("featured = %v, want %v (full Charlie left out, ties by name)", names(got), want)
	}

	// sorting by members alone does not hide the full clan
	got, _ = r.ListClans(ctx, game, "", ListClansOptions{Limit: 100, ByMembers: true})
	if want := []string{"Charlie", "Bravo", "Echo", "Alpha", "Delta"}; !equalStrings(names(got), want) {
		t.Fatalf("by members = %v, want %v", names(got), want)
	}

	// hasSpace alone keeps the default order, by name
	got, _ = r.ListClans(ctx, game, "", ListClansOptions{Limit: 100, OnlyWithSpace: true, MaxMembers: 6})
	if want := []string{"Alpha", "Bravo", "Delta", "Echo"}; !equalStrings(names(got), want) {
		t.Fatalf("with space = %v, want %v", names(got), want)
	}

	// a search with no options finds every clan of the game, the full one too
	got, _ = r.ListClans(ctx, game, "char", ListClansOptions{Limit: 100})
	if want := []string{"Charlie"}; !equalStrings(names(got), want) {
		t.Fatalf("search = %v, want the full clan to be found", names(got))
	}

	// the limit applies after sorting
	got, _ = r.ListClans(ctx, game, "", ListClansOptions{Limit: 1, ByMembers: true})
	if want := []string{"Charlie"}; !equalStrings(names(got), want) {
		t.Fatalf("limit 1 by members = %v, want %v", names(got), want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The limit is the game's rule and can change between two joins. Raising it opens full clans;
// lowering it removes nobody, the clan just takes no one new.
func TestJoinClanUsesTheLimitItIsGiven(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	clan := clanWith(t, r, game, "Wolves", 3)

	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_a"), 3, "member"); !errors.Is(err, ErrClanFull) {
		t.Fatalf("3 members, limit 3: err = %v, want ErrClanFull", err)
	}
	if c, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_b"), 30, "member"); err != nil || c.MemberCount != 4 {
		t.Fatalf("limit raised to 30: %+v, %v; want 4 members", c, err)
	}
	// lowered to 2: all 4 stay, no one new gets in, and the clan does not count as having space
	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_c"), 2, "member"); !errors.Is(err, ErrClanFull) {
		t.Fatalf("over a lowered limit: err = %v, want ErrClanFull", err)
	}
	if n := countRows(t, r, "clan_members", game); n != 4 {
		t.Fatalf("members = %d, want the 4 to stay", n)
	}
	list, _ := r.ListClans(ctx, game, "", ListClansOptions{Limit: 10, OnlyWithSpace: true, MaxMembers: 2})
	if len(list) != 0 {
		t.Fatalf("with space under a lowered limit: %v, want none", names(list))
	}
	list, _ = r.ListClans(ctx, game, "", ListClansOptions{Limit: 10, OnlyWithSpace: true, MaxMembers: 5})
	if len(list) != 1 {
		t.Fatalf("with space under limit 5: %v, want Wolves", names(list))
	}
	checkClanIsConsistent(t, r, clan)
}

func TestCreateClanSavesTheDescriptionAndBadge(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	made, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "u_1"),
		NewClan{Name: "Wolves", Description: "Casual farmers", Badge: "wolf", Tag: "WOLF"})
	if err != nil {
		t.Fatal(err)
	}
	if made.Description != "Casual farmers" || made.Badge != "wolf" || made.Tag != "WOLF" {
		t.Fatalf("create answered %+v", made)
	}

	// Both ways a player reads a clan must carry them.
	list, err := r.ListClans(ctx, game, "", ListClansOptions{Limit: 100, MaxMembers: testMaxMembers})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Description != "Casual farmers" || list[0].Badge != "wolf" || list[0].Tag != "WOLF" {
		t.Fatalf("list gave %+v", list)
	}
	mine, err := r.MyClan(ctx, game, "u_1")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Clan.Description != "Casual farmers" || mine.Clan.Badge != "wolf" || mine.Clan.Tag != "WOLF" {
		t.Fatalf("my clan gave %+v", mine.Clan)
	}
}

// A clan made without them - like every clan made before the columns existed, which
// addClan writes the same way - reads back as empty, not as an error.
func TestClanWithoutTheOptionalFieldsReadsAsEmpty(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)

	id := addClan(t, r, game, "Bears")
	list, err := r.ListClans(context.Background(), game, "", ListClansOptions{Limit: 100, MaxMembers: testMaxMembers})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].Description != "" || list[0].Badge != "" || list[0].Tag != "" {
		t.Fatalf("list gave %+v", list)
	}
}

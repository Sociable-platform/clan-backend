package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

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

	got, err := r.ListClans(ctx, gameA, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Wolves" {
		t.Fatalf("game A sees %v, want only its own [Wolves]", names(got))
	}
	if got, _ := r.ListClans(ctx, gameA, "bear", 100); len(got) != 0 {
		t.Fatalf("searching game B's clan name from game A found %v", names(got))
	}
	if got[0].ID == "" || got[0].MemberCount != 1 || got[0].MaxMembers != 10 {
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
		got, err := r.ListClans(ctx, game, tc.q, 100)
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprint(names(got)); s != tc.want {
			t.Errorf("q=%q: got %s, want %s", tc.q, s, tc.want)
		}
	}

	if got, _ := r.ListClans(ctx, game, "", 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d clans", len(got))
	}
	if got, _ := r.ListClans(ctx, newGameID(t), "", 100); got == nil {
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

	c, err := r.CreateClan(ctx, game, owner, "Wolves")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Name != "Wolves" || c.MemberCount != 1 || c.MaxMembers != 10 {
		t.Fatalf("clan: %+v", c)
	}
	var role, gotGame string
	err = r.pool.QueryRow(ctx,
		"select role, game_id::text from clan_members where clan_id = $1 and player_id = $2",
		c.ID, owner).Scan(&role, &gotGame)
	if err != nil || role != "owner" || gotGame != game {
		t.Fatalf("owner membership: role=%q game=%q err=%v", role, gotGame, err)
	}
	if list, _ := r.ListClans(ctx, game, "", 100); len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("the new clan should be listed: %+v", list)
	}
}

func TestCreateClanNameTaken(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()

	if _, err := r.CreateClan(ctx, game, newPlayer(t, r, game, "u_1"), "Wolves"); err != nil {
		t.Fatal(err)
	}
	second := newPlayer(t, r, game, "u_2")
	if _, err := r.CreateClan(ctx, game, second, "WOLVES"); !errors.Is(err, ErrClanNameTaken) {
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
	if _, err := r.CreateClan(ctx, other, newPlayer(t, r, other, "u_1"), "Wolves"); err != nil {
		t.Fatalf("same name in another game: %v", err)
	}
}

func TestCreateClanOwnerAlreadyInClan(t *testing.T) {
	r, cleanup := testRepo(t)
	game, other := newGameID(t), newGameID(t)
	defer cleanup(game, other)
	ctx := context.Background()
	owner := newPlayer(t, r, game, "u_1")

	if _, err := r.CreateClan(ctx, game, owner, "Wolves"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateClan(ctx, game, owner, "Bears"); !errors.Is(err, ErrAlreadyInClan) {
		t.Fatalf("err = %v, want ErrAlreadyInClan", err)
	}
	if n := countRows(t, r, "clans", game); n != 1 {
		t.Fatalf("clans = %d, want 1 (Bears must not be saved)", n)
	}
	// being in a clan in one game does not matter in another game
	if _, err := r.CreateClan(ctx, other, newPlayer(t, r, other, "u_1"), "Bears"); err != nil {
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
		_, err := r.CreateClan(context.Background(), game, owner, fmt.Sprintf("Clan %d", i))
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
		_, err := r.CreateClan(context.Background(), game, owners[i], "Wolves")
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
	c, err := r.CreateClan(context.Background(), gameID, newPlayer(t, r, gameID, "owner_"+name), name)
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

	c, err := r.JoinClan(ctx, game, clan, player)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != clan || c.Name != "Wolves" || c.MemberCount != 2 || c.MaxMembers != 10 {
		t.Fatalf("clan after join: %+v", c)
	}
	var role string
	if err := r.pool.QueryRow(ctx, "select role from clan_members where clan_id = $1 and player_id = $2",
		clan, player).Scan(&role); err != nil || role != "member" {
		t.Fatalf("membership: role=%q err=%v", role, err)
	}
	if list, _ := r.ListClans(ctx, game, "", 100); len(list) != 1 || list[0].MemberCount != 2 {
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
		if _, err := r.JoinClan(ctx, gameB, clanID, playerOfB); !errors.Is(err, ErrClanNotFound) {
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
	if _, err := r.JoinClan(ctx, game, wolves, player); err != nil {
		t.Fatal(err)
	}

	if _, err := r.JoinClan(ctx, game, wolves, player); !errors.Is(err, ErrAlreadyInClan) {
		t.Errorf("same clan again: err = %v, want ErrAlreadyInClan", err)
	}
	if _, err := r.JoinClan(ctx, game, bears, player); !errors.Is(err, ErrAlreadyInClan) {
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
	if _, err := r.pool.Exec(ctx, "update clans set max_members = 2 where id = $1", clan); err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_2")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClan(ctx, game, clan, newPlayer(t, r, game, "u_3")); !errors.Is(err, ErrClanFull) {
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
		_, err := r.JoinClan(context.Background(), game, clan, players[i])
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
		_, err := r.JoinClan(context.Background(), game, clans[i], player)
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

	created, err := r.CreateClan(ctx, game, ali, "Wolves")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.MyClan(ctx, game, "u_ali")
	if err != nil || got.Role != "owner" || got.Clan != created {
		t.Fatalf("owner: got %+v, %v; want %+v as owner", got, err, created)
	}

	if _, err := r.JoinClan(ctx, game, created.ID, newPlayer(t, r, game, "u_leyla")); err != nil {
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
	c, err := r.CreateClan(ctx, game, owner, "Wolves")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"u_b", "u_a", "u_c"} { // joined in this order
		if _, err := r.JoinClan(ctx, game, c.ID, newPlayer(t, r, game, p)); err != nil {
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

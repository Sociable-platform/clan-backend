package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// These tests need the real database like widget_test.go (PLATFORM_TEST_DATABASE_URL), with migration
// 000006 (games.allowed_origins) applied.

func TestNewGameHasAnEmptyNonNilAllowedOriginsList(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	g, err := r.GameOfMember(ctx, game, dev)
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

	owned, err := r.GameOfMember(ctx, game, dev)
	if err != nil || !reflect.DeepEqual(owned.AllowedOrigins, want) {
		t.Errorf("GameOfMember = %+v, %v", owned, err)
	}
	byID, err := r.GameByID(ctx, game)
	if err != nil || !reflect.DeepEqual(byID.AllowedOrigins, want) {
		t.Errorf("GameByID = %+v, %v", byID, err)
	}
	list, err := r.GamesOfMember(ctx, dev)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0].AllowedOrigins, want) {
		t.Errorf("GamesOfMember = %+v, %v", list, err)
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

// ---- game members (TEAM-305): who may use a game is decided by game_members, with migration 000015 applied.

// newDeveloper creates a developer and removes them (and any membership they were given) when the test ends.
func newDeveloper(t *testing.T, r *Repository, label string) string {
	t.Helper()
	dev, err := r.CreateDeveloper(context.Background(), fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano()), "hash", label)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = r.pool.Exec(c, `delete from game_members where developer_id = $1`, dev.ID)
		_, _ = r.pool.Exec(c, `delete from developers where id = $1`, dev.ID)
	})
	return dev.ID
}

func rolesOf(t *testing.T, r *Repository, gameID string) map[string]string {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), `select developer_id::text, role from game_members where game_id = $1`, gameID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var dev, role string
		if err := rows.Scan(&dev, &role); err != nil {
			t.Fatal(err)
		}
		out[dev] = role
	}
	return out
}

func TestCreateGameMakesTheDeveloperItsOwner(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	if got := rolesOf(t, r, game); !reflect.DeepEqual(got, map[string]string{dev: RoleOwner}) {
		t.Fatalf("members = %v, want only the creator, as owner", got)
	}
	g, err := r.GameOfMember(ctx, game, dev)
	if err != nil || g.Role != RoleOwner || g.ID != game || g.DeveloperID != dev {
		t.Fatalf("GameOfMember = %+v, %v; want the game with the owner role", g, err)
	}
	list, err := r.GamesOfMember(ctx, dev)
	if err != nil || len(list) != 1 || list[0].ID != game || list[0].Role != RoleOwner {
		t.Fatalf("GamesOfMember = %+v, %v; want the one game with its role", list, err)
	}
	created, err := r.CreateGame(ctx, dev, "Second game", "")
	if err != nil || created.Role != RoleOwner {
		t.Fatalf("CreateGame = %+v, %v; want the owner role in the answer too", created, err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), `delete from games where id = $1`, created.ID) })
}

// A game that cannot be made leaves nothing behind: the game and its owner row are one statement.
func TestCreateGameForAMissingDeveloperLeavesNothing(t *testing.T) {
	r := testRepo(t)
	name := fmt.Sprintf("Orphan %d", time.Now().UnixNano())
	if _, err := r.CreateGame(context.Background(), unknownID, name, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var games, members int
	if err := r.pool.QueryRow(context.Background(),
		`select (select count(*) from games where name = $1),
		        (select count(*) from game_members where developer_id = $2)`, name, unknownID).Scan(&games, &members); err != nil {
		t.Fatal(err)
	}
	if games != 0 || members != 0 {
		t.Errorf("%d games and %d memberships left behind, want none", games, members)
	}
}

// Access follows the membership, not games.developer_id: a member gets in at once, and loses it at once when
// the row goes. A developer who is not a member never sees the game, whatever they ask.
func TestMembershipDecidesWhoMayUseAGame(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	stranger := newDeveloper(t, r, "stranger")
	key, err := r.CreateAPIKey(ctx, game, "clk_test", fmt.Sprintf("hash-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}

	// Not a member: the game is not there, for every lookup and every change.
	if _, err := r.GameOfMember(ctx, game, stranger); !errors.Is(err, ErrNotFound) {
		t.Errorf("GameOfMember by a stranger: %v, want ErrNotFound", err)
	}
	if none, err := r.GamesOfMember(ctx, stranger); err != nil || none == nil || len(none) != 0 {
		t.Errorf("GamesOfMember by a stranger = %#v, %v; want an empty, non-nil list", none, err)
	}
	if _, err := r.SetAllowedOrigins(ctx, game, stranger, []string{"https://evil.example.com"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAllowedOrigins by a stranger: %v, want ErrNotFound", err)
	}
	if err := r.RevokeAPIKey(ctx, key.ID, stranger); !errors.Is(err, ErrNotFound) {
		t.Errorf("RevokeAPIKey by a stranger: %v, want ErrNotFound", err)
	}
	if _, err := r.GameOfMember(ctx, "not-a-uuid", owner); !errors.Is(err, ErrNotFound) {
		t.Errorf("GameOfMember with an ID that is not a UUID: %v, want ErrNotFound", err)
	}

	// A member row is all it takes, and the role comes back.
	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'member')`, game, stranger); err != nil {
		t.Fatal(err)
	}
	g, err := r.GameOfMember(ctx, game, stranger)
	if err != nil || g.Role != RoleMember || g.ID != game {
		t.Fatalf("GameOfMember by a member = %+v, %v; want the game with the member role", g, err)
	}
	if list, err := r.GamesOfMember(ctx, stranger); err != nil || len(list) != 1 || list[0].ID != game || list[0].Role != RoleMember {
		t.Errorf("GamesOfMember by a member = %+v, %v", list, err)
	}
	if set, err := r.SetAllowedOrigins(ctx, game, stranger, []string{"https://ok.example.com"}); err != nil || set.Role != RoleMember {
		t.Errorf("SetAllowedOrigins by a member = %+v, %v; want it to work (TEAM-306 makes it owner-only) and report the role", set, err)
	}
	if err := r.RevokeAPIKey(ctx, key.ID, stranger); err != nil {
		t.Errorf("RevokeAPIKey by a member: %v", err)
	}

	// The owner still has the game, and the member row going takes the access with it.
	if g, err := r.GameOfMember(ctx, game, owner); err != nil || g.Role != RoleOwner {
		t.Errorf("GameOfMember by the owner = %+v, %v", g, err)
	}
	if _, err := r.pool.Exec(ctx, `delete from game_members where game_id = $1 and developer_id = $2`, game, stranger); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GameOfMember(ctx, game, stranger); !errors.Is(err, ErrNotFound) {
		t.Errorf("GameOfMember after the membership went: %v, want ErrNotFound", err)
	}
}

// The table keeps itself honest: one row per developer and game, and only the two roles.
func TestGameMembersRules(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	dev, game := newGame(t, r)

	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'member')`, game, dev); err == nil {
		t.Error("a second row for the same developer and game was stored")
	}
	other := newDeveloper(t, r, "other")
	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'admin')`, game, other); err == nil {
		t.Error("a role that is neither owner nor member was stored")
	}
	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'member')`, unknownID, other); err == nil {
		t.Error("a member of a game that does not exist was stored")
	}
	// A game that goes takes its members with it.
	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'member')`, game, other); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `delete from widget_versions where game_id = $1`, game); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `delete from games where id = $1`, game); err != nil {
		t.Fatal(err)
	}
	if got := rolesOf(t, r, game); len(got) != 0 {
		t.Errorf("members of a deleted game = %v, want none", got)
	}
}

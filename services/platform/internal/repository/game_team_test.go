package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// addMember puts a developer on a game with a role, straight into the table.
func addMember(t *testing.T, r *Repository, game, developer, role string) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		`insert into game_members (game_id, developer_id, role) values ($1, $2, $3)`, game, developer, role); err != nil {
		t.Fatal(err)
	}
}

func suspendDeveloper(t *testing.T, r *Repository, developer string) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(), `update developers set status = 'suspended' where id = $1`, developer); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveGameMember(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	coOwner := newDeveloper(t, r, "co-owner")
	addMember(t, r, game, member, RoleMember)
	addMember(t, r, game, coOwner, RoleOwner)

	if err := r.RemoveGameMember(ctx, game, unknownID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a developer who is not on the game: %v, want ErrNotFound", err)
	}
	if err := r.RemoveGameMember(ctx, unknownID, member); !errors.Is(err, ErrNotFound) {
		t.Errorf("a game that does not exist: %v, want ErrNotFound", err)
	}
	if err := r.RemoveGameMember(ctx, "not-a-uuid", member); !errors.Is(err, ErrNotFound) {
		t.Errorf("a game ID that is not a UUID: %v, want ErrNotFound", err)
	}

	if err := r.RemoveGameMember(ctx, game, member); err != nil {
		t.Fatal(err)
	}
	// Access stops at once: the join that every request does finds nothing.
	if _, err := r.GameOfMember(ctx, game, member); !errors.Is(err, ErrNotFound) {
		t.Errorf("the removed member still reaches the game: %v", err)
	}
	if err := r.RemoveGameMember(ctx, game, member); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v, want ErrNotFound", err)
	}

	// An owner can be removed while another is left, and the last one never.
	if err := r.RemoveGameMember(ctx, game, coOwner); err != nil {
		t.Fatalf("removing one of two owners: %v", err)
	}
	if err := r.RemoveGameMember(ctx, game, owner); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last owner: %v, want ErrLastOwner", err)
	}
	if roles := rolesOf(t, r, game); len(roles) != 1 || roles[owner] != RoleOwner {
		t.Errorf("roles = %v, want the owner alone", roles)
	}
}

func TestSetGameMemberRole(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	addMember(t, r, game, member, RoleMember)

	got, err := r.SetGameMemberRole(ctx, game, member, RoleOwner)
	if err != nil || got.DeveloperID != member || got.Role != RoleOwner || got.Name != "member" || got.Email == "" || got.Status != StatusActive || got.JoinedAt.IsZero() {
		t.Fatalf("promote = %+v, %v", got, err)
	}
	if again, err := r.SetGameMemberRole(ctx, game, member, RoleOwner); err != nil || again.Role != RoleOwner {
		t.Errorf("the same role again: %+v, %v", again, err)
	}
	if _, err := r.SetGameMemberRole(ctx, game, owner, RoleMember); err != nil {
		t.Fatalf("demoting one of two owners: %v", err)
	}
	if _, err := r.SetGameMemberRole(ctx, game, member, RoleMember); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting the last owner: %v, want ErrLastOwner", err)
	}
	if rolesOf(t, r, game)[member] != RoleOwner {
		t.Error("a refused demotion changed the role")
	}
	if _, err := r.SetGameMemberRole(ctx, game, unknownID, RoleOwner); !errors.Is(err, ErrNotFound) {
		t.Errorf("a developer who is not on the game: %v, want ErrNotFound", err)
	}
	// The table refuses a role that is not one of the two, so a bug in the service cannot store one.
	if _, err := r.SetGameMemberRole(ctx, game, owner, "admin"); err == nil || errors.Is(err, ErrLastOwner) || errors.Is(err, ErrNotFound) {
		t.Errorf("a role that does not exist: %v, want an error from the table", err)
	}
}

func TestTransferGameOwnership(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	other := newDeveloper(t, r, "other-member")
	addMember(t, r, game, member, RoleMember)
	addMember(t, r, game, other, RoleMember)

	if err := r.TransferGameOwnership(ctx, game, member, other); !errors.Is(err, ErrOwnerRequired) {
		t.Errorf("a member transferring: %v, want ErrOwnerRequired", err)
	}
	if err := r.TransferGameOwnership(ctx, game, owner, unknownID); !errors.Is(err, ErrNotFound) {
		t.Errorf("to somebody who is not on the game: %v, want ErrNotFound", err)
	}
	if err := r.TransferGameOwnership(ctx, game, owner, owner); !errors.Is(err, ErrAlreadyOwner) {
		t.Errorf("to herself: %v, want ErrAlreadyOwner", err)
	}
	if roles := rolesOf(t, r, game); roles[owner] != RoleOwner || roles[member] != RoleMember || roles[other] != RoleMember {
		t.Fatalf("a refused transfer changed the roles: %v", roles)
	}

	if err := r.TransferGameOwnership(ctx, game, owner, member); err != nil {
		t.Fatal(err)
	}
	if roles := rolesOf(t, r, game); roles[owner] != RoleMember || roles[member] != RoleOwner || roles[other] != RoleMember {
		t.Errorf("roles = %v, want the old owner a member, the target the owner, the other untouched", roles)
	}
	if err := r.TransferGameOwnership(ctx, game, owner, other); !errors.Is(err, ErrOwnerRequired) {
		t.Errorf("the old owner transferring again: %v, want ErrOwnerRequired", err)
	}
	if err := r.TransferGameOwnership(ctx, game, member, other); err != nil {
		t.Errorf("the new owner passing it on: %v", err)
	}
	if n := countOwners(t, r, game); n != 1 {
		t.Errorf("%d owners, want exactly one throughout", n)
	}
}

func countOwners(t *testing.T, r *Repository, game string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `select count(*) from game_members where game_id = $1 and role = 'owner'`, game).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// --- Races: a game keeps one owner whatever happens at the same moment ------------------------------------------

// race runs the functions at the same moment and returns what each returned.
func race(fns ...func() error) []error {
	start := make(chan struct{})
	out := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = fn()
		}()
	}
	close(start)
	wg.Wait()
	return out
}

// Two owners both try to take themselves (or each other) off the game at once. Without the lock both would count two
// owners and both would succeed, leaving none. Exactly one may win, and the other gets ErrLastOwner.
func TestTheLastOwnerSurvivesConcurrentChanges(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	steps := map[string]func(a, b, game string) []func() error{
		"both leave": func(a, b, game string) []func() error {
			return []func() error{
				func() error { return r.RemoveGameMember(ctx, game, a) },
				func() error { return r.RemoveGameMember(ctx, game, b) },
			}
		},
		"both demote themselves": func(a, b, game string) []func() error {
			return []func() error{
				func() error { _, err := r.SetGameMemberRole(ctx, game, a, RoleMember); return err },
				func() error { _, err := r.SetGameMemberRole(ctx, game, b, RoleMember); return err },
			}
		},
		"each removes the other": func(a, b, game string) []func() error {
			return []func() error{
				func() error { return r.RemoveGameMember(ctx, game, b) },
				func() error { return r.RemoveGameMember(ctx, game, a) },
			}
		},
		"one leaves while the other demotes": func(a, b, game string) []func() error {
			return []func() error{
				func() error { return r.RemoveGameMember(ctx, game, a) },
				func() error { _, err := r.SetGameMemberRole(ctx, game, b, RoleMember); return err },
			}
		},
	}
	for name, build := range steps {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 25; i++ {
				a, game := newGame(t, r)
				b := newDeveloper(t, r, fmt.Sprintf("second-%d", i))
				addMember(t, r, game, b, RoleOwner)

				results := race(build(a, b, game)...)
				var won, refused int
				for _, err := range results {
					switch {
					case err == nil:
						won++
					case errors.Is(err, ErrLastOwner):
						refused++
					default:
						t.Fatalf("round %d: unexpected error %v", i, err)
					}
				}
				if won != 1 || refused != 1 {
					t.Fatalf("round %d: %d succeeded and %d were refused, want one of each (%v)", i, won, refused, results)
				}
				if n := countOwners(t, r, game); n != 1 {
					t.Fatalf("round %d: %d owners left, want exactly one", i, n)
				}
			}
		})
	}
}

// A transfer and a removal of the receiving owner at the same moment: whatever the order, the game has an owner.
func TestATransferAndARemovalRaceKeepAnOwner(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		a, game := newGame(t, r)
		b := newDeveloper(t, r, fmt.Sprintf("receiver-%d", i))
		addMember(t, r, game, b, RoleMember)

		race(
			func() error { return r.TransferGameOwnership(ctx, game, a, b) },
			func() error { return r.RemoveGameMember(ctx, game, b) },
		)
		if n := countOwners(t, r, game); n != 1 {
			t.Fatalf("round %d: %d owners, want exactly one", i, n)
		}
	}
}

// Many members leaving at once, and the owner among them: only the members go.
func TestManyLeaveAtOnce(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	ids := []string{owner}
	for i := 0; i < 6; i++ {
		id := newDeveloper(t, r, fmt.Sprintf("leaver-%d", i))
		addMember(t, r, game, id, RoleMember)
		ids = append(ids, id)
	}
	fns := make([]func() error, len(ids))
	for i, id := range ids {
		fns[i] = func() error { return r.RemoveGameMember(ctx, game, id) }
	}
	results := race(fns...)
	if !errors.Is(results[0], ErrLastOwner) {
		t.Errorf("the owner: %v, want ErrLastOwner", results[0])
	}
	for i, err := range results[1:] {
		if err != nil {
			t.Errorf("member %d: %v", i, err)
		}
	}
	if roles := rolesOf(t, r, game); len(roles) != 1 || roles[owner] != RoleOwner {
		t.Errorf("roles = %v, want the owner alone", roles)
	}
}

// --- Whether a game is served, with a team -----------------------------------------------------------------------

func TestGameAccessFollowsTheOwners(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	coOwner := newDeveloper(t, r, "co-owner")
	addMember(t, r, game, member, RoleMember)
	status := func() string {
		a, err := r.GameAccessOf(ctx, game)
		if err != nil {
			t.Fatal(err)
		}
		return a.DeveloperStatus
	}

	if got := status(); got != StatusActive {
		t.Fatalf("everybody active: %q", got)
	}
	suspendDeveloper(t, r, member)
	if got := status(); got != StatusActive {
		t.Errorf("a suspended member stopped the game: %q", got)
	}
	suspendDeveloper(t, r, owner)
	if got := status(); got != StatusSuspended {
		t.Errorf("the only owner is suspended: %q, want suspended", got)
	}
	addMember(t, r, game, coOwner, RoleOwner)
	if got := status(); got != StatusActive {
		t.Errorf("an active co-owner: %q, want active", got)
	}
	suspendDeveloper(t, r, coOwner)
	if got := status(); got != StatusSuspended {
		t.Errorf("every owner is suspended: %q, want suspended", got)
	}
	if _, err := r.GameAccessOf(ctx, unknownID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a game that does not exist: %v, want ErrNotFound", err)
	}
}

func TestAdminViewsShowTheTeam(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	coOwner := newDeveloper(t, r, "co-owner")
	addMember(t, r, game, member, RoleMember)
	addMember(t, r, game, coOwner, RoleOwner)

	// The games of a developer carry the developer's role, and whether another owner keeps the game going.
	for _, tc := range []struct {
		name, dev, role string
		otherActive     bool
	}{{"owner", owner, RoleOwner, true}, {"co-owner", coOwner, RoleOwner, true}, {"member", member, RoleMember, true}} {
		games, err := r.AdminGamesByDeveloper(ctx, tc.dev)
		if err != nil || len(games) != 1 || games[0].ID != game || games[0].Role != tc.role || games[0].OtherOwnerActive != tc.otherActive {
			t.Errorf("%s: %+v, %v; want the game with role %s and other owner active %v", tc.name, games, err, tc.role, tc.otherActive)
		}
	}
	suspendDeveloper(t, r, coOwner)
	games, _ := r.AdminGamesByDeveloper(ctx, owner)
	if len(games) != 1 || games[0].OtherOwnerActive {
		t.Errorf("with the other owner suspended: %+v, want OtherOwnerActive false", games)
	}

	// The count in the developer list is the games they are on, as member too.
	page, err := r.ListDevelopers(ctx, DeveloperFilter{Query: "member-", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var count = -1
	for _, d := range page {
		if d.ID == member {
			count = d.GameCount
		}
	}
	if count != 1 {
		t.Errorf("a member of one game counts %d games, want 1", count)
	}

	// The game detail lists everybody with their account status.
	g, err := r.AdminGameByID(ctx, game)
	if err != nil || g.ID != game || g.Status != StatusActive {
		t.Fatalf("game = %+v, %v", g, err)
	}
	members, _ := r.GameMembers(ctx, game)
	statuses := map[string]string{}
	for _, m := range members {
		statuses[m.DeveloperID] = m.Status
	}
	if statuses[coOwner] != StatusSuspended || statuses[owner] != StatusActive {
		t.Errorf("member statuses = %v", statuses)
	}
	for _, bad := range []string{unknownID, "not-a-uuid"} {
		if _, err := r.AdminGameByID(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("AdminGameByID(%q): %v, want ErrNotFound", bad, err)
		}
	}
}

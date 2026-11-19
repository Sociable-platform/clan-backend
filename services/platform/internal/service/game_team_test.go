package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// The team methods of fakeStore follow the SQL of repository/game_team.go.

func (f *fakeStore) RemoveGameMember(_ context.Context, gameID, developerID string) error {
	if f.err != nil {
		return f.err
	}
	role, ok := f.team(gameID)[developerID]
	if !ok {
		return repository.ErrNotFound
	}
	if role == repository.RoleOwner && len(f.ownersOf(gameID)) <= 1 {
		return repository.ErrLastOwner
	}
	f.removeMember(gameID, developerID)
	return nil
}

func (f *fakeStore) SetGameMemberRole(_ context.Context, gameID, developerID, role string) (repository.GameMember, bool, error) {
	if f.err != nil {
		return repository.GameMember{}, false, f.err
	}
	current, ok := f.team(gameID)[developerID]
	if !ok {
		return repository.GameMember{}, false, repository.ErrNotFound
	}
	if current == repository.RoleOwner && role != repository.RoleOwner && len(f.ownersOf(gameID)) <= 1 {
		return repository.GameMember{}, false, repository.ErrLastOwner
	}
	f.setRole(gameID, developerID, role)
	return f.memberRow(gameID, developerID, role), current != role, nil
}

func (f *fakeStore) TransferGameOwnership(_ context.Context, gameID, fromID, toID string) error {
	if f.err != nil {
		return f.err
	}
	team := f.team(gameID)
	if team[fromID] != repository.RoleOwner {
		return repository.ErrOwnerRequired
	}
	to, ok := team[toID]
	if !ok {
		return repository.ErrNotFound
	}
	if to == repository.RoleOwner {
		return repository.ErrAlreadyOwner
	}
	f.setRole(gameID, fromID, repository.RoleMember)
	f.setRole(gameID, toID, repository.RoleOwner)
	return nil
}

func (f *fakeStore) AdminGameByID(_ context.Context, gameID string) (repository.AdminGame, error) {
	if f.err != nil {
		return repository.AdminGame{}, f.err
	}
	for _, g := range f.games {
		if g.ID == gameID {
			status := "active"
			if f.suspendedGames[g.ID] {
				status = "suspended"
			}
			return repository.AdminGame{ID: g.ID, Name: g.Name, Description: g.Description, Status: status, CreatedAt: g.CreatedAt}, nil
		}
	}
	return repository.AdminGame{}, repository.ErrNotFound
}

// teamSetup is a game owned by alice with bob and carol as members, and a recorder for the events.
func teamSetup(t *testing.T) (svc *Service, alice, bob, carol Developer, game Game) {
	t.Helper()
	svc, alice, bob, game = aliceGame(t)
	var err error
	carol, err = svc.Register(context.Background(), RegisterInput{Email: "carol@example.com", Password: "correct horse", Name: "Carol"})
	if err != nil {
		t.Fatal(err)
	}
	st := svc.store.(*fakeStore)
	st.setRole(game.ID, bob.ID, repository.RoleMember)
	st.setRole(game.ID, carol.ID, repository.RoleMember)
	return svc, alice, bob, carol, game
}

func rolesIn(t *testing.T, svc *Service, game Game) map[string]string {
	t.Helper()
	return svc.store.(*fakeStore).team(game.ID)
}

func TestRemoveGameMember(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	ctx := context.Background()

	// Only an owner removes; a member and a stranger are refused like for the other owner actions.
	wantError(t, svc.RemoveGameMember(ctx, bob.ID, game.ID, carol.ID), http.StatusForbidden, "owner_required")
	wantError(t, svc.RemoveGameMember(ctx, aStranger, game.ID, carol.ID), http.StatusNotFound, "game_not_found")
	wantError(t, svc.RemoveGameMember(ctx, alice.ID, game.ID, aStranger), http.StatusNotFound, "member_not_found")
	wantError(t, svc.RemoveGameMember(ctx, alice.ID, game.ID, "not-a-uuid"), http.StatusNotFound, "member_not_found")
	if len(rolesIn(t, svc, game)) != 3 {
		t.Fatal("a refused removal changed the team")
	}

	if err := svc.RemoveGameMember(ctx, alice.ID, game.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	// Access stops at once: the next thing Bob does for the game is a 404, like for a game he never saw.
	_, err := svc.MemberGame(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	if games, _ := svc.ListGames(ctx, bob.ID); len(games) != 0 {
		t.Errorf("Bob still lists %+v", games)
	}
	_, err = svc.WidgetDraft(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	wantError(t, svc.RemoveGameMember(ctx, alice.ID, game.ID, bob.ID), http.StatusNotFound, "member_not_found") // twice
	if _, ok := rolesIn(t, svc, game)[carol.ID]; !ok {
		t.Error("removing Bob removed Carol")
	}
}

func TestLeaveGame(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	ctx := context.Background()

	if err := svc.LeaveGame(ctx, bob.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.MemberGame(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	wantError(t, svc.LeaveGame(ctx, bob.ID, game.ID), http.StatusNotFound, "game_not_found") // not on it any more
	wantError(t, svc.LeaveGame(ctx, aStranger, game.ID), http.StatusNotFound, "game_not_found")

	// The only owner cannot leave: the game would have none.
	wantError(t, svc.LeaveGame(ctx, alice.ID, game.ID), http.StatusConflict, "last_owner")
	if _, err := svc.MemberGame(ctx, alice.ID, game.ID); err != nil {
		t.Errorf("a refused leave took Alice off the game: %v", err)
	}
}

func TestAnOwnerLeavesOnlyAfterAnotherIsOwner(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	ctx := context.Background()

	wantError(t, svc.LeaveGame(ctx, alice.ID, game.ID), http.StatusConflict, "last_owner")
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LeaveGame(ctx, alice.ID, game.ID); err != nil {
		t.Fatalf("an owner with a co-owner could not leave: %v", err)
	}
	if owners := svc.store.(*fakeStore).ownersOf(game.ID); len(owners) != 1 || owners[0] != bob.ID {
		t.Errorf("owners = %v, want Bob alone", owners)
	}
	// Bob is the last owner now.
	wantError(t, svc.LeaveGame(ctx, bob.ID, game.ID), http.StatusConflict, "last_owner")
}

func TestSetMemberRole(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	ctx := context.Background()

	m, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner")
	if err != nil || m.DeveloperID != bob.ID || m.Role != "owner" || m.Email != "bob@example.com" {
		t.Fatalf("promote = %+v, %v", m, err)
	}
	// Bob can now do what owners do, and an owner demoting the other is fine while one is left.
	if _, err := svc.CreateAPIKey(ctx, bob.ID, game.ID); err != nil {
		t.Errorf("the new owner cannot create a key: %v", err)
	}
	if _, err := svc.SetMemberRole(ctx, bob.ID, game.ID, alice.ID, "member"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	// Alice is a member now, and Bob is the last owner: he cannot demote himself.
	_, err = svc.SetMemberRole(ctx, bob.ID, game.ID, bob.ID, "member")
	wantError(t, err, http.StatusConflict, "last_owner")
	_, err = svc.SetMemberRole(ctx, alice.ID, game.ID, carol.ID, "owner")
	wantError(t, err, http.StatusForbidden, "owner_required")

	// A role they have already is not a change.
	if m, err := svc.SetMemberRole(ctx, bob.ID, game.ID, bob.ID, "owner"); err != nil || m.Role != "owner" {
		t.Errorf("same role: %+v, %v", m, err)
	}
	for _, bad := range []string{"", "admin", "Owner", "owner "} {
		_, err := svc.SetMemberRole(ctx, bob.ID, game.ID, carol.ID, bad)
		wantError(t, err, http.StatusBadRequest, "invalid_role")
	}
	_, err = svc.SetMemberRole(ctx, bob.ID, game.ID, aStranger, "owner")
	wantError(t, err, http.StatusNotFound, "member_not_found")
	if rolesIn(t, svc, game)[carol.ID] != "member" {
		t.Error("a refused change moved Carol")
	}
}

func TestTransferOwnership(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	ctx := context.Background()

	_, err := svc.TransferOwnership(ctx, bob.ID, game.ID, carol.ID)
	wantError(t, err, http.StatusForbidden, "owner_required")
	_, err = svc.TransferOwnership(ctx, alice.ID, game.ID, aStranger)
	wantError(t, err, http.StatusNotFound, "member_not_found")
	_, err = svc.TransferOwnership(ctx, alice.ID, game.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "member_not_found")
	_, err = svc.TransferOwnership(ctx, alice.ID, game.ID, alice.ID) // to herself: she is an owner already
	wantError(t, err, http.StatusConflict, "already_owner")

	g, err := svc.TransferOwnership(ctx, alice.ID, game.ID, bob.ID)
	if err != nil || g.ID != game.ID || g.Role != "member" {
		t.Fatalf("transfer = %+v, %v; want the game as the old owner now sees it, as a member", g, err)
	}
	if roles := rolesIn(t, svc, game); roles[alice.ID] != "member" || roles[bob.ID] != "owner" || roles[carol.ID] != "member" {
		t.Errorf("roles = %v, want Bob the owner and the others members", roles)
	}
	// Alice lost what owners do; Bob has it. Neither left the game without an owner.
	if _, err := svc.CreateAPIKey(ctx, alice.ID, game.ID); err == nil {
		t.Error("the old owner can still create a key")
	}
	if _, err := svc.CreateAPIKey(ctx, bob.ID, game.ID); err != nil {
		t.Errorf("the new owner cannot create a key: %v", err)
	}
	// Handing it over again to somebody who is an owner already is refused.
	_, err = svc.TransferOwnership(ctx, bob.ID, game.ID, bob.ID)
	wantError(t, err, http.StatusConflict, "already_owner")
	if _, err := svc.TransferOwnership(ctx, alice.ID, game.ID, bob.ID); err == nil {
		t.Error("the old owner transferred a game she no longer owns")
	}
}

func TestTeamChangeErrorsThatAreNotAnswersAreWrapped(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	_ = bob
	// A store failure after the permission check is an internal error, not a client error.
	st := svc.store.(*fakeStore)
	ctx := context.Background()
	if _, err := svc.MemberGame(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	st.err = errors.New("db down")
	err := svc.RemoveGameMember(ctx, alice.ID, game.ID, "00000000-0000-4000-8000-0000000000aa")
	if err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("got %v, want an internal error", err)
	}
}

// --- Suspension together with the team -------------------------------------------------------------------------

// A game is served while its own switch is on and at least one owner is active. A suspended member never stops it, a
// suspended owner stops it only when no other owner is active, and a game with one owner behaves as before.
func TestAGameIsServedWhileAnOwnerIsActive(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	st := svc.store.(*fakeStore)
	ctx := context.Background()
	served := func() error { return svc.EnsureGameServed(ctx, game.ID) }

	st.suspendDeveloper(bob.ID) // a member
	if err := served(); err != nil {
		t.Errorf("a suspended member stopped the game: %v", err)
	}
	st.suspendDeveloper(alice.ID) // the only owner
	wantError(t, served(), http.StatusForbidden, "account_suspended")

	st.setRole(game.ID, carol.ID, "owner") // a second owner, active
	if err := served(); err != nil {
		t.Errorf("an active co-owner did not keep the game served: %v", err)
	}
	st.suspendDeveloper(carol.ID)
	wantError(t, served(), http.StatusForbidden, "account_suspended") // every owner is off

	// The game's own switch is independent, whoever is on the team.
	delete(st.suspendedDevs, alice.ID)
	if err := served(); err != nil {
		t.Fatal(err)
	}
	st.suspendGame(game.ID)
	wantError(t, served(), http.StatusForbidden, "game_suspended")
	st.setRole(game.ID, bob.ID, "owner")
	wantError(t, served(), http.StatusForbidden, "game_suspended") // adding an owner does not turn a suspended game on
}

func TestTeamChangesAnnounceWhenTheGameStopsOrStartsBeingServed(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	st := svc.store.(*fakeStore)
	rec := &st.committed
	ctx := context.Background()

	// Two owners; Alice is suspended, Bob is active: the game is served.
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec) // a promotion that changes nothing about serving announces nothing
	st.suspendDeveloper(alice.ID)

	// Bob, the last active owner, leaves: only suspended Alice is left, so the game stops being served.
	if err := svc.LeaveGame(ctx, bob.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, game.ID})
	wantError(t, svc.EnsureGameServed(ctx, game.ID), http.StatusForbidden, "account_suspended")

	// Carol is an active member: making her an owner turns the game back on. (Alice, suspended, cannot write, but the
	// service does not enforce that; the handler does.)
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, carol.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, game.ID}, [2]string{EventGameResumed, game.ID})
	if err := svc.EnsureGameServed(ctx, game.ID); err != nil {
		t.Errorf("the game is still off: %v", err)
	}

	// A transfer to an active member while the old owner is suspended also announces nothing new: Carol already is owner.
	// Removing a member changes nothing for serving.
	if err := svc.RemoveGameMember(ctx, carol.ID, game.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, game.ID}, [2]string{EventGameResumed, game.ID})
}

func TestTeamChangesAnnounceNothingForAGameSuspendedOnItsOwn(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	st := svc.store.(*fakeStore)
	st.suspendGame(game.ID)
	st.suspendDeveloper(alice.ID)
	ctx := context.Background()
	// Alice (suspended) is the only owner; making Bob an owner and Alice a member turns the account switch on, but the game
	// is off on its own, so the other services have nothing to learn.
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMemberRole(ctx, bob.ID, game.ID, alice.ID, "member"); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, &st.committed)
}

func TestARefusedTeamChangeAnnouncesNothing(t *testing.T) {
	svc, alice, _, _, game := teamSetup(t)
	st := svc.store.(*fakeStore)
	st.suspendDeveloper(alice.ID)
	// The last owner cannot leave, so nothing changed and nothing is announced.
	wantError(t, svc.LeaveGame(context.Background(), alice.ID, game.ID), http.StatusConflict, "last_owner")
	wantGameEvents(t, &st.committed)
}

// Suspending a developer announces the games that stop being served: the ones they own alone (or with only suspended
// co-owners). A game they are only a member of, or share with an active owner, keeps being served.
func TestSetDeveloperStatusAnnouncesOnlyGamesThatStopBeingServed(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	ctx := context.Background()
	store.adminGames = []repository.AdminGame{
		{ID: statusGame1, Status: StatusActive, Role: "owner"},                         // alone: stops
		{ID: statusGame2, Status: StatusActive, Role: "owner", OtherOwnerActive: true}, // an active co-owner: keeps going
		{ID: statusGame3, Status: StatusActive, Role: "member"},                        // only a member: keeps going
	}
	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusSuspended}
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, SetStatusInput{Status: StatusSuspended, Reason: ReasonNonPayment}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, statusGame1})

	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusActive}
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, SetStatusInput{Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, statusGame1}, [2]string{EventGameResumed, statusGame1})
}

func TestAdminGameDetailListsTheTeamWithTheirStatus(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	st := svc.store.(*fakeStore)
	st.suspendDeveloper(bob.ID)
	st.suspendGame(game.ID)
	ctx := context.Background()

	d, err := svc.AdminGameDetail(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != game.ID || d.Status != StatusSuspended || len(d.Members) != 3 {
		t.Fatalf("detail = %+v", d)
	}
	if d.Members[0].DeveloperID != alice.ID || d.Members[0].Role != "owner" || d.Members[0].Status != StatusActive {
		t.Errorf("first member = %+v, want the owner, active", d.Members[0])
	}
	var bobRow AdminGameMember
	for _, m := range d.Members {
		if m.DeveloperID == bob.ID {
			bobRow = m
		}
	}
	if bobRow.Role != "member" || bobRow.Status != StatusSuspended || bobRow.Email != "bob@example.com" {
		t.Errorf("Bob = %+v, want a suspended member with an email", bobRow)
	}
	for _, bad := range []string{"not-a-uuid", aStranger} {
		_, err := svc.AdminGameDetail(ctx, bad)
		wantError(t, err, http.StatusNotFound, "game_not_found")
	}
}

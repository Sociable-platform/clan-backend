package service

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// These methods make fakeStore (see developers_test.go) handle games like the real tables do:
// a game needs an existing developer, and lookups by ID are limited to the owner.

func (f *fakeStore) developerExists(id string) bool {
	for _, d := range f.byEmail {
		if d.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreateGame(_ context.Context, developerID, name, description string) (repository.Game, error) {
	if f.err != nil {
		return repository.Game{}, f.err
	}
	if !f.developerExists(developerID) {
		return repository.Game{}, repository.ErrNotFound
	}
	n := len(f.games) + 1
	g := repository.Game{
		ID:          "00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(strconv.Itoa(n))) + strconv.Itoa(n),
		DeveloperID: developerID, Role: repository.RoleOwner, Name: name, Description: description,
		CreatedAt: time.Unix(1_800_000_000+int64(n), 0),
	}
	f.games = append(f.games, g)
	return g, nil
}

func (f *fakeStore) GamesOfMember(_ context.Context, developerID string) ([]repository.Game, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.Game{}
	for i := len(f.games) - 1; i >= 0; i-- { // newest first
		if g, ok := f.gameAs(f.games[i], developerID); ok {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) GameOfMember(_ context.Context, gameID, developerID string) (repository.Game, error) {
	if f.err != nil {
		return repository.Game{}, f.err
	}
	for _, g := range f.games {
		if g.ID != gameID {
			continue
		}
		if g, ok := f.gameAs(g, developerID); ok {
			return g, nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

// removedRole in fakeStore.members marks a developer taken off a game, even the one who registered it.
const removedRole = "removed"

// team is who is on the game, by developer ID with the role: the developer who registered it is an owner (what
// CreateGame does in the database), and the members map adds, changes or (removedRole) removes the others.
func (f *fakeStore) team(gameID string) map[string]string {
	out := map[string]string{}
	for _, g := range f.games {
		if g.ID == gameID {
			out[g.DeveloperID] = repository.RoleOwner
		}
	}
	for key, role := range f.members {
		if id, found := strings.CutPrefix(key, gameID+"|"); found {
			if role == removedRole {
				delete(out, id)
			} else {
				out[id] = role
			}
		}
	}
	return out
}

func (f *fakeStore) setRole(gameID, developerID, role string) {
	if f.members == nil {
		f.members = map[string]string{}
	}
	f.members[gameID+"|"+developerID] = role
}

func (f *fakeStore) ownersOf(gameID string) []string {
	var ids []string
	for id, role := range f.team(gameID) {
		if role == repository.RoleOwner {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (f *fakeStore) memberRow(gameID, id, role string) repository.GameMember {
	m := repository.GameMember{DeveloperID: id, Name: "Member " + id, Email: id + "@example.com", Role: role, Status: "active", JoinedAt: time.Unix(1_800_000_000, 0)}
	for _, d := range f.byEmail {
		if d.ID == id {
			m.Name, m.Email = d.Name, d.Email
		}
	}
	if f.suspendedDevs[id] {
		m.Status = "suspended"
	}
	return m
}

// GameMembers lists the team, owners first and then by ID, like the SQL does.
func (f *fakeStore) GameMembers(_ context.Context, gameID string) ([]repository.GameMember, error) {
	if f.err != nil {
		return nil, f.err
	}
	team := f.team(gameID)
	ids := make([]string, 0, len(team))
	for id := range team {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		oi, oj := team[ids[i]] == repository.RoleOwner, team[ids[j]] == repository.RoleOwner
		if oi != oj {
			return oi
		}
		return ids[i] < ids[j]
	})
	out := []repository.GameMember{}
	for _, id := range ids {
		out = append(out, f.memberRow(gameID, id, team[id]))
	}
	return out, nil
}

// gameAs is the game with the role developerID has on it, or false when they do not belong to it.
func (f *fakeStore) gameAs(g repository.Game, developerID string) (repository.Game, bool) {
	role, ok := f.team(g.ID)[developerID]
	if !ok {
		return repository.Game{}, false
	}
	g.Role = role
	return g, true
}

// twoDevelopers registers two developers on one service.
func twoDevelopers(t *testing.T) (svc *Service, alice, bob Developer) {
	t.Helper()
	svc, _, _ = newTestService(t)
	var err error
	alice, err = svc.Register(context.Background(), RegisterInput{Email: "alice@example.com", Password: "correct horse", Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err = svc.Register(context.Background(), RegisterInput{Email: "bob@example.com", Password: "correct horse", Name: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, alice, bob
}

func TestRegisterGame_Valid(t *testing.T) {
	svc, alice, _ := twoDevelopers(t)

	g, err := svc.RegisterGame(context.Background(), alice.ID, RegisterGameInput{Name: "  Space Raiders ", Description: " Browser shooter "})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || g.Name != "Space Raiders" || g.Description != "Browser shooter" {
		t.Fatalf("unexpected game %+v", g)
	}
}

func TestRegisterGame_DescriptionIsOptional(t *testing.T) {
	svc, alice, _ := twoDevelopers(t)
	if _, err := svc.RegisterGame(context.Background(), alice.ID, RegisterGameInput{Name: "Space Raiders"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterGame_Validation(t *testing.T) {
	tests := []struct {
		name string
		in   RegisterGameInput
		code string
	}{
		{"empty name", RegisterGameInput{Name: ""}, "invalid_name"},
		{"blank name", RegisterGameInput{Name: "   "}, "invalid_name"},
		{"name too long", RegisterGameInput{Name: strings.Repeat("n", 101)}, "invalid_name"},
		{"description too long", RegisterGameInput{Name: "G", Description: strings.Repeat("d", 501)}, "invalid_description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, alice, _ := twoDevelopers(t)
			_, err := svc.RegisterGame(context.Background(), alice.ID, tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
	}

	t.Run("limits are inclusive", func(t *testing.T) {
		svc, alice, _ := twoDevelopers(t)
		in := RegisterGameInput{Name: strings.Repeat("n", 100), Description: strings.Repeat("d", 500)}
		if _, err := svc.RegisterGame(context.Background(), alice.ID, in); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRegisterGame_UnknownDeveloper(t *testing.T) {
	svc, _, _ := twoDevelopers(t)
	_, err := svc.RegisterGame(context.Background(), "00000000-0000-4000-8000-00000000dead", RegisterGameInput{Name: "G"})
	wantError(t, err, http.StatusUnauthorized, "unauthorized")
}

func TestListGames_OnlyOwn(t *testing.T) {
	svc, alice, bob := twoDevelopers(t)
	ctx := context.Background()
	for _, name := range []string{"A1", "A2"} {
		if _, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RegisterGame(ctx, bob.ID, RegisterGameInput{Name: "B1"}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ListGames(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "A2" || got[1].Name != "A1" {
		t.Fatalf("alice should see only her two games, newest first, got %+v", got)
	}

	got, err = svc.ListGames(ctx, bob.ID)
	if err != nil || len(got) != 1 || got[0].Name != "B1" {
		t.Fatalf("bob should see only B1, got %+v, %v", got, err)
	}
}

func TestListGames_NoGamesIsEmptyNotNil(t *testing.T) {
	svc, alice, _ := twoDevelopers(t)
	got, err := svc.ListGames(context.Background(), alice.ID)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v, %v", got, err)
	}
}

func TestMemberGame(t *testing.T) {
	svc, alice, bob := twoDevelopers(t)
	ctx := context.Background()
	aliceGame, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Space Raiders"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("owner gets the game", func(t *testing.T) {
		g, err := svc.MemberGame(ctx, alice.ID, aliceGame.ID)
		if err != nil || g.ID != aliceGame.ID {
			t.Fatalf("got %+v, %v", g, err)
		}
	})

	t.Run("another developer's game is 404", func(t *testing.T) {
		_, err := svc.MemberGame(ctx, bob.ID, aliceGame.ID)
		wantError(t, err, http.StatusNotFound, "game_not_found")
	})

	t.Run("answer is identical for a game that does not exist", func(t *testing.T) {
		_, errOther := svc.MemberGame(ctx, bob.ID, aliceGame.ID)
		_, errMissing := svc.MemberGame(ctx, bob.ID, "00000000-0000-4000-8000-00000000beef")
		wantError(t, errMissing, http.StatusNotFound, "game_not_found")
		if errOther.Error() != errMissing.Error() {
			t.Fatalf("responses differ: %q vs %q", errOther, errMissing)
		}
	})

	t.Run("malformed ID is 404 and never reaches the store", func(t *testing.T) {
		for _, id := range []string{"", "abc", "1; drop table games", aliceGame.ID + "x"} {
			_, err := svc.MemberGame(ctx, alice.ID, id)
			wantError(t, err, http.StatusNotFound, "game_not_found")
		}
	})
}

// Who belongs to a game, with which role, is the store's answer: the service passes the role on and treats
// everybody else as a stranger (404), however the membership came about.
func TestMemberGameAndListGamesCarryTheRole(t *testing.T) {
	svc, alice, bob := twoDevelopers(t)
	ctx := context.Background()
	store := svc.store.(*fakeStore)
	game, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Space Raiders"})
	if err != nil {
		t.Fatal(err)
	}
	if game.Role != "owner" {
		t.Errorf("RegisterGame role = %q, want owner: whoever registers a game owns it", game.Role)
	}

	if g, err := svc.MemberGame(ctx, alice.ID, game.ID); err != nil || g.Role != "owner" {
		t.Fatalf("owner: %+v, %v; want the owner role", g, err)
	}
	_, err = svc.MemberGame(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	if list, err := svc.ListGames(ctx, bob.ID); err != nil || len(list) != 0 {
		t.Fatalf("a stranger's list = %+v, %v; want none", list, err)
	}

	// Bob is added as a member: he gets the game, with that role, and it is in his list.
	store.members = map[string]string{game.ID + "|" + bob.ID: "member"}
	if g, err := svc.MemberGame(ctx, bob.ID, game.ID); err != nil || g.Role != "member" || g.ID != game.ID {
		t.Fatalf("member: %+v, %v; want the game with the member role", g, err)
	}
	list, err := svc.ListGames(ctx, bob.ID)
	if err != nil || len(list) != 1 || list[0].ID != game.ID || list[0].Role != "member" {
		t.Fatalf("a member's list = %+v, %v; want the game with the member role", list, err)
	}
	// The owner is unaffected, and the member going takes the access with it.
	if g, err := svc.MemberGame(ctx, alice.ID, game.ID); err != nil || g.Role != "owner" {
		t.Fatalf("owner after: %+v, %v", g, err)
	}
	delete(store.members, game.ID+"|"+bob.ID)
	_, err = svc.MemberGame(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
}

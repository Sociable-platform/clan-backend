package repository

import (
	"context"
	"fmt"
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

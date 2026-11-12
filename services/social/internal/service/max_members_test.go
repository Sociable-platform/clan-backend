package service

import (
	"context"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// gameRules is a gameconfig.Source with a fixed clan size per game; other games get the default.
type gameRules map[string]int

func (g gameRules) Rules(_ context.Context, gameID string) gameconfig.Rules {
	if n, ok := g[gameID]; ok {
		return gameconfig.Rules{MaxMembers: n, Published: true}
	}
	return gameconfig.Defaults()
}

// Every clan answer shows the game's clan size, whatever the clan row says.
func TestClanAnswersShowTheGamesClanSize(t *testing.T) {
	ctx := context.Background()
	rules := gameRules{gameA: 30}

	list := newClanStore(repository.Clan{ID: "c-1", Name: "Wolves", MemberCount: 3})
	clans, err := New(list).WithRules(rules).ListClans(ctx, gameA, ListClansInput{HasSpace: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(clans) != 1 || clans[0].MaxMembers != 30 || list.opts.MaxMembers != 30 {
		t.Fatalf("list: %+v, store opts %+v; want maxMembers 30 in both", clans, list.opts)
	}

	join := newJoiningStore()
	c, err := New(join).WithRules(rules).JoinClan(ctx, gameA, "u_42", "Ali", someClan)
	if err != nil {
		t.Fatal(err)
	}
	if join.limit != 30 || c.MaxMembers != 30 {
		t.Fatalf("join: store got limit %d, answer %+v; want 30", join.limit, c)
	}

	create := newCreatingStore()
	if c, err := New(create).WithRules(rules).CreateClan(ctx, gameA, "u_42", "Ali", "Wolves"); err != nil || c.MaxMembers != 30 {
		t.Fatalf("create: %+v, %v; want maxMembers 30", c, err)
	}

	mine := newReadingStore()
	mine.myClan = repository.PlayerClan{Clan: repository.Clan{ID: someClan, Name: "Wolves", MemberCount: 3}, Role: "owner"}
	if pc, err := New(mine).WithRules(rules).MyClan(ctx, gameA, "u_42"); err != nil || pc.Clan.MaxMembers != 30 {
		t.Fatalf("my clan: %+v, %v; want maxMembers 30", pc, err)
	}

	rename := newRenamingStore(true)
	if c, err := New(rename).WithRules(rules).RenameClan(ctx, gameA, "u_42", someClan, "Grey Wolves"); err != nil || c.MaxMembers != 30 {
		t.Fatalf("rename: %+v, %v; want maxMembers 30", c, err)
	}
}

// Each game has its own clan size; a game that never published has 10.
func TestEachGameHasItsOwnClanSize(t *testing.T) {
	ctx := context.Background()
	const gameB = "b0b0b0b0-1111-4222-8333-944455556666"
	rules := gameRules{gameA: 30}

	join := newJoiningStore()
	svc := New(join).WithRules(rules)
	if _, err := svc.JoinClan(ctx, gameB, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	if join.limit != gameconfig.DefaultMaxMembers {
		t.Fatalf("a game with no published config: limit %d, want %d", join.limit, gameconfig.DefaultMaxMembers)
	}
	if _, err := svc.JoinClan(ctx, gameA, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	if join.limit != 30 {
		t.Fatalf("game A: limit %d, want 30", join.limit)
	}
}

// A clan over a lowered limit shows more members than maxMembers: nobody was removed.
func TestAClanOverALoweredLimitShowsAsIs(t *testing.T) {
	list := newClanStore(repository.Clan{ID: "c-1", Name: "Wolves", MemberCount: 12})
	clans, err := New(list).WithRules(gameRules{gameA: 5}).ListClans(context.Background(), gameA, ListClansInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(clans) != 1 || clans[0].MemberCount != 12 || clans[0].MaxMembers != 5 {
		t.Fatalf("got %+v, want 12 members of 5", clans)
	}
}

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

const aStranger = "00000000-0000-4000-8000-0000000000ff" // a developer who is in no game

// Every operation on a game, for the three kinds of developer there are: an owner, a member and somebody who
// is not in the game. An owner may do everything; a member does the work on the game (draft, publish, roll
// back, read) but not what manages it (API keys, allowed origins), and gets 403 owner_required, not a 404,
// because they know the game; a stranger gets the 404 of a game that does not exist.
func TestWhatEachRoleMayDoOnAGame(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	saveDraft(t, svc, alice.ID, game.ID, cfg("One"))
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	key, err := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}

	type op struct {
		name      string
		ownerOnly bool
		notFound  string // the 404 code a stranger gets
		do        func(developerID string) error
	}
	ops := []op{
		{"create an API key", true, "game_not_found", func(d string) error { _, err := svc.CreateAPIKey(ctx, d, game.ID); return err }},
		{"list API keys", true, "game_not_found", func(d string) error { _, err := svc.ListAPIKeys(ctx, d, game.ID); return err }},
		{"revoke an API key", true, "api_key_not_found", func(d string) error { return svc.RevokeAPIKey(ctx, d, key.ID) }},
		{"set the allowed origins", true, "game_not_found", func(d string) error {
			_, err := svc.SetAllowedOrigins(ctx, d, game.ID, []string{"https://play.example.com"})
			return err
		}},
		{"read the draft", false, "game_not_found", func(d string) error { _, err := svc.WidgetDraft(ctx, d, game.ID); return err }},
		{"save the draft", false, "game_not_found", func(d string) error {
			_, err := svc.SaveWidgetDraft(ctx, d, game.ID, json.RawMessage(cfg("Two")))
			return err
		}},
		{"publish", false, "game_not_found", func(d string) error { _, err := svc.PublishWidget(ctx, d, game.ID); return err }},
		{"list the versions", false, "game_not_found", func(d string) error { _, err := svc.WidgetVersions(ctx, d, game.ID); return err }},
		{"read a version", false, "game_not_found", func(d string) error { _, err := svc.WidgetVersion(ctx, d, game.ID, 1); return err }},
		{"roll back", false, "game_not_found", func(d string) error { _, err := svc.RollbackWidget(ctx, d, game.ID, 1); return err }},
		{"list the team", false, "game_not_found", func(d string) error { _, err := svc.GameMembers(ctx, d, game.ID); return err }},
	}
	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			// A stranger first: nothing they do changes anything, so the owner and the member below see the game as it was.
			wantError(t, o.do(aStranger), http.StatusNotFound, o.notFound)
			if o.ownerOnly {
				wantError(t, o.do(bob.ID), http.StatusForbidden, "owner_required")
			} else if err := o.do(bob.ID); err != nil {
				t.Errorf("a member: %v, want it allowed", err)
			}
			if err := o.do(alice.ID); err != nil {
				t.Errorf("the owner: %v, want it allowed", err)
			}
		})
	}
}

// A member's work is theirs: publishing records who published, so the history shows who did what.
func TestAMemberPublishesAndTheVersionSaysWho(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	saveDraft(t, svc, bob.ID, game.ID, cfg("Member's"))
	v, err := svc.PublishWidget(ctx, bob.ID, game.ID)
	if err != nil || v.PublishedBy != bob.ID {
		t.Fatalf("published by %q, %v; want the member who published", v.PublishedBy, err)
	}
	v2, err := svc.PublishWidget(ctx, alice.ID, game.ID)
	if err != nil || v2.PublishedBy != alice.ID {
		t.Fatalf("published by %q, %v; want the owner", v2.PublishedBy, err)
	}
}

// OwnerGame is the one check behind the owner-only actions.
func TestOwnerGame(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	if g, err := svc.OwnerGame(ctx, alice.ID, game.ID); err != nil || g.Role != "owner" {
		t.Fatalf("owner: %+v, %v", g, err)
	}
	_, err := svc.OwnerGame(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusForbidden, "owner_required")
	_, err = svc.OwnerGame(ctx, aStranger, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.OwnerGame(ctx, alice.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "game_not_found")
	// Being refused is the same for a stranger whether or not the game exists: nobody learns a game exists by it.
	_, errReal := svc.OwnerGame(ctx, aStranger, game.ID)
	_, errMissing := svc.OwnerGame(ctx, aStranger, "00000000-0000-4000-8000-00000000beef")
	if errReal.Error() != errMissing.Error() {
		t.Errorf("a stranger's answers differ: %q vs %q", errReal, errMissing)
	}
}

func TestGameMembersListsTheTeamOwnersFirst(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	for _, developer := range []string{alice.ID, bob.ID} { // a member sees the team as well as the owner does
		got, err := svc.GameMembers(ctx, developer, game.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("members = %+v, %v; want the owner and the member", got, err)
		}
		if got[0].DeveloperID != alice.ID || got[0].Role != "owner" || got[1].DeveloperID != bob.ID || got[1].Role != "member" {
			t.Errorf("members = %+v, want the owner first, then the member", got)
		}
		if got[0].Name == "" || got[0].Email == "" || got[0].JoinedAt.IsZero() {
			t.Errorf("a member row is missing its name, email or join time: %+v", got[0])
		}
	}
}

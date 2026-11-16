package service

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

// issueFor runs the same two steps the endpoint does: check the key, then issue the token.
func issueFor(t *testing.T, svc *Service, apiKey string, in PlayerTokenInput) (PlayerToken, string, error) {
	t.Helper()
	gameID, err := svc.GameForAPIKey(context.Background(), apiKey)
	if err != nil {
		return PlayerToken{}, "", err
	}
	tok, err := svc.IssuePlayerToken(gameID, in)
	return tok, gameID, err
}

func TestIssuePlayerToken_ValidKey(t *testing.T) {
	svc, _, key := newTestService(t)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	alice := mustRegister(t, svc, "alice@example.com")
	game, err := svc.RegisterGame(context.Background(), alice.ID, RegisterGameInput{Name: "Space Raiders"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := svc.CreateAPIKey(context.Background(), alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}

	tok, gameID, err := issueFor(t, svc, k.Key, PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Rashad"})
	if err != nil {
		t.Fatal(err)
	}
	if gameID != game.ID {
		t.Fatalf("key resolved to game %q, want %q", gameID, game.ID)
	}
	if tok.ExpiresIn != 3600 {
		t.Fatalf("expiresIn %d, want 3600", tok.ExpiresIn)
	}

	// Verify it the way social does: with the PUBLIC key only.
	pub := key.Public().(ed25519.PublicKey)
	c, err := jwt.Verify(pub, tok.Token, jwt.TypePlayer, now)
	if err != nil {
		t.Fatalf("social could not verify the token: %v", err)
	}
	want := jwt.Claims{
		Subject: "u_42", GameID: game.ID, Name: "Rashad", Type: jwt.TypePlayer,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	if c != want {
		t.Fatalf("claims %+v, want %+v", c, want)
	}

	// Valid for the whole hour, dead right after it.
	if _, err := jwt.Verify(pub, tok.Token, jwt.TypePlayer, now.Add(time.Hour-time.Second)); err != nil {
		t.Fatalf("token should still be valid just before 1 hour: %v", err)
	}
	if _, err := jwt.Verify(pub, tok.Token, jwt.TypePlayer, now.Add(time.Hour)); err != jwt.ErrExpired {
		t.Fatalf("token should be expired after 1 hour, got %v", err)
	}
	// A player token is never accepted where a developer token is needed.
	if _, err := svc.AuthenticateDeveloper(context.Background(), tok.Token); err == nil {
		t.Fatal("a player token must not authenticate a developer")
	}
}

func TestIssuePlayerToken_TokenCannotBeForgedWithAnotherKey(t *testing.T) {
	svc, _, _ := newTestService(t)
	other, _, _ := newTestService(t) // a service with a different signing key

	tok, err := svc.IssuePlayerToken("0d6f2c0e-5b1a-4f5e-8f55-2f4f0c7a9b11", PlayerTokenInput{ExternalPlayerID: "u_1", DisplayName: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Verify(other.pub, tok.Token, jwt.TypePlayer, time.Now()); err != jwt.ErrSignature {
		t.Fatalf("token must not verify with a different public key, got %v", err)
	}
}

func TestIssuePlayerToken_RejectsBadKeys(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)
	ctx := context.Background()
	live, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	revoked, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	if err := svc.RevokeAPIKey(ctx, alice.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	in := PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Rashad"}

	for name, key := range map[string]string{
		"missing":      "",
		"wrong":        "clk_" + strings.Repeat("A", 43),
		"revoked":      revoked.Key,
		"no marker":    strings.TrimPrefix(live.Key, "clk_"),
		"hash as key":  hashKey(live.Key),
		"only spaces":  "   ",
		"live + extra": live.Key + " ",
	} {
		t.Run(name, func(t *testing.T) {
			tok, _, err := issueFor(t, svc, key, in)
			wantError(t, err, http.StatusUnauthorized, "invalid_api_key")
			if tok.Token != "" {
				t.Fatal("no token may be issued for a bad key")
			}
		})
	}

	// The live key still works after the others failed.
	if _, _, err := issueFor(t, svc, live.Key, in); err != nil {
		t.Fatalf("live key: %v", err)
	}
}

func TestIssuePlayerToken_RevokingAKeyStopsNewTokens(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)
	ctx := context.Background()
	k, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	in := PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Rashad"}

	if _, _, err := issueFor(t, svc, k.Key, in); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAPIKey(ctx, alice.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err := issueFor(t, svc, k.Key, in)
	wantError(t, err, http.StatusUnauthorized, "invalid_api_key")
}

func TestIssuePlayerToken_GameComesFromTheKey(t *testing.T) {
	svc, _, alice, _, gameA := gameOfAlice(t)
	ctx := context.Background()
	gameB, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Second Game"})
	if err != nil {
		t.Fatal(err)
	}
	ka, _ := svc.CreateAPIKey(ctx, alice.ID, gameA.ID)
	kb, _ := svc.CreateAPIKey(ctx, alice.ID, gameB.ID)
	in := PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Rashad"} // same player ID in both games

	for _, tt := range []struct{ key, game string }{{ka.Key, gameA.ID}, {kb.Key, gameB.ID}} {
		tok, _, err := issueFor(t, svc, tt.key, in)
		if err != nil {
			t.Fatal(err)
		}
		c, err := jwt.Verify(svc.pub, tok.Token, jwt.TypePlayer, time.Now())
		if err != nil || c.GameID != tt.game {
			t.Fatalf("token game %q (err %v), want %q", c.GameID, err, tt.game)
		}
	}
}

func TestIssuePlayerToken_ValidatesThePlayer(t *testing.T) {
	svc, _, _ := newTestService(t)
	const game = "0d6f2c0e-5b1a-4f5e-8f55-2f4f0c7a9b11"

	for _, tt := range []struct {
		name string
		in   PlayerTokenInput
		code string
	}{
		{"no player id", PlayerTokenInput{DisplayName: "Rashad"}, "invalid_external_player_id"},
		{"blank player id", PlayerTokenInput{ExternalPlayerID: "  ", DisplayName: "Rashad"}, "invalid_external_player_id"},
		{"long player id", PlayerTokenInput{ExternalPlayerID: strings.Repeat("a", 129), DisplayName: "Rashad"}, "invalid_external_player_id"},
		{"control char in player id", PlayerTokenInput{ExternalPlayerID: "u_\n42", DisplayName: "Rashad"}, "invalid_external_player_id"},
		{"no display name", PlayerTokenInput{ExternalPlayerID: "u_42"}, "invalid_display_name"},
		{"blank display name", PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: " \t "}, "invalid_display_name"},
		{"long display name", PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: strings.Repeat("é", 51)}, "invalid_display_name"},
		{"control char in display name", PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Ra\x00shad"}, "invalid_display_name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.IssuePlayerToken(game, tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
	}

	t.Run("limits are inclusive and counted in characters", func(t *testing.T) {
		in := PlayerTokenInput{ExternalPlayerID: strings.Repeat("a", 128), DisplayName: strings.Repeat("é", 50)}
		if _, err := svc.IssuePlayerToken(game, in); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("surrounding spaces are trimmed", func(t *testing.T) {
		tok, err := svc.IssuePlayerToken(game, PlayerTokenInput{ExternalPlayerID: " u_42 ", DisplayName: " Rashad "})
		if err != nil {
			t.Fatal(err)
		}
		c, _ := jwt.Verify(svc.pub, tok.Token, jwt.TypePlayer, time.Now())
		if c.Subject != "u_42" || c.Name != "Rashad" {
			t.Fatalf("claims %+v", c)
		}
	})
}

func mustRegister(t *testing.T, svc *Service, email string) Developer {
	t.Helper()
	d, err := svc.Register(context.Background(), RegisterInput{Email: email, Password: "correct horse", Name: "Dev"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

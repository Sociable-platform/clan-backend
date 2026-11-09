package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

const gameID = "A6C1E2F0-1111-4222-8333-944455556666"

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(t *testing.T, key ed25519.PrivateKey, c jwt.Claims, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.Sign(key, c, ttl, now)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestVerify(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)
	player := jwt.Claims{Subject: "u_42", GameID: gameID, Name: "Ali", Type: jwt.TypePlayer}

	tests := []struct {
		name     string
		token    string
		wantCode string // empty = success
	}{
		{"valid player token", sign(t, priv, player, time.Hour), ""},
		{"empty token", "", "missing_token"},
		{"blank token", "   ", "missing_token"},
		{"not a token", "not-a-jwt", "invalid_token"},
		{"signed by another key", sign(t, otherPriv, player, time.Hour), "invalid_token"},
		{"expired", sign(t, priv, player, -time.Minute), "token_expired"},
		{"developer token", sign(t, priv, jwt.Claims{Subject: "dev_1", Type: jwt.TypeDeveloper}, time.Hour), "wrong_token_type"},
		{"no player in token", sign(t, priv, jwt.Claims{GameID: gameID, Type: jwt.TypePlayer}, time.Hour), "invalid_token"},
		{"no game in token", sign(t, priv, jwt.Claims{Subject: "u_42", Type: jwt.TypePlayer}, time.Hour), "invalid_token"},
		{"game is not a UUID", sign(t, priv, jwt.Claims{Subject: "u_42", GameID: "game-1", Type: jwt.TypePlayer}, time.Hour), "invalid_token"},
	}
	v := NewVerifier(pub, func() time.Time { return now })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Verify(tt.token)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Verify() error = %v", err)
				}
				if got.GameID != strings.ToLower(gameID) || got.ExternalID != "u_42" || got.Name != "Ali" {
					t.Fatalf("Verify() = %+v", got)
				}
				if !got.ExpiresAt.Equal(now.Add(time.Hour)) {
					t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, now.Add(time.Hour))
				}
				return
			}
			var apiErr *httpx.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *httpx.Error", err)
			}
			if apiErr.Status != http.StatusUnauthorized || apiErr.Code != tt.wantCode {
				t.Fatalf("error = %d %s, want 401 %s", apiErr.Status, apiErr.Code, tt.wantCode)
			}
			if tt.token != "" && strings.Contains(apiErr.Message, tt.token) {
				t.Fatalf("error message contains the token: %q", apiErr.Message)
			}
		})
	}
}

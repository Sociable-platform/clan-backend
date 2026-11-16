package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// refresh exchanges a refresh token and fails the test when that does not work.
func refresh(t *testing.T, svc *Service, refreshToken string) Session {
	t.Helper()
	s, err := svc.RefreshSession(context.Background(), refreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return s
}

func wantRefreshRefused(t *testing.T, svc *Service, refreshToken string) {
	t.Helper()
	_, err := svc.RefreshSession(context.Background(), refreshToken)
	wantError(t, err, http.StatusUnauthorized, "invalid_refresh_token")
}

func TestLogin_StartsARefreshSession(t *testing.T) {
	svc, store, _, _ := resetService(t)

	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if s.RefreshToken == "" || s.RefreshToken == s.Token {
		t.Fatalf("refresh token %q", s.RefreshToken)
	}
	if want := int((7 * 24 * time.Hour).Seconds()); s.RefreshExpiresIn != want {
		t.Fatalf("refresh expires in %d s, want %d", s.RefreshExpiresIn, want)
	}

	// Only the hash of the token is stored.
	if _, ok := store.refreshTokens[s.RefreshToken]; ok {
		t.Fatal("the refresh token itself was stored")
	}
	stored, ok := store.refreshTokens[hashOpaqueToken(s.RefreshToken)]
	if !ok {
		t.Fatal("the hash of the refresh token was not stored")
	}
	if want := svc.now().Add(7 * 24 * time.Hour); !stored.expiresAt.Equal(want) {
		t.Fatalf("expires at %v, want %v", stored.expiresAt, want)
	}
	if stored.replacedAt != nil {
		t.Fatal("a fresh token must not be marked as replaced")
	}
}

func TestLogin_WrongPasswordStartsNoSession(t *testing.T) {
	svc, store, _, _ := resetService(t)
	if _, err := login(svc, "wrong password"); err == nil {
		t.Fatal("want an error")
	}
	if len(store.refreshTokens) != 0 {
		t.Fatalf("%d refresh tokens stored after a failed login", len(store.refreshTokens))
	}
}

func TestAdminLogin_HasNoRefreshToken(t *testing.T) {
	svc, store, _ := newTestService(t)
	addAdmin(t, store, "ops@example.com", nil)

	s, err := svc.AdminLogin(context.Background(), LoginInput{Email: "ops@example.com", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}
	if s.RefreshToken != "" || s.RefreshExpiresIn != 0 || len(store.refreshTokens) != 0 {
		t.Fatalf("an admin login must not start a refresh session: %+v", s)
	}
}

func TestRefreshSession_RotatesTheToken(t *testing.T) {
	svc, store, _, now := resetService(t)
	first, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	*now = now.Add(20 * time.Minute)
	second := refresh(t, svc, first.RefreshToken)

	if second.RefreshToken == "" || second.RefreshToken == first.RefreshToken {
		t.Fatalf("the refresh token was not replaced: %q", second.RefreshToken)
	}
	if second.Token == first.Token {
		t.Fatal("the developer token was not replaced")
	}
	id, err := svc.AuthenticateDeveloper(context.Background(), second.Token)
	if err != nil || id != "dev-1" {
		t.Fatalf("the new developer token does not work: %q, %v", id, err)
	}

	// Both rows belong to one session; the old one is marked, the new one runs for 7 days from now.
	old := store.refreshTokens[hashOpaqueToken(first.RefreshToken)]
	next := store.refreshTokens[hashOpaqueToken(second.RefreshToken)]
	if old.replacedAt == nil || !old.replacedAt.Equal(*now) {
		t.Fatalf("old token replaced at %v, want %v", old.replacedAt, *now)
	}
	if next.sessionID != old.sessionID || next.replacedAt != nil {
		t.Fatalf("new token %+v, old token %+v", next, old)
	}
	if want := now.Add(7 * 24 * time.Hour); !next.expiresAt.Equal(want) {
		t.Fatalf("new token expires at %v, want %v", next.expiresAt, want)
	}

	// The chain goes on.
	*now = now.Add(20 * time.Minute)
	third := refresh(t, svc, second.RefreshToken)
	if third.RefreshToken == second.RefreshToken {
		t.Fatal("the second refresh did not replace the token")
	}
}

func TestRefreshSession_AReplacedTokenEndsItsSessionOnly(t *testing.T) {
	svc, _, _, now := resetService(t)
	laptop, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	office, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	current := refresh(t, svc, laptop.RefreshToken)

	// Somebody sends the laptop's first token again, long after it was replaced.
	*now = now.Add(time.Minute)
	wantRefreshRefused(t, svc, laptop.RefreshToken)

	// The laptop's session is over, its current token included ...
	wantRefreshRefused(t, svc, current.RefreshToken)
	// ... and the other login of the same developer is not touched.
	refresh(t, svc, office.RefreshToken)
}

func TestRefreshSession_TheSameRefreshTwiceIsNotATheft(t *testing.T) {
	svc, store, _, now := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	first := refresh(t, svc, s.RefreshToken)
	*now = now.Add(10 * time.Second) // the answer was lost; the browser asks again
	again := refresh(t, svc, s.RefreshToken)

	if again.RefreshToken == first.RefreshToken {
		t.Fatal("each answer must carry its own token")
	}
	// Both answers work, in the same session.
	a := store.refreshTokens[hashOpaqueToken(first.RefreshToken)]
	b := store.refreshTokens[hashOpaqueToken(again.RefreshToken)]
	if a.sessionID == "" || a.sessionID != b.sessionID {
		t.Fatalf("sessions %q and %q", a.sessionID, b.sessionID)
	}
	refresh(t, svc, again.RefreshToken)

	// One second later it would have been reuse.
	*now = now.Add(11 * time.Second)
	wantRefreshRefused(t, svc, s.RefreshToken)
}

func TestRefreshSession_RefusesBadTokens(t *testing.T) {
	svc, _, _, now := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	for name, bad := range map[string]string{
		"empty":           "",
		"unknown":         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"too long":        strings.Repeat("a", 500),
		"a developer JWT": s.Token,
	} {
		t.Run(name, func(t *testing.T) {
			wantRefreshRefused(t, svc, bad)
		})
	}

	t.Run("expired", func(t *testing.T) {
		*now = now.Add(7 * 24 * time.Hour)
		wantRefreshRefused(t, svc, s.RefreshToken)
	})
}

func TestRefreshSession_KeepsAnActiveDeveloperLoggedIn(t *testing.T) {
	svc, _, _, now := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	// Six days between visits, for a month: each refresh gives 7 more days.
	for i := 0; i < 5; i++ {
		*now = now.Add(6 * 24 * time.Hour)
		s = refresh(t, svc, s.RefreshToken)
	}
}

func TestRefreshSession_SuspendedDeveloperCanRefresh(t *testing.T) {
	svc, store, _, _ := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	store.suspendedDevs = map[string]bool{"dev-1": true}
	refresh(t, svc, s.RefreshToken)
}

func TestRefreshSession_AccountGone(t *testing.T) {
	svc, store, _, _ := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	delete(store.byEmail, "dev@example.com")
	wantRefreshRefused(t, svc, s.RefreshToken)
}

func TestRefreshSession_UsesTheConfiguredLifetime(t *testing.T) {
	svc, _, _, now := resetService(t)
	svc.WithRefreshTokenTTL(time.Hour)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if s.RefreshExpiresIn != 3600 {
		t.Fatalf("refresh expires in %d s, want 3600", s.RefreshExpiresIn)
	}
	*now = now.Add(time.Hour)
	wantRefreshRefused(t, svc, s.RefreshToken)
}

func TestLogout_EndsThatSessionOnly(t *testing.T) {
	svc, store, _, _ := resetService(t)
	ctx := context.Background()
	laptop, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	office, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	current := refresh(t, svc, laptop.RefreshToken)

	if err := svc.Logout(ctx, current.RefreshToken); err != nil {
		t.Fatal(err)
	}
	wantRefreshRefused(t, svc, current.RefreshToken)
	wantRefreshRefused(t, svc, laptop.RefreshToken)
	if len(store.refreshTokens) != 1 {
		t.Fatalf("%d refresh tokens left, want the office's one", len(store.refreshTokens))
	}
	refresh(t, svc, office.RefreshToken)

	// Logging out again, or without a session, is not an error.
	for _, token := range []string{current.RefreshToken, "", "unknown", strings.Repeat("a", 500)} {
		if err := svc.Logout(ctx, token); err != nil {
			t.Fatalf("logout with %q: %v", token, err)
		}
	}
}

func TestResetPassword_EndsEveryRefreshSession(t *testing.T) {
	svc, store, sender, _ := resetService(t)
	ctx := context.Background()
	laptop, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	office, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, ResetPasswordInput{Token: sender.lastToken(t), Password: "a brand new one"}); err != nil {
		t.Fatal(err)
	}

	wantRefreshRefused(t, svc, laptop.RefreshToken)
	wantRefreshRefused(t, svc, office.RefreshToken)
	if len(store.refreshTokens) != 0 {
		t.Fatalf("%d refresh tokens left after the reset", len(store.refreshTokens))
	}
}

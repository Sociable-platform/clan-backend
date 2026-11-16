package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sentReset struct{ to, name, link string }

// recordingSender keeps the reset links instead of emailing them.
type recordingSender struct{ sent []sentReset }

func (r *recordingSender) SendPasswordReset(to, name, link string) {
	r.sent = append(r.sent, sentReset{to, name, link})
}

// lastToken returns the token of the newest reset link.
func (r *recordingSender) lastToken(t *testing.T) string {
	t.Helper()
	if len(r.sent) == 0 {
		t.Fatal("no reset link was sent")
	}
	_, token, found := strings.Cut(r.sent[len(r.sent)-1].link, "#token=")
	if !found || token == "" {
		t.Fatalf("link %q has no token", r.sent[len(r.sent)-1].link)
	}
	return token
}

// resetService returns a service with one registered developer (validRegister), a clock the test
// moves, and a sender that records the links.
func resetService(t *testing.T) (*Service, *fakeStore, *recordingSender, *time.Time) {
	t.Helper()
	svc, store, _ := newTestService(t)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	sender := &recordingSender{}
	svc.WithPasswordReset(sender, "https://studio.example.com/")
	if _, err := svc.Register(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	return svc, store, sender, &now
}

func login(svc *Service, password string) (Session, error) {
	return svc.Login(context.Background(), LoginInput{Email: "dev@example.com", Password: password})
}

func TestRequestPasswordReset_SendsALink(t *testing.T) {
	svc, store, sender, _ := resetService(t)

	if err := svc.RequestPasswordReset(context.Background(), "  Dev@Example.com "); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("%d links sent, want 1", len(sender.sent))
	}
	got := sender.sent[0]
	if got.to != "dev@example.com" || got.name != "Dev One" {
		t.Fatalf("sent to %q (%q)", got.to, got.name)
	}
	if !strings.HasPrefix(got.link, "https://studio.example.com/reset-password#token=") {
		t.Fatalf("link %q", got.link)
	}

	// Only the hash of the token is stored.
	token := sender.lastToken(t)
	if len(store.resetTokens) != 1 {
		t.Fatalf("%d tokens stored, want 1", len(store.resetTokens))
	}
	if _, ok := store.resetTokens[token]; ok {
		t.Fatal("the token itself was stored")
	}
	stored, ok := store.resetTokens[hashResetToken(token)]
	if !ok {
		t.Fatal("the hash of the token was not stored")
	}
	if want := svc.now().Add(30 * time.Minute); !stored.expiresAt.Equal(want) {
		t.Fatalf("expires at %v, want %v", stored.expiresAt, want)
	}
}

func TestRequestPasswordReset_UnknownEmailLooksTheSame(t *testing.T) {
	svc, store, sender, _ := resetService(t)

	if err := svc.RequestPasswordReset(context.Background(), "nobody@example.com"); err != nil {
		t.Fatalf("an unknown email must not be an error, got %v", err)
	}
	if len(sender.sent) != 0 || len(store.resetTokens) != 0 {
		t.Fatalf("sent %d links, stored %d tokens for an unknown email", len(sender.sent), len(store.resetTokens))
	}
}

func TestRequestPasswordReset_InvalidEmail(t *testing.T) {
	svc, _, _, _ := resetService(t)
	wantError(t, svc.RequestPasswordReset(context.Background(), "not an email"), http.StatusBadRequest, "invalid_email")
}

func TestResetPassword_SetsTheNewPasswordOnce(t *testing.T) {
	svc, _, sender, _ := resetService(t)
	ctx := context.Background()
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	token := sender.lastToken(t)

	if err := svc.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "a brand new one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := login(svc, "correct horse"); err == nil {
		t.Fatal("the old password still works")
	}
	if _, err := login(svc, "a brand new one"); err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}

	// The link works once.
	err := svc.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "yet another one"})
	wantError(t, err, http.StatusBadRequest, "invalid_reset_token")
	if _, err := login(svc, "a brand new one"); err != nil {
		t.Fatalf("a used link changed the password again: %v", err)
	}
}

func TestResetPassword_RefusesBadTokens(t *testing.T) {
	svc, _, sender, now := resetService(t)
	ctx := context.Background()
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	token := sender.lastToken(t)

	for name, bad := range map[string]string{
		"empty":    "",
		"unknown":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"too long": strings.Repeat("a", 500),
	} {
		t.Run(name, func(t *testing.T) {
			err := svc.ResetPassword(ctx, ResetPasswordInput{Token: bad, Password: "a brand new one"})
			wantError(t, err, http.StatusBadRequest, "invalid_reset_token")
		})
	}

	t.Run("expired", func(t *testing.T) {
		*now = now.Add(30 * time.Minute)
		err := svc.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "a brand new one"})
		wantError(t, err, http.StatusBadRequest, "invalid_reset_token")
		if _, err := login(svc, "correct horse"); err != nil {
			t.Fatalf("an expired link changed the password: %v", err)
		}
	})
}

func TestResetPassword_AWeakPasswordDoesNotUseTheLinkUp(t *testing.T) {
	svc, _, sender, _ := resetService(t)
	ctx := context.Background()
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	token := sender.lastToken(t)

	wantError(t, svc.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "short"}), http.StatusBadRequest, "invalid_password")
	if err := svc.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "a brand new one"}); err != nil {
		t.Fatalf("the link was used up by the refused password: %v", err)
	}
}

func TestRequestPasswordReset_ANewLinkReplacesTheOldOne(t *testing.T) {
	svc, _, sender, _ := resetService(t)
	ctx := context.Background()
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	first := sender.lastToken(t)
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	second := sender.lastToken(t)
	if first == second {
		t.Fatal("both links carry the same token")
	}

	wantError(t, svc.ResetPassword(ctx, ResetPasswordInput{Token: first, Password: "a brand new one"}), http.StatusBadRequest, "invalid_reset_token")
	if err := svc.ResetPassword(ctx, ResetPasswordInput{Token: second, Password: "a brand new one"}); err != nil {
		t.Fatalf("the newest link does not work: %v", err)
	}
}

func TestResetPassword_LogsOutEarlierSessions(t *testing.T) {
	svc, _, sender, now := resetService(t)
	ctx := context.Background()

	before, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDeveloper(ctx, before.Token); err != nil {
		t.Fatalf("the token must work before the reset: %v", err)
	}

	*now = now.Add(10 * time.Second)
	if err := svc.RequestPasswordReset(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, ResetPasswordInput{Token: sender.lastToken(t), Password: "a brand new one"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AuthenticateDeveloper(ctx, before.Token)
	wantError(t, err, http.StatusUnauthorized, "unauthorized")

	*now = now.Add(10 * time.Second)
	after, err := login(svc, "a brand new one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDeveloper(ctx, after.Token); err != nil {
		t.Fatalf("a login after the reset must work: %v", err)
	}
}

func TestAuthenticateDeveloper_AccountGone(t *testing.T) {
	svc, store, _, _ := resetService(t)
	s, err := login(svc, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	delete(store.byEmail, "dev@example.com")
	_, err = svc.AuthenticateDeveloper(context.Background(), s.Token)
	wantError(t, err, http.StatusUnauthorized, "unauthorized")
}

package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

func (f *fakeStore) AdminByEmail(_ context.Context, email string) (repository.Admin, error) {
	if f.err != nil {
		return repository.Admin{}, f.err
	}
	a, ok := f.admins[email]
	if !ok {
		return repository.Admin{}, repository.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) AdminByID(_ context.Context, id string) (repository.Admin, error) {
	if f.err != nil {
		return repository.Admin{}, f.err
	}
	for _, a := range f.admins {
		if a.ID == id {
			return a, nil
		}
	}
	return repository.Admin{}, repository.ErrNotFound
}

const adminPassword = "correct horse"

// addAdmin puts an admin with adminPassword into the store, the way tools/createadmin would.
func addAdmin(t *testing.T, store *fakeStore, email string, disabledAt *time.Time) repository.Admin {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a := repository.Admin{
		ID: "admin-" + email, Email: email, PasswordHash: string(hash), Name: "Ops One",
		CreatedAt: time.Now(), DisabledAt: disabledAt,
	}
	store.admins[email] = a
	return a
}

func TestAdminLogin_ReturnsAdminToken(t *testing.T) {
	svc, store, key := newTestService(t)
	a := addAdmin(t, store, "ops@example.com", nil)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }

	s, err := svc.AdminLogin(context.Background(), LoginInput{Email: " OPS@example.com ", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}
	if s.ExpiresIn != 3600 {
		t.Fatalf("expiresIn %d, want the 1 h default (3600)", s.ExpiresIn)
	}
	pub := key.Public().(ed25519.PublicKey)
	c, err := jwt.Verify(pub, s.Token, jwt.TypeAdmin, now)
	if err != nil {
		t.Fatalf("token does not verify as an admin token: %v", err)
	}
	if c.Subject != a.ID || c.Type != jwt.TypeAdmin || c.GameID != "" || c.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("unexpected claims %+v", c)
	}
	for _, typ := range []string{jwt.TypePlayer, jwt.TypeDeveloper} {
		if _, err := jwt.Verify(pub, s.Token, typ, now); !errors.Is(err, jwt.ErrType) {
			t.Fatalf("admin token must not verify as %s, got %v", typ, err)
		}
	}
}

func TestAdminLogin_TokenTTLIsConfigurable(t *testing.T) {
	svc, store, _ := newTestService(t)
	addAdmin(t, store, "ops@example.com", nil)
	svc.WithAdminTokenTTL(15 * time.Minute)

	s, err := svc.AdminLogin(context.Background(), LoginInput{Email: "ops@example.com", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}
	if s.ExpiresIn != 900 {
		t.Fatalf("expiresIn %d, want 900", s.ExpiresIn)
	}
}

func TestAdminLogin_Refused(t *testing.T) {
	svc, store, _ := newTestService(t)
	addAdmin(t, store, "ops@example.com", nil)
	gone := time.Now().Add(-time.Hour)
	addAdmin(t, store, "left@example.com", &gone)
	// A developer account is not an admin account, even with the same email and password.
	if _, err := svc.Register(context.Background(), RegisterInput{Email: "dev@example.com", Password: adminPassword, Name: "Dev"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, email, password string
	}{
		{"wrong password", "ops@example.com", "not the password"},
		{"empty password", "ops@example.com", ""},
		{"unknown email", "nobody@example.com", adminPassword},
		{"disabled admin with the right password", "left@example.com", adminPassword},
		{"developer account", "dev@example.com", adminPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AdminLogin(context.Background(), LoginInput{Email: tt.email, Password: tt.password})
			wantError(t, err, http.StatusUnauthorized, "invalid_credentials")
		})
	}
}

func TestAdminLogin_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	_, err := svc.AdminLogin(context.Background(), LoginInput{Email: "ops@example.com", Password: adminPassword})
	var e *httpx.Error
	if err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

func TestAuthenticateAdmin(t *testing.T) {
	svc, store, key := newTestService(t)
	a := addAdmin(t, store, "ops@example.com", nil)
	gone := time.Now().Add(-time.Hour)
	left := addAdmin(t, store, "left@example.com", &gone)
	now := time.Now()
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)

	sign := func(k ed25519.PrivateKey, c jwt.Claims, ttl time.Duration) string {
		tok, err := jwt.Sign(k, c, ttl, now)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	login, err := svc.AdminLogin(context.Background(), LoginInput{Email: "ops@example.com", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		token  string
		wantID string // empty: must be refused with 401
	}{
		{"login token", login.Token, a.ID},
		{"developer token", sign(key, jwt.Claims{Subject: a.ID, Type: jwt.TypeDeveloper}, time.Hour), ""},
		{"player token", sign(key, jwt.Claims{Subject: a.ID, GameID: "g1", Type: jwt.TypePlayer}, time.Hour), ""},
		{"expired", sign(key, jwt.Claims{Subject: a.ID, Type: jwt.TypeAdmin}, -time.Minute), ""},
		{"signed by another key", sign(otherKey, jwt.Claims{Subject: a.ID, Type: jwt.TypeAdmin}, time.Hour), ""},
		{"admin who no longer exists", sign(key, jwt.Claims{Subject: "admin-deleted", Type: jwt.TypeAdmin}, time.Hour), ""},
		{"admin disabled after the token was issued", sign(key, jwt.Claims{Subject: left.ID, Type: jwt.TypeAdmin}, time.Hour), ""},
		{"garbage", "not-a-token", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := svc.AuthenticateAdmin(context.Background(), tt.token)
			if tt.wantID == "" {
				wantError(t, err, http.StatusUnauthorized, "unauthorized")
				return
			}
			if err != nil || id != tt.wantID {
				t.Fatalf("got %q, %v; want %q", id, err, tt.wantID)
			}
		})
	}

	t.Run("store failure is not a 401", func(t *testing.T) {
		store.err = errors.New("connection refused")
		defer func() { store.err = nil }()
		_, err := svc.AuthenticateAdmin(context.Background(), login.Token)
		var e *httpx.Error
		if err == nil || errors.As(err, &e) {
			t.Fatalf("want a plain internal error, got %v", err)
		}
	})
}

func TestAdminMe(t *testing.T) {
	svc, store, _ := newTestService(t)
	a := addAdmin(t, store, "ops@example.com", nil)
	gone := time.Now().Add(-time.Hour)
	left := addAdmin(t, store, "left@example.com", &gone)

	got, err := svc.AdminMe(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.Email != "ops@example.com" || got.Name != "Ops One" {
		t.Fatalf("unexpected admin %+v", got)
	}
	_, err = svc.AdminMe(context.Background(), left.ID)
	wantError(t, err, http.StatusUnauthorized, "unauthorized")
	_, err = svc.AdminMe(context.Background(), "admin-deleted")
	wantError(t, err, http.StatusUnauthorized, "unauthorized")
}

package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

func TestAdminLogin(t *testing.T) {
	svc := fakeService{adminLogin: func(in service.LoginInput) (service.Session, error) {
		if in.Password != "correct horse" {
			return service.Session{}, httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "wrong")
		}
		return service.Session{Token: "tok", ExpiresIn: 3600}, nil
	}}

	t.Run("success", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/admin/login",
			`{"email":"ops@example.com","password":"correct horse"}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != `{"token":"tok","expiresIn":3600}`+"\n" {
			t.Fatalf("body %s", rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/admin/login",
			`{"email":"ops@example.com","password":"nope"}`, "")
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_credentials" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("not json", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/admin/login", `x`, "")
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestAdminMe_RequiresAdminToken(t *testing.T) {
	created := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	svc := fakeService{
		authAdmin: func(token string) (string, error) {
			if token != "good" {
				return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
			}
			return "a1", nil
		},
		adminMe: func(id string) (service.Admin, error) {
			return service.Admin{ID: id, Email: "ops@example.com", Name: "Ops", CreatedAt: created}, nil
		},
	}
	for _, tt := range []struct {
		name, header string
		want         int
	}{
		{"valid token", "Bearer good", http.StatusOK},
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic good", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"rejected token", "Bearer bad", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			New("test", svc, discardLog()).Register(mux)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"id":"a1"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
			if tt.want == http.StatusUnauthorized && errorCode(t, rec) != "unauthorized" {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}

// TestAdminRoutesRequireAdmin walks Routes(): every route under /api/v1/admin/ except login must
// refuse a request without a token, so a new admin route cannot be added unguarded by mistake.
func TestAdminRoutesRequireAdmin(t *testing.T) {
	rejecting := fakeService{authAdmin: func(string) (string, error) {
		return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
	}}
	h := New("test", rejecting, discardLog())

	guarded := 0
	for _, r := range h.Routes() {
		if !strings.HasPrefix(r.Path, "/api/v1/admin/") || r.Path == "/api/v1/admin/login" {
			continue
		}
		guarded++
		for _, header := range []string{"", "Bearer some-token"} {
			req := httptest.NewRequest(r.Method, strings.ReplaceAll(r.Path, "{developerID}", "d1"), nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			r.Handle(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with header %q: status %d, want 401", r.Method, r.Path, header, rec.Code)
			}
		}
	}
	if guarded == 0 {
		t.Fatal("no admin routes found to check")
	}
}

// TestAdminAuthFlow runs login -> me through the real handler and service, and checks that
// developer and player tokens, and a token of a disabled admin, are refused on admin routes.
func TestAdminAuthFlow(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	gone := time.Now().Add(-time.Hour)
	store := &memStore{admins: []repository.Admin{
		{ID: "a1", Email: "ops@example.com", PasswordHash: string(hash), Name: "Ops", CreatedAt: time.Now()},
		{ID: "a2", Email: "left@example.com", PasswordHash: string(hash), Name: "Left", CreatedAt: time.Now(), DisabledAt: &gone},
	}}
	svc, err := service.New(store, key, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc.WithAdminTokenTTL(30 * time.Minute)
	login := func(email string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), http.MethodPost, "/api/v1/admin/login",
			`{"email":"`+email+`","password":"correct horse"}`, "")
	}

	rec := login("Ops@Example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var session loginResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &session)
	if session.ExpiresIn != 1800 {
		t.Fatalf("expiresIn %d, want 1800", session.ExpiresIn)
	}

	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/admin/me", "", session.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"ops@example.com"`) {
		t.Fatalf("me with login token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := login("left@example.com"); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_credentials" {
		t.Fatalf("disabled admin login: %d %s", rec.Code, rec.Body.String())
	}

	sign := func(c jwt.Claims) string {
		tok, err := jwt.Sign(key, c, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	for name, token := range map[string]string{
		"developer token":                 sign(jwt.Claims{Subject: "a1", Type: jwt.TypeDeveloper}),
		"player token":                    sign(jwt.Claims{Subject: "a1", GameID: "g1", Type: jwt.TypePlayer}),
		"admin token of a disabled admin": sign(jwt.Claims{Subject: "a2", Type: jwt.TypeAdmin}),
	} {
		rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/admin/me", "", token)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
			t.Errorf("%s on admin route: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// And the other way round: an admin token is no good on a developer route.
	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/developers/me", "", session.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("developer route with admin token: %d %s", rec.Code, rec.Body.String())
	}
}

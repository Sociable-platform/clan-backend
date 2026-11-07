package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
)

// newProtectedMux registers the real routes plus a dummy /api/v1/test endpoint,
// guarded by the real player-token middleware. The dummy endpoint answers with the
// game ID it received, so the test can see what got through.
func newProtectedMux(t *testing.T) (*http.ServeMux, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := New("social", fakeService{}, auth.RequirePlayer(pub, time.Now))
	dummy := Route{http.MethodGet, "/api/v1/test", func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PlayerFrom(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"game_id": p.GameID})
	}}
	mux := http.NewServeMux()
	h.register(mux, append(h.Routes(), dummy))
	return mux, priv
}

func get(mux *http.ServeMux, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestAPIRoutesNeedPlayerToken(t *testing.T) {
	mux, priv := newProtectedMux(t)
	player, _ := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: "a6c1e2f0-1111-4222-8333-944455556666", Type: jwt.TypePlayer}, time.Hour, time.Now())
	developer, _ := jwt.Sign(priv, jwt.Claims{Subject: "d_1", Type: jwt.TypeDeveloper}, time.Hour, time.Now())

	if rec := get(mux, "/api/v1/test", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", rec.Code)
	}
	if rec := get(mux, "/api/v1/test", developer); rec.Code != http.StatusUnauthorized {
		t.Errorf("developer token: status %d, want 401", rec.Code)
	}
	rec := get(mux, "/api/v1/test", player)
	if rec.Code != http.StatusOK {
		t.Fatalf("player token: status %d, want 200", rec.Code)
	}
	if want := `"game_id":"a6c1e2f0-1111-4222-8333-944455556666"`; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("player token: body %s, want it to contain %s", rec.Body.String(), want)
	}
}

func TestHealthAndReadyStayOpen(t *testing.T) {
	mux, _ := newProtectedMux(t)
	for _, path := range []string{"/health", "/ready"} {
		if rec := get(mux, path, ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s asked for a token; it must stay open for Docker and monitoring", path)
		}
	}
}

func TestIsPlayerRoute(t *testing.T) {
	tests := map[string]bool{
		"/api/v1/clans":      true,
		"/api/v1/players/me": true,
		"/health":            false,
		"/ready":             false,
		"/api/v10/clans":     false,
		"/api/v1":            false,
	}
	for path, want := range tests {
		if got := isPlayerRoute(path); got != want {
			t.Errorf("isPlayerRoute(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestNewRequiresTokenMiddleware(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New without the token middleware should refuse (panic)")
		}
	}()
	New("social", fakeService{}, nil)
}

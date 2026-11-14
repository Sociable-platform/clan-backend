package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
)

const (
	servedGame    = "a6c1e2f0-1111-4222-8333-944455556666"
	suspendedGame = "b7d2f301-2222-4333-8444-a55566667777"
)

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// gatedMux registers the real routes behind the real token check and the game gate, with
// suspendedGame switched off. It returns a token for a player of each game.
func gatedMux(t *testing.T) (mux *http.ServeMux, served, suspended string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	isSuspended := func(_ context.Context, gameID string) bool { return gameID == suspendedGame }
	h := New("social", fakeService{}, auth.RequirePlayer(pub, time.Now), testLog()).
		WithGameGate(auth.RequireServedGame(isSuspended))
	mux = http.NewServeMux()
	h.Register(mux)

	token := func(game string) string {
		tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: game, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	return mux, token(servedGame), token(suspendedGame)
}

func send(mux *http.ServeMux, method, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// Every player route is behind the gate: a route added later that skips it fails here.
func TestEveryPlayerRouteRefusesASuspendedGame(t *testing.T) {
	mux, _, suspended := gatedMux(t)
	h := New("social", fakeService{}, passThrough, testLog())

	checked := 0
	for _, route := range h.Routes() {
		if !isPlayerRoute(route.Path) {
			continue
		}
		checked++
		path := pathParam.ReplaceAllString(route.Path, "00000000-0000-4000-8000-000000000001")
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			rec := send(mux, route.Method, path, suspended)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"game_suspended"`) {
				t.Fatalf("suspended game: status %d, body %s; want 403 game_suspended", rec.Code, rec.Body.String())
			}
		})
	}
	if checked < 20 {
		t.Fatalf("checked only %d player routes; the route list changed shape", checked)
	}
}

func TestAServedGameIsNotRefused(t *testing.T) {
	mux, served, _ := gatedMux(t)
	if rec := send(mux, http.MethodGet, "/api/v1/players/me", served); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s; want 200 for a game that is served", rec.Code, rec.Body.String())
	}
}

// A request without a valid token gets 401 whatever game it names, so nobody can use this to find out
// which games are suspended.
func TestTheTokenIsCheckedBeforeTheGame(t *testing.T) {
	mux, _, _ := gatedMux(t)
	if rec := send(mux, http.MethodGet, "/api/v1/friends", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}
}

// Docker and monitoring must keep working while games are suspended.
func TestHealthAndReadyAreNotGated(t *testing.T) {
	mux, _, suspended := gatedMux(t)
	for _, path := range []string{"/health", "/ready"} {
		for _, token := range []string{"", suspended} {
			if rec := send(mux, http.MethodGet, path, token); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s answered %d; it has no game and must stay open", path, rec.Code)
			}
		}
	}
}

// Without a gate (WithGameGate never called) every game is let through, as before.
func TestWithoutAGateNothingIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	New("social", fakeService{}, passThrough, testLog()).Register(mux)
	if rec := send(mux, http.MethodGet, "/api/v1/friends", ""); rec.Code == http.StatusForbidden {
		t.Fatalf("status %d: no gate was set", rec.Code)
	}
}

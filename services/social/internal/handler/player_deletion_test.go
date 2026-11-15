package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
)

// The other fakes must satisfy the Service interface too.
func (f fakeService) DeletePlayer(context.Context, string, string) error { return f.err }

func (s *recordingService) DeletePlayer(context.Context, string, string) error { return s.err }

// deletingService remembers what deleteMe passed on and answers with err.
type deletingService struct {
	fakeService
	gameID, externalID string
	calls              int
	err                error
}

func (s *deletingService) DeletePlayer(_ context.Context, gameID, externalID string) error {
	s.gameID, s.externalID = gameID, externalID
	s.calls++
	return s.err
}

// deleteMux registers the real routes behind the real token check and the player gate. The gate
// says a player is deleted when isDeleted says so. It returns a token for u_42 of servedGame.
func deleteMux(t *testing.T, svc Service, log *slog.Logger, isDeleted func(auth.Player) bool) (*http.ServeMux, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gate := func(_ context.Context, p auth.Player) bool { return isDeleted(p) }
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), log).
		WithPlayerGate(auth.RejectDeletedPlayers(gate)).
		Register(mux)
	tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: servedGame, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return mux, tok
}

func notDeleted(auth.Player) bool { return false }

func TestDeleteMeDeletesThePlayerOfTheToken(t *testing.T) {
	svc := &deletingService{}
	mux, tok := deleteMux(t, svc, testLog(), notDeleted)

	rec := send(mux, http.MethodDelete, "/api/v1/players/me", tok)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d, body %q; want 204 with no body", rec.Code, rec.Body.String())
	}
	if svc.calls != 1 || svc.gameID != servedGame || svc.externalID != "u_42" {
		t.Fatalf("service got %d call(s) with (%q, %q), want the token's game and player", svc.calls, svc.gameID, svc.externalID)
	}
}

func TestDeleteMeErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"expected failure", httpx.NewError(http.StatusConflict, "some_rule", "A rule says no"), http.StatusConflict, `"code":"some_rule"`},
		{"database down", errors.New("pq: connection refused at 10.0.0.5"), http.StatusInternalServerError, `"code":"internal_error"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			mux, tok := deleteMux(t, &deletingService{err: tc.err}, slog.New(slog.NewTextHandler(&logs, nil)), notDeleted)
			rec := send(mux, http.MethodDelete, "/api/v1/players/me", tok)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Fatalf("internal details leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestDeleteMeNeedsAToken(t *testing.T) {
	svc := &deletingService{}
	mux, _ := deleteMux(t, svc, testLog(), notDeleted)
	if rec := send(mux, http.MethodDelete, "/api/v1/players/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if svc.calls != 0 {
		t.Fatal("the service was called without a token")
	}
}

// Every player route is behind the player gate: a route added later that skips it fails here.
func TestEveryPlayerRouteRefusesADeletedPlayer(t *testing.T) {
	var seen auth.Player
	mux, tok := deleteMux(t, &deletingService{}, testLog(), func(p auth.Player) bool { seen = p; return true })
	h := New("social", fakeService{}, passThrough, testLog())

	checked := 0
	for _, route := range h.Routes() {
		if !isPlayerRoute(route.Path) {
			continue
		}
		checked++
		path := pathParam.ReplaceAllString(route.Path, "00000000-0000-4000-8000-000000000001")
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			rec := send(mux, route.Method, path, tok)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"player_deleted"`) {
				t.Fatalf("status %d, body %s; want 401 player_deleted", rec.Code, rec.Body.String())
			}
		})
	}
	if checked < 20 {
		t.Fatalf("checked only %d player routes; the route list changed shape", checked)
	}
	if seen.GameID != servedGame || seen.ExternalID != "u_42" || seen.IssuedAt.IsZero() {
		t.Errorf("gate got %+v, want the token's game, player and issue time", seen)
	}
}

// Without a player gate (WithPlayerGate never called) no token is refused for a deletion.
func TestWithoutAPlayerGateNoTokenIsRefused(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", &deletingService{}, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	tok, _ := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: servedGame, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
	if rec := send(mux, http.MethodDelete, "/api/v1/players/me", tok); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, body %s; want 204", rec.Code, rec.Body.String())
	}
}

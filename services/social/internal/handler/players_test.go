package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// recordingService remembers what the handler asked for, so tests can check the
// arguments came from the token.
type recordingService struct {
	gameID, externalID, name string
	err                      error
}

func (s *recordingService) Ready(context.Context) error { return nil }

func (s *recordingService) EnsurePlayer(_ context.Context, gameID, externalID, name string) (service.Player, error) {
	s.gameID, s.externalID, s.name = gameID, externalID, name
	if s.err != nil {
		return service.Player{}, s.err
	}
	return service.Player{
		ID: "p-1", GameID: gameID, ExternalID: externalID, DisplayName: name,
		CreatedAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
	}, nil
}

// callMe sends GET /api/v1/players/me through the real router and token guard.
func callMe(t *testing.T, svc Service, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, _ := jwt.Sign(priv, jwt.Claims{
		Subject: "u_42", GameID: "a6c1e2f0-1111-4222-8333-944455556666", Name: "Ali", Type: jwt.TypePlayer,
	}, time.Hour, time.Now())

	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), log).Register(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/players/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestGetMeReturnsThePlayerFromTheToken(t *testing.T) {
	svc := &recordingService{}
	rec := callMe(t, svc, testLog())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != "a6c1e2f0-1111-4222-8333-944455556666" || svc.externalID != "u_42" || svc.name != "Ali" {
		t.Fatalf("service got %+v, want the values from the token", svc)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "gameId", "externalId", "displayName", "createdAt"} {
		if body[field] == "" {
			t.Errorf("response is missing %q: %s", field, rec.Body.String())
		}
	}
}

func TestGetMeHidesInternalErrors(t *testing.T) {
	var logs bytes.Buffer
	svc := &recordingService{err: errors.New("pq: connection refused at 10.0.0.5")}
	rec := callMe(t, svc, slog.New(slog.NewTextHandler(&logs, nil)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("internal details leaked to the client: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "10.0.0.5") {
		t.Fatal("the real error should be in the server log")
	}
}

func TestGetMeNeedsAToken(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", &recordingService{}, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/players/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

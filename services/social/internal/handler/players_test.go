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

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
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

// The other fakes must satisfy the Service interface too.
func (f fakeService) SearchPlayers(context.Context, string, string, string, service.SearchPlayersInput) ([]service.PlayerSearchResult, error) {
	return []service.PlayerSearchResult{}, f.err
}

func (f fakeService) ListFriendshipsForSync(context.Context, service.SyncFriendshipsInput) (service.FriendshipPage, error) {
	return service.FriendshipPage{}, f.err
}

func (s *recordingService) ListFriendshipsForSync(context.Context, service.SyncFriendshipsInput) (service.FriendshipPage, error) {
	return service.FriendshipPage{}, nil
}

func (s *recordingService) SearchPlayers(context.Context, string, string, string, service.SearchPlayersInput) ([]service.PlayerSearchResult, error) {
	return []service.PlayerSearchResult{}, s.err
}

// searchingService remembers what searchPlayers passed on and answers with what the test gave it.
type searchingService struct {
	fakeService
	results                  []service.PlayerSearchResult
	err                      error
	gameID, externalID, name string
	in                       service.SearchPlayersInput
}

func (s *searchingService) ListFriendshipsForSync(context.Context, service.SyncFriendshipsInput) (service.FriendshipPage, error) {
	return service.FriendshipPage{}, nil
}

func (s *searchingService) SearchPlayers(_ context.Context, gameID, externalID, name string, in service.SearchPlayersInput) ([]service.PlayerSearchResult, error) {
	s.gameID, s.externalID, s.name, s.in = gameID, externalID, name, in
	return s.results, s.err
}

func TestSearchPlayersHandler(t *testing.T) {
	svc := &searchingService{results: []service.PlayerSearchResult{
		{ID: "p-1", DisplayName: "Aysel", Relation: "request_received", RequestID: "req-1"},
		{ID: "p-2", DisplayName: "Ayten", Relation: "none"},
	}}
	// a game in the URL must be ignored: only the token decides the game
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/players?q=ay&limit=5&gameId=99999999-9999-4999-8999-999999999999", "", false)
	checkBody(t, rec, http.StatusOK, `{"players":[`+
		`{"id":"p-1","displayName":"Aysel","relation":"request_received","requestId":"req-1"},`+
		`{"id":"p-2","displayName":"Ayten","relation":"none"}]}`)
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.name != "Ali" {
		t.Errorf("service got game=%q player=%q name=%q, want the token's", svc.gameID, svc.externalID, svc.name)
	}
	if svc.in != (service.SearchPlayersInput{Query: "ay", Limit: "5"}) {
		t.Errorf("service got %+v", svc.in)
	}
}

func TestSearchPlayersHandlerEmptyIsEmptyArray(t *testing.T) {
	rec := callFriends(t, &searchingService{results: []service.PlayerSearchResult{}}, http.MethodGet, "/api/v1/players?q=zz", "", false)
	checkBody(t, rec, http.StatusOK, `{"players":[]}`)
}

func TestSearchPlayersHandlerPassesErrorsOn(t *testing.T) {
	svc := &searchingService{err: httpx.NewError(http.StatusBadRequest, "invalid_query", "Search text (q) is required")}
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/players", "", false)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_query"`) {
		t.Fatalf("status %d, body %s; want 400 invalid_query", rec.Code, rec.Body.String())
	}
}

func TestSearchPlayersNeedsAToken(t *testing.T) {
	svc := &searchingService{}
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/players?q=ay", "", true)
	if rec.Code != http.StatusUnauthorized || svc.gameID != "" {
		t.Fatalf("status %d, service reached = %v; want 401 and not reached", rec.Code, svc.gameID != "")
	}
}

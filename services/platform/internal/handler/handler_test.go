package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// fakeService answers every call from its fields, so tests set only what they need.
type fakeService struct {
	err      error // returned by Ready
	register func(service.RegisterInput) (service.Developer, error)
	login    func(service.LoginInput) (service.Session, error)
	auth     func(token string) (string, error)
	me       func(id string) (service.Developer, error)

	registerGame func(developerID string, in service.RegisterGameInput) (service.Game, error)
	listGames    func(developerID string) ([]service.Game, error)

	createAPIKey func(developerID, gameID string) (service.NewAPIKey, error)
	listAPIKeys  func(developerID, gameID string) ([]service.APIKey, error)
	revokeAPIKey func(developerID, keyID string) error
}

func (f fakeService) Ready(context.Context) error { return f.err }

func (f fakeService) Register(_ context.Context, in service.RegisterInput) (service.Developer, error) {
	return f.register(in)
}

func (f fakeService) Login(_ context.Context, in service.LoginInput) (service.Session, error) {
	return f.login(in)
}

func (f fakeService) AuthenticateDeveloper(token string) (string, error) { return f.auth(token) }

func (f fakeService) Me(_ context.Context, id string) (service.Developer, error) { return f.me(id) }

func serve(t *testing.T, svc Service, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealth(t *testing.T) {
	rec := serve(t, fakeService{}, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestReady(t *testing.T) {
	if rec := serve(t, fakeService{}, http.MethodGet, "/ready"); rec.Code != http.StatusOK {
		t.Fatalf("ready with database: status %d", rec.Code)
	}
	rec := serve(t, fakeService{err: errors.New("no db")}, http.MethodGet, "/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready without database: status %d", rec.Code)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "not_ready" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	rec := serve(t, fakeService{}, http.MethodGet, "/nope")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") == "" {
		t.Fatalf("status %d, content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func (f fakeService) RegisterGame(_ context.Context, developerID string, in service.RegisterGameInput) (service.Game, error) {
	return f.registerGame(developerID, in)
}

func (f fakeService) ListGames(_ context.Context, developerID string) ([]service.Game, error) {
	return f.listGames(developerID)
}

func (f fakeService) CreateAPIKey(_ context.Context, developerID, gameID string) (service.NewAPIKey, error) {
	return f.createAPIKey(developerID, gameID)
}

func (f fakeService) ListAPIKeys(_ context.Context, developerID, gameID string) ([]service.APIKey, error) {
	return f.listAPIKeys(developerID, gameID)
}

func (f fakeService) RevokeAPIKey(_ context.Context, developerID, keyID string) error {
	return f.revokeAPIKey(developerID, keyID)
}

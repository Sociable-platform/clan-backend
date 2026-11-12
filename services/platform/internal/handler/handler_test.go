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

	adminLogin func(service.LoginInput) (service.Session, error)
	authAdmin  func(token string) (string, error)
	adminMe    func(id string) (service.Admin, error)

	listDevelopers func(service.ListDevelopersInput) (service.DeveloperPage, error)
	adminDeveloper func(developerID string) (service.AdminDeveloper, error)
	auditLog       func(service.AuditLogInput) (service.AuditPage, error)

	setDeveloperStatus func(actor, developerID string, in service.SetStatusInput) (service.AdminDeveloper, error)
	setGameStatus      func(actor, gameID string, in service.SetStatusInput) (service.AdminGame, error)

	registerGame func(developerID string, in service.RegisterGameInput) (service.Game, error)
	listGames    func(developerID string) ([]service.Game, error)

	createAPIKey func(developerID, gameID string) (service.NewAPIKey, error)
	listAPIKeys  func(developerID, gameID string) ([]service.APIKey, error)
	revokeAPIKey func(developerID, keyID string) error

	widgetDraft     func(developerID, gameID string) (service.WidgetDraft, error)
	saveWidgetDraft func(developerID, gameID string, config json.RawMessage) (service.WidgetDraft, error)
	publishWidget   func(developerID, gameID string) (service.WidgetVersion, error)
	widgetVersions  func(developerID, gameID string) ([]service.WidgetVersion, error)
	widgetVersion   func(developerID, gameID string, version int) (service.WidgetVersion, error)
	rollbackWidget  func(developerID, gameID string, version int) (service.WidgetVersion, error)

	publicWidgetConfig func(gameID string) (service.WidgetVersion, error)

	setAllowedOrigins  func(developerID, gameID string, origins []string) (service.Game, error)
	internalGameConfig func(gameID string) (service.InternalGameConfig, error)

	gameForAPIKey    func(key string) (string, error)
	ensureGameServed func(gameID string) error // nil: every game is served
	issuePlayerToken func(gameID string, in service.PlayerTokenInput) (service.PlayerToken, error)
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

func (f fakeService) AdminLogin(_ context.Context, in service.LoginInput) (service.Session, error) {
	return f.adminLogin(in)
}

func (f fakeService) AuthenticateAdmin(_ context.Context, token string) (string, error) {
	return f.authAdmin(token)
}

func (f fakeService) AdminMe(_ context.Context, id string) (service.Admin, error) {
	return f.adminMe(id)
}

func (f fakeService) ListDevelopers(_ context.Context, in service.ListDevelopersInput) (service.DeveloperPage, error) {
	return f.listDevelopers(in)
}

func (f fakeService) AdminDeveloper(_ context.Context, id string) (service.AdminDeveloper, error) {
	return f.adminDeveloper(id)
}

func (f fakeService) AuditLog(_ context.Context, in service.AuditLogInput) (service.AuditPage, error) {
	return f.auditLog(in)
}

func (f fakeService) SetDeveloperStatus(_ context.Context, actor, id string, in service.SetStatusInput) (service.AdminDeveloper, error) {
	return f.setDeveloperStatus(actor, id, in)
}

func (f fakeService) SetGameStatus(_ context.Context, actor, id string, in service.SetStatusInput) (service.AdminGame, error) {
	return f.setGameStatus(actor, id, in)
}

func (f fakeService) EnsureGameServed(_ context.Context, gameID string) error {
	if f.ensureGameServed == nil {
		return nil
	}
	return f.ensureGameServed(gameID)
}

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

func (f fakeService) GameForAPIKey(_ context.Context, key string) (string, error) {
	return f.gameForAPIKey(key)
}

func (f fakeService) IssuePlayerToken(gameID string, in service.PlayerTokenInput) (service.PlayerToken, error) {
	return f.issuePlayerToken(gameID, in)
}

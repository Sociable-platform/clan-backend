// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
	Register(ctx context.Context, in service.RegisterInput) (service.Developer, error)
	Login(ctx context.Context, in service.LoginInput) (service.Session, error)
	AuthenticateDeveloper(token string) (developerID string, err error)
	AdminLogin(ctx context.Context, in service.LoginInput) (service.Session, error)
	AuthenticateAdmin(ctx context.Context, token string) (adminID string, err error)
	AdminMe(ctx context.Context, adminID string) (service.Admin, error)
	ListDevelopers(ctx context.Context, in service.ListDevelopersInput) (service.DeveloperPage, error)
	AdminDeveloper(ctx context.Context, developerID string) (service.AdminDeveloper, error)
	AuditLog(ctx context.Context, in service.AuditLogInput) (service.AuditPage, error)
	SetDeveloperStatus(ctx context.Context, actor, developerID string, in service.SetStatusInput) (service.AdminDeveloper, error)
	SetGameStatus(ctx context.Context, actor, gameID string, in service.SetStatusInput) (service.AdminGame, error)
	Me(ctx context.Context, developerID string) (service.Developer, error)
	RegisterGame(ctx context.Context, developerID string, in service.RegisterGameInput) (service.Game, error)
	ListGames(ctx context.Context, developerID string) ([]service.Game, error)
	CreateAPIKey(ctx context.Context, developerID, gameID string) (service.NewAPIKey, error)
	ListAPIKeys(ctx context.Context, developerID, gameID string) ([]service.APIKey, error)
	RevokeAPIKey(ctx context.Context, developerID, keyID string) error
	GameForAPIKey(ctx context.Context, key string) (gameID string, err error)
	EnsureGameServed(ctx context.Context, gameID string) error
	IssuePlayerToken(gameID string, in service.PlayerTokenInput) (service.PlayerToken, error)
	WidgetDraft(ctx context.Context, developerID, gameID string) (service.WidgetDraft, error)
	SaveWidgetDraft(ctx context.Context, developerID, gameID string, config json.RawMessage) (service.WidgetDraft, error)
	PublishWidget(ctx context.Context, developerID, gameID string) (service.WidgetVersion, error)
	WidgetVersions(ctx context.Context, developerID, gameID string) ([]service.WidgetVersion, error)
	WidgetVersion(ctx context.Context, developerID, gameID string, version int) (service.WidgetVersion, error)
	RollbackWidget(ctx context.Context, developerID, gameID string, version int) (service.WidgetVersion, error)
	PublicWidgetConfig(ctx context.Context, gameID string) (service.WidgetVersion, error)
	SetAllowedOrigins(ctx context.Context, developerID, gameID string, origins []string) (service.Game, error)
	InternalGameConfig(ctx context.Context, gameID string) (service.InternalGameConfig, error)
}

// Handler holds the routes of the service.
type Handler struct {
	name            string
	svc             Service
	log             *slog.Logger
	requireInternal httpx.Middleware
}

// New returns a Handler. name is the service name shown in /health.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, log *slog.Logger) *Handler {
	return &Handler{name: name, svc: svc, log: log}
}

// WithInternal sets the guard of the /internal routes, which other services call with a shared
// secret instead of a token (see httpx.RequireInternal). It returns h, so it can follow New.
// Without it those routes answer the same JSON 404 as an unknown route.
func (h *Handler) WithInternal(requireInternal httpx.Middleware) *Handler {
	h.requireInternal = requireInternal
	return h
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here. Endpoints that need
// a logged-in developer are wrapped with h.requireDeveloper; the one endpoint a game's own
// server calls (/players/auth) is wrapped with h.requireAPIKey. For example:
//
//	{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.registerGame)},
//
// Endpoints under /api/v1/admin/ are for the distributor's admin panel. Every one except
// /admin/login is wrapped with h.requireAdmin, which accepts only an admin token.
//
// Endpoints under /internal/ are for other services: they need the shared secret in
// X-Internal-Token and are wrapped with h.internal. A browser must never reach them.
//
// Endpoints under /api/v1/public/ need no login at all and are open to every website
// (see the CORS setup in cmd/main.go); they must never return anything secret.
//
// Endpoints under /api/v1/games/{gameID} must first call svc.OwnedGame, so one developer
// can never reach another developer's game.
//
// The OpenAPI file (openapi/platform.yaml) must document exactly these routes;
// TestOpenAPIMatchesRoutes fails when the two drift apart.
func (h *Handler) Routes() []Route {
	return []Route{
		{http.MethodGet, "/health", h.health},
		{http.MethodGet, "/ready", h.ready},
		{http.MethodPost, "/api/v1/developers/register", h.registerDeveloper},
		{http.MethodPost, "/api/v1/developers/login", h.loginDeveloper},
		{http.MethodGet, "/api/v1/developers/me", h.requireDeveloper(h.me)},
		{http.MethodPost, "/api/v1/admin/login", h.adminLogin},
		{http.MethodGet, "/api/v1/admin/me", h.requireAdmin(h.adminMe)},
		{http.MethodGet, "/api/v1/admin/developers", h.requireAdmin(h.listAdminDevelopers)},
		{http.MethodGet, "/api/v1/admin/developers/{developerID}", h.requireAdmin(h.getAdminDeveloper)},
		{http.MethodPut, "/api/v1/admin/developers/{developerID}/status", h.requireAdmin(h.setDeveloperStatus)},
		{http.MethodPut, "/api/v1/admin/games/{gameID}/status", h.requireAdmin(h.setGameStatus)},
		{http.MethodGet, "/api/v1/admin/audit-log", h.requireAdmin(h.listAuditLog)},
		{http.MethodGet, "/api/v1/games", h.requireDeveloper(h.listGames)},
		{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.registerGame)},
		{http.MethodPost, "/api/v1/games/{gameID}/api-keys", h.requireDeveloper(h.createAPIKey)},
		{http.MethodGet, "/api/v1/games/{gameID}/api-keys", h.requireDeveloper(h.listAPIKeys)},
		{http.MethodDelete, "/api/v1/api-keys/{keyID}", h.requireDeveloper(h.revokeAPIKey)},
		{http.MethodPut, "/api/v1/games/{gameID}/allowed-origins", h.requireDeveloper(h.setAllowedOrigins)},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/draft", h.requireDeveloper(h.getWidgetDraft)},
		{http.MethodPut, "/api/v1/games/{gameID}/widget/draft", h.requireDeveloper(h.saveWidgetDraft)},
		{http.MethodPost, "/api/v1/games/{gameID}/widget/publish", h.requireDeveloper(h.publishWidget)},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/versions", h.requireDeveloper(h.listWidgetVersions)},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/versions/{version}", h.requireDeveloper(h.getWidgetVersion)},
		{http.MethodPost, "/api/v1/games/{gameID}/widget/versions/{version}/rollback", h.requireDeveloper(h.rollbackWidget)},
		{http.MethodGet, "/api/v1/public/games/{gameID}/widget-config", h.getPublicWidgetConfig},
		{http.MethodPost, "/api/v1/players/auth", h.requireAPIKey(h.authPlayer)},
		{http.MethodGet, "/internal/v1/games/{gameID}/config", h.internal(h.getInternalGameConfig)},
	}
}

// Register adds all routes to mux, plus a JSON 404 for everything else.
func (h *Handler) Register(mux *http.ServeMux) {
	for _, r := range h.Routes() {
		mux.HandleFunc(r.Method+" "+r.Path, r.Handle)
	}
	mux.HandleFunc("/", h.notFound)
}

// health answers as long as the process is running.
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": h.name})
}

// ready answers 200 only when the service can reach its database.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Ready(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "not_ready", "Database is not reachable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": h.name})
}

// internal wraps next so only a caller with the internal token reaches it. While no token is
// configured the guard answers 404, so the route looks like it does not exist.
func (h *Handler) internal(next http.HandlerFunc) http.HandlerFunc {
	guard := h.requireInternal
	if guard == nil {
		guard = httpx.RequireInternal("")
	}
	return guard(next).ServeHTTP
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "Route not found")
}

// fail writes err as the standard error response. Expected failures (*httpx.Error)
// are sent as they are; anything else is logged here and becomes a generic 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		h.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	httpx.WriteErr(w, err)
}

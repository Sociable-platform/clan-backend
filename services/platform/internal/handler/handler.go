// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
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
	Me(ctx context.Context, developerID string) (service.Developer, error)
	RegisterGame(ctx context.Context, developerID string, in service.RegisterGameInput) (service.Game, error)
	ListGames(ctx context.Context, developerID string) ([]service.Game, error)
}

// Handler holds the routes of the service.
type Handler struct {
	name string
	svc  Service
	log  *slog.Logger
}

// New returns a Handler. name is the service name shown in /health.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, log *slog.Logger) *Handler {
	return &Handler{name: name, svc: svc, log: log}
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here. Endpoints that need
// a logged-in developer are wrapped with h.requireDeveloper, for example:
//
//	{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.registerGame)},
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
		{http.MethodGet, "/api/v1/games", h.requireDeveloper(h.listGames)},
		{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.registerGame)},
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

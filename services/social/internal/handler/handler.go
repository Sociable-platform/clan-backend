// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
	EnsurePlayer(ctx context.Context, gameID, externalID, displayName string) (service.Player, error)
	ListClans(ctx context.Context, gameID, query string) ([]service.Clan, error)
}

// Handler holds the routes of the service.
type Handler struct {
	name          string
	svc           Service
	requirePlayer httpx.Middleware
	log           *slog.Logger
}

// New returns a Handler. name is the service name shown in /health.
// requirePlayer guards every /api/v1 route (see package auth); it must not be nil.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, requirePlayer httpx.Middleware, log *slog.Logger) *Handler {
	if requirePlayer == nil {
		panic("handler: requirePlayer middleware is required")
	}
	return &Handler{name: name, svc: svc, requirePlayer: requirePlayer, log: log}
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here, for example:
//
//	{http.MethodPost, "/api/v1/clans", h.createClan},
//
// The OpenAPI file (openapi/social.yaml) must document exactly these routes;
// TestOpenAPIMatchesRoutes fails when the two drift apart.
func (h *Handler) Routes() []Route {
	return []Route{
		{http.MethodGet, "/health", h.health},
		{http.MethodGet, "/ready", h.ready},
		{http.MethodGet, "/api/v1/players/me", h.getMe},
		{http.MethodGet, "/api/v1/clans", h.listClans},
	}
}

// Register adds all routes to mux, plus a JSON 404 for everything else.
// Every route under /api/v1/ runs only for a valid player token (requirePlayer);
// /health and /ready stay open so Docker and monitoring can call them.
func (h *Handler) Register(mux *http.ServeMux) {
	h.register(mux, h.Routes())
}

// register does the work of Register for the given routes. Tests use it to add
// extra routes (for example a dummy /api/v1 endpoint) to check the token guard.
func (h *Handler) register(mux *http.ServeMux, routes []Route) {
	for _, r := range routes {
		var handle http.Handler = r.Handle
		if isPlayerRoute(r.Path) {
			handle = h.requirePlayer(handle)
		}
		mux.Handle(r.Method+" "+r.Path, handle)
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

// errNoPlayerInContext means a player route ran without the token guard (a programming error).
var errNoPlayerInContext = errors.New("handler: no player in request context; route is missing the token guard")

// fail writes err as an error response. Expected errors (*httpx.Error) keep their status
// and code; anything else is logged with details and answered with a generic 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		h.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	httpx.WriteErr(w, err)
}

// isPlayerRoute reports whether path needs a player token.
func isPlayerRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/")
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "Route not found")
}

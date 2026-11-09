// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
}

// Handler holds the routes of the service.
type Handler struct {
	name    string
	svc     Service
	wsOpts  WSOptions
	log     *slog.Logger
	mu      sync.Mutex     // guards closing
	closing bool           // set by Wait: no new WebSocket connection is accepted any more
	conns   sync.WaitGroup // open WebSocket connections
}

// New returns a Handler. name is the service name shown in /health.
// ws configures the WebSocket endpoint; its Verifier is required.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, ws WSOptions, log *slog.Logger) *Handler {
	if ws.Verifier == nil {
		panic("handler: WSOptions.Verifier is required")
	}
	return &Handler{name: name, svc: svc, wsOpts: ws.withDefaults(), log: log}
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here, for example:
//
//	{http.MethodGet, "/api/v1/conversations", h.listConversations},
//
// and document them in openapi/realtime.yaml (the WebSocket in openapi/realtime-ws.md) in the same commit.
func (h *Handler) Routes() []Route {
	return []Route{
		{http.MethodGet, "/health", h.health},
		{http.MethodGet, "/ready", h.ready},
		{http.MethodGet, "/api/v1/ws", h.ws},
	}
}

// Register adds all routes to mux, plus a JSON 404 for everything else.
func (h *Handler) Register(mux *http.ServeMux) {
	for _, r := range h.Routes() {
		mux.Handle(r.Method+" "+r.Path, r.Handle)
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

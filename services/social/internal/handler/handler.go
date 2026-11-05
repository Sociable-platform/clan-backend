// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
}

// Handler holds the routes of the service.
type Handler struct {
	name string
	svc  Service
}

// New returns a Handler. name is the service name shown in /health.
func New(name string, svc Service) *Handler {
	return &Handler{name: name, svc: svc}
}

// Register adds all routes to mux. New feature routes go here, for example:
//
//	mux.HandleFunc("POST /api/v1/clans", h.createClan)
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /ready", h.ready)
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

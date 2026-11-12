package handler

import (
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// setStatusRequest is the body of the two status endpoints.
type setStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// setDeveloperStatus handles PUT /api/v1/admin/developers/{developerID}/status. The response is
// the same developer detail as the GET, so the panel can redraw from it.
func (h *Handler) setDeveloperStatus(w http.ResponseWriter, r *http.Request) {
	var req setStatusRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.SetDeveloperStatus(r.Context(), adminID(r.Context()), r.PathValue("developerID"),
		service.SetStatusInput{Status: req.Status, Reason: req.Reason, Note: req.Note})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAdminDeveloperResponse(d))
}

// setGameStatus handles PUT /api/v1/admin/games/{gameID}/status.
func (h *Handler) setGameStatus(w http.ResponseWriter, r *http.Request) {
	var req setStatusRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	g, err := h.svc.SetGameStatus(r.Context(), adminID(r.Context()), r.PathValue("gameID"),
		service.SetStatusInput{Status: req.Status, Reason: req.Reason, Note: req.Note})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAdminGameResponse(g))
}

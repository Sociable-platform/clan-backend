package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
)

// joinRequestResponse is a pending request to join a closed clan.
type joinRequestResponse struct {
	ID          string    `json:"id"`
	PlayerID    string    `json:"playerId"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

type joinRequestListResponse struct {
	Requests []joinRequestResponse `json:"requests"`
}

// requestJoin handles POST /api/v1/clans/{clanID}/requests: the caller asks to join a closed clan. 201 with
// the request. No body. A leader of the clan accepts or declines it.
func (h *Handler) requestJoin(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	q, err := h.svc.RequestJoin(r.Context(), caller.GameID, caller.ExternalID, caller.Name, r.PathValue("clanID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, joinRequestResponse{
		ID: q.ID, PlayerID: q.PlayerID, DisplayName: q.DisplayName, CreatedAt: q.CreatedAt.UTC(),
	})
}

// cancelJoinRequest handles DELETE /api/v1/clans/{clanID}/requests/me: the caller takes their request
// back. 204 with no body.
func (h *Handler) cancelJoinRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.CancelJoinRequest(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listJoinRequests handles GET /api/v1/clans/{clanID}/requests: the pending requests of the clan, oldest
// first, for a leader of that clan (its owner, or a role that may remove members).
func (h *Handler) listJoinRequests(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	rows, err := h.svc.ListJoinRequests(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := joinRequestListResponse{Requests: make([]joinRequestResponse, 0, len(rows))}
	for _, q := range rows {
		out.Requests = append(out.Requests, joinRequestResponse{
			ID: q.ID, PlayerID: q.PlayerID, DisplayName: q.DisplayName, CreatedAt: q.CreatedAt.UTC(),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// acceptJoinRequest handles POST /api/v1/clans/{clanID}/requests/{requestID}/accept: a leader lets the
// player in. 204 with no body.
func (h *Handler) acceptJoinRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.AcceptJoinRequest(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), r.PathValue("requestID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// declineJoinRequest handles POST /api/v1/clans/{clanID}/requests/{requestID}/decline: a leader turns the
// player down; they may ask again. 204 with no body.
func (h *Handler) declineJoinRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.DeclineJoinRequest(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), r.PathValue("requestID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package handler

import (
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type setRoleRequest struct {
	Role string `json:"role"`
}

type transferRequest struct {
	DeveloperID string `json:"developerId"`
}

func toGameMemberResponse(m service.GameMember) gameMemberResponse {
	return gameMemberResponse{DeveloperID: m.DeveloperID, Name: m.Name, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt.UTC()}
}

// removeGameMember handles DELETE /api/v1/games/{gameID}/members/{developerID}: an owner takes a developer off the game.
func (h *Handler) removeGameMember(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveGameMember(r.Context(), developerID(r.Context()), r.PathValue("gameID"), r.PathValue("developerID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// leaveGame handles POST /api/v1/games/{gameID}/leave: the logged-in developer leaves the game.
func (h *Handler) leaveGame(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.LeaveGame(r.Context(), developerID(r.Context()), r.PathValue("gameID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setGameMemberRole handles PUT /api/v1/games/{gameID}/members/{developerID}/role: an owner makes somebody an owner or a member.
func (h *Handler) setGameMemberRole(w http.ResponseWriter, r *http.Request) {
	var req setRoleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.svc.SetMemberRole(r.Context(), developerID(r.Context()), r.PathValue("gameID"), r.PathValue("developerID"), req.Role)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toGameMemberResponse(m))
}

// transferGameOwnership handles POST /api/v1/games/{gameID}/transfer-ownership: an owner hands the game to a member and
// becomes a member. The answer is the game as the caller now sees it.
func (h *Handler) transferGameOwnership(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	g, err := h.svc.TransferOwnership(r.Context(), developerID(r.Context()), r.PathValue("gameID"), req.DeveloperID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toGameResponse(g))
}

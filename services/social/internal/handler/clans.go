package handler

import (
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type clanResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
	MaxMembers  int    `json:"maxMembers"`
}

type clanListResponse struct {
	Clans []clanResponse `json:"clans"`
}

func toClanResponse(c service.Clan) clanResponse {
	return clanResponse{ID: c.ID, Name: c.Name, MemberCount: c.MemberCount, MaxMembers: c.MaxMembers}
}

// listClans handles GET /api/v1/clans?q=: the clans of the caller's game, optionally searched by name.
// The game comes only from the player token; q comes from the URL.
func (h *Handler) listClans(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	clans, err := h.svc.ListClans(r.Context(), caller.GameID, r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := clanListResponse{Clans: make([]clanResponse, 0, len(clans))}
	for _, c := range clans {
		out.Clans = append(out.Clans, toClanResponse(c))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

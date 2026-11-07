package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type playerResponse struct {
	ID          string    `json:"id"`
	GameID      string    `json:"gameId"`
	ExternalID  string    `json:"externalId"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toPlayerResponse(p service.Player) playerResponse {
	return playerResponse{
		ID:          p.ID,
		GameID:      p.GameID,
		ExternalID:  p.ExternalID,
		DisplayName: p.DisplayName,
		CreatedAt:   p.CreatedAt.UTC(),
	}
}

// getMe handles GET /api/v1/players/me: the calling player, created on first use.
// Who is calling comes only from the verified player token (auth.PlayerFrom).
func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		// Only possible if the route was registered without the token guard: a bug, not a client error.
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	p, err := h.svc.EnsurePlayer(r.Context(), caller.GameID, caller.ExternalID, caller.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPlayerResponse(p))
}

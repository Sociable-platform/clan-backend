package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
)

type blockedPlayerResponse struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Since       time.Time `json:"since"` // when the caller blocked this player
}

type blockListResponse struct {
	Blocks []blockedPlayerResponse `json:"blocks"`
}

// blockPlayer handles POST /api/v1/players/{playerID}/block: the caller blocks that player.
// It also ends their friendship and pending friend request. No body; 204 with no body, also
// when the player was blocked already.
func (h *Handler) blockPlayer(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.BlockPlayer(r.Context(), caller.GameID, caller.ExternalID, caller.Name, r.PathValue("playerID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unblockPlayer handles DELETE /api/v1/players/{playerID}/block: the caller removes their block
// of that player. 204 with no body, also when the player was not blocked.
func (h *Handler) unblockPlayer(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.UnblockPlayer(r.Context(), caller.GameID, caller.ExternalID, caller.Name, r.PathValue("playerID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listBlocks handles GET /api/v1/players/me/blocks: the players the caller blocked, newest first.
func (h *Handler) listBlocks(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	blocks, err := h.svc.ListBlocks(r.Context(), caller.GameID, caller.ExternalID, caller.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := blockListResponse{Blocks: make([]blockedPlayerResponse, 0, len(blocks))}
	for _, b := range blocks {
		out.Blocks = append(out.Blocks, blockedPlayerResponse{ID: b.ID, DisplayName: b.DisplayName, Since: b.Since.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

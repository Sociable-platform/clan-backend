package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type syncPlayerResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type syncFriendshipResponse struct {
	GameID  string             `json:"gameId"`
	PlayerA syncPlayerResponse `json:"playerA"`
	PlayerB syncPlayerResponse `json:"playerB"`
	Since   time.Time          `json:"since"`
}

type syncFriendshipsResponse struct {
	Friendships []syncFriendshipResponse `json:"friendships"`
	NextCursor  string                   `json:"nextCursor,omitempty"`
}

// listFriendshipsForSync handles GET /internal/v1/friendships: one page of every game's
// friendships for the realtime service. It runs behind the internal token, not a player token.
func (h *Handler) listFriendshipsForSync(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	page, err := h.svc.ListFriendshipsForSync(r.Context(), service.SyncFriendshipsInput{
		After: params.Get("after"),
		Limit: params.Get("limit"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := syncFriendshipsResponse{
		Friendships: make([]syncFriendshipResponse, 0, len(page.Friendships)),
		NextCursor:  page.NextCursor,
	}
	for _, f := range page.Friendships {
		out.Friendships = append(out.Friendships, syncFriendshipResponse{
			GameID:  f.GameID,
			PlayerA: syncPlayerResponse(f.PlayerA),
			PlayerB: syncPlayerResponse(f.PlayerB),
			Since:   f.Since,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

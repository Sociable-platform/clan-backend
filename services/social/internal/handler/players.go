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

// deleteMe handles DELETE /api/v1/players/me: the game deleted the calling player's account, so
// their social profile goes too (see service.DeletePlayer). It answers 204 with no body. The
// game's server gets a token for the player (POST /api/v1/players/auth on platform) and calls this
// with it. Calling it again with a token issued before the deletion never gets here: the player
// gate answers 401 player_deleted, which tells the game the player is already gone.
func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.DeletePlayer(r.Context(), caller.GameID, caller.ExternalID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type playerSearchResultResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Relation    string `json:"relation"`
	RequestID   string `json:"requestId,omitempty"` // only for request_sent and request_received
}

type playerSearchResponse struct {
	Players []playerSearchResultResponse `json:"players"`
}

// searchPlayers handles GET /api/v1/players?q=&limit=: players of the caller's game whose name
// contains q, each with how they relate to the caller. The game and the caller come only from
// the token; q and limit come from the URL and are checked by the service.
func (h *Handler) searchPlayers(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	params := r.URL.Query()
	players, err := h.svc.SearchPlayers(r.Context(), caller.GameID, caller.ExternalID, caller.Name, service.SearchPlayersInput{
		Query: params.Get("q"),
		Limit: params.Get("limit"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := playerSearchResponse{Players: make([]playerSearchResultResponse, 0, len(players))}
	for _, p := range players {
		out.Players = append(out.Players, playerSearchResultResponse(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

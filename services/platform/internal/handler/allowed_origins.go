package handler

import (
	"encoding/json"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

type setAllowedOriginsRequest struct {
	AllowedOrigins []string `json:"allowedOrigins"`
}

// setAllowedOrigins handles PUT /api/v1/games/{gameID}/allowed-origins: it replaces the list of
// websites that may open a WebSocket for the game. The game must belong to the logged-in developer.
func (h *Handler) setAllowedOrigins(w http.ResponseWriter, r *http.Request) {
	var req setAllowedOriginsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.AllowedOrigins == nil { // a missing field is a mistake; an empty list clears the setting
		httpx.WriteError(w, http.StatusBadRequest, "invalid_origins", "allowedOrigins must be a list, use [] for none")
		return
	}
	g, err := h.svc.SetAllowedOrigins(r.Context(), developerID(r.Context()), r.PathValue("gameID"), req.AllowedOrigins)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toGameResponse(g))
}

// internalGameConfigResponse is what realtime gets: the game's active config and the origins that
// may connect. Config is null while the game has no published version (version 0).
type internalGameConfigResponse struct {
	GameID         string          `json:"gameId"`
	Version        int             `json:"version"`
	Config         json.RawMessage `json:"config"`
	AllowedOrigins []string        `json:"allowedOrigins"`
}

// getInternalGameConfig handles GET /internal/v1/games/{gameID}/config, for other services only.
func (h *Handler) getInternalGameConfig(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.InternalGameConfig(r.Context(), r.PathValue("gameID"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		h.fail(w, r, err)
		return
	}
	cfg := c.Config
	if cfg == nil {
		cfg = json.RawMessage("null")
	}
	w.Header().Set("Cache-Control", "no-store") // the caller keeps its own cache and learns of changes from events
	httpx.WriteJSON(w, http.StatusOK, internalGameConfigResponse{
		GameID: c.GameID, Version: c.Version, Config: cfg, AllowedOrigins: c.AllowedOrigins,
	})
}

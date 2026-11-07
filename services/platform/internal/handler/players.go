package handler

import (
	"context"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type playerAuthRequest struct {
	ExternalPlayerID string `json:"externalPlayerId"`
	DisplayName      string `json:"displayName"`
}

type playerAuthResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"` // seconds
}

type gameCtxKey struct{}

// gameID returns the game ID that requireAPIKey stored in ctx.
func gameID(ctx context.Context) string {
	id, _ := ctx.Value(gameCtxKey{}).(string)
	return id
}

// requireAPIKey wraps next so it only runs for a valid, active API key in the
// X-API-Key header. The key is checked before the body is read, so a caller with a bad
// key learns nothing about the request format. A missing, wrong or revoked key gets 401.
func (h *Handler) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := h.svc.GameForAPIKey(r.Context(), r.Header.Get("X-API-Key"))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), gameCtxKey{}, id)))
	}
}

// authPlayer handles POST /api/v1/players/auth. The game's own server calls it with its
// secret API key to get a short-lived token for a player it has already logged in.
// The game in the token is the key's game; the body cannot name another one.
func (h *Handler) authPlayer(w http.ResponseWriter, r *http.Request) {
	var req playerAuthRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	t, err := h.svc.IssuePlayerToken(gameID(r.Context()), service.PlayerTokenInput{
		ExternalPlayerID: req.ExternalPlayerID,
		DisplayName:      req.DisplayName,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the response holds a credential
	httpx.WriteJSON(w, http.StatusOK, playerAuthResponse{Token: t.Token, ExpiresIn: t.ExpiresIn})
}

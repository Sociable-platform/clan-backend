package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// apiKeyResponse is a key as lists show it: no secret.
type apiKeyResponse struct {
	ID        string     `json:"id"`
	GameID    string     `json:"gameId"`
	KeyPrefix string     `json:"keyPrefix"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt"` // null while the key is active
}

// newAPIKeyResponse is the one response that carries the full key.
type newAPIKeyResponse struct {
	apiKeyResponse
	Key string `json:"key"`
}

type apiKeyListResponse struct {
	APIKeys []apiKeyResponse `json:"apiKeys"`
}

func toAPIKeyResponse(k service.APIKey) apiKeyResponse {
	r := apiKeyResponse{ID: k.ID, GameID: k.GameID, KeyPrefix: k.KeyPrefix, CreatedAt: k.CreatedAt.UTC()}
	if k.RevokedAt != nil {
		t := k.RevokedAt.UTC()
		r.RevokedAt = &t
	}
	return r
}

// createAPIKey handles POST /api/v1/games/{gameID}/api-keys. This response is the only
// time the full key is ever returned.
func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	k, err := h.svc.CreateAPIKey(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the response holds a secret
	httpx.WriteJSON(w, http.StatusCreated, newAPIKeyResponse{apiKeyResponse: toAPIKeyResponse(k.APIKey), Key: k.Key})
}

// listAPIKeys handles GET /api/v1/games/{gameID}/api-keys: prefixes only, never the full key.
func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.ListAPIKeys(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := apiKeyListResponse{APIKeys: make([]apiKeyResponse, 0, len(keys))}
	for _, k := range keys {
		out.APIKeys = append(out.APIKeys, toAPIKeyResponse(k))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// revokeAPIKey handles DELETE /api/v1/api-keys/{keyID}. The key stays in the list,
// marked revoked, and stops working at once.
func (h *Handler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeAPIKey(r.Context(), developerID(r.Context()), r.PathValue("keyID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

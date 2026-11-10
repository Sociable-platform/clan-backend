package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

// caller is the player who made a REST request, as proven by their token and social.
type caller struct {
	GameID   string // from the verified token, never from the request
	PlayerID string // social's own ID for the player
}

// authenticate checks the bearer token and finds the player's social ID. On failure it has
// already written the error response and reports false.
//
// The token is verified with platform's public key (no call to platform). Social's player ID is
// asked from social with the player's own token, the same way the WebSocket does at connect time.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (caller, bool) {
	fail := func(err error) (caller, bool) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="realtime"`)
		httpx.WriteErr(w, err)
		return caller{}, false
	}

	scheme, token, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return fail(httpx.NewError(http.StatusUnauthorized, "missing_token", "Send a player token: Authorization: Bearer <token>"))
	}
	token = strings.TrimSpace(token)
	player, err := h.wsOpts.Verifier.Verify(token)
	if err != nil {
		return fail(err)
	}

	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()
	sp, err := h.wsOpts.Players.Player(ctx, token, player.GameID)
	if err != nil {
		if errors.Is(err, social.ErrUnavailable) {
			h.log.Warn("cannot resolve the player in social", "game_id", player.GameID, "error", err)
		} else {
			h.log.Error("resolve the player in social", "game_id", player.GameID, "error", err)
		}
		httpx.WriteError(w, http.StatusServiceUnavailable, "social_unavailable", "Cannot reach the social service; try again shortly")
		return caller{}, false
	}
	return caller{GameID: player.GameID, PlayerID: sp.ID}, true
}

func (c caller) reader() service.Reader {
	return service.Reader{GameID: c.GameID, PlayerID: c.PlayerID}
}

// listConversations answers GET /api/v1/conversations.
func (h *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	who, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	list, err := h.svc.Conversations(r.Context(), who.reader())
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// listMessages answers GET /api/v1/conversations/{conversationId}/messages?before=&limit=.
func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	who, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.Messages(r.Context(), who.reader(), r.PathValue("conversationId"), q.Get("before"), q.Get("limit"))
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

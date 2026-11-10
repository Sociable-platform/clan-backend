package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type friendPlayerResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type friendRequestResponse struct {
	ID        string               `json:"id"`
	Player    friendPlayerResponse `json:"player"` // the other player
	CreatedAt time.Time            `json:"createdAt"`
}

type friendResponse struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Since       time.Time `json:"since"`
}

type friendRequestsResponse struct {
	Incoming []friendRequestResponse `json:"incoming"`
	Outgoing []friendRequestResponse `json:"outgoing"`
}

type friendListResponse struct {
	Friends []friendResponse `json:"friends"`
}

func toFriendRequestResponse(r service.FriendRequest) friendRequestResponse {
	return friendRequestResponse{
		ID:        r.ID,
		Player:    friendPlayerResponse{ID: r.Player.ID, DisplayName: r.Player.DisplayName},
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func toFriendResponse(f service.Friend) friendResponse {
	return friendResponse{ID: f.ID, DisplayName: f.DisplayName, Since: f.Since.UTC()}
}

type sendFriendRequestRequest struct {
	PlayerID string `json:"playerId"`
}

// sendFriendRequest handles POST /api/v1/friends/requests with { "playerId": "..." }: the
// caller asks another player of their game to be friends. 201 with the new pending request;
// 200 with the new friend when that player had already asked the caller (it is accepted).
func (h *Handler) sendFriendRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	var req sendFriendRequestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.svc.SendFriendRequest(r.Context(), caller.GameID, caller.ExternalID, caller.Name, req.PlayerID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if res.Friend != nil {
		httpx.WriteJSON(w, http.StatusOK, toFriendResponse(*res.Friend))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toFriendRequestResponse(*res.Request))
}

// listFriendRequests handles GET /api/v1/friends/requests: the caller's pending requests,
// split into incoming (they can accept) and outgoing (waiting for the other player).
func (h *Handler) listFriendRequests(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	reqs, err := h.svc.ListFriendRequests(r.Context(), caller.GameID, caller.ExternalID, caller.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := friendRequestsResponse{
		Incoming: make([]friendRequestResponse, 0, len(reqs.Incoming)),
		Outgoing: make([]friendRequestResponse, 0, len(reqs.Outgoing)),
	}
	for _, q := range reqs.Incoming {
		out.Incoming = append(out.Incoming, toFriendRequestResponse(q))
	}
	for _, q := range reqs.Outgoing {
		out.Outgoing = append(out.Outgoing, toFriendRequestResponse(q))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// acceptFriendRequest handles POST /api/v1/friends/requests/{requestID}/accept: the caller
// accepts a request sent to them and gets the new friend back. No body is needed.
func (h *Handler) acceptFriendRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	f, err := h.svc.AcceptFriendRequest(r.Context(), caller.GameID, caller.ExternalID, caller.Name, r.PathValue("requestID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toFriendResponse(f))
}

// listFriends handles GET /api/v1/friends: the caller's friends, sorted by name.
func (h *Handler) listFriends(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	friends, err := h.svc.ListFriends(r.Context(), caller.GameID, caller.ExternalID, caller.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := friendListResponse{Friends: make([]friendResponse, 0, len(friends))}
	for _, f := range friends {
		out.Friends = append(out.Friends, toFriendResponse(f))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

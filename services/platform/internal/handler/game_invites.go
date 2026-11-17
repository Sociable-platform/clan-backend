package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type inviteRequest struct {
	Email string `json:"email"`
}

// gameInviteResponse is an invite as the game's owner sees it.
type gameInviteResponse struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	InvitedByName string    `json:"invitedByName"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type gameInviteListResponse struct {
	Invites []gameInviteResponse `json:"invites"`
}

// myInviteResponse is an invite as the invited developer sees it: which game and who asked.
type myInviteResponse struct {
	ID            string    `json:"id"`
	GameID        string    `json:"gameId"`
	GameName      string    `json:"gameName"`
	InvitedByName string    `json:"invitedByName"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type myInviteListResponse struct {
	Invites []myInviteResponse `json:"invites"`
}

func toGameInviteResponse(i service.GameInvite) gameInviteResponse {
	return gameInviteResponse{ID: i.ID, Email: i.Email, InvitedByName: i.InvitedByName, CreatedAt: i.CreatedAt.UTC(), ExpiresAt: i.ExpiresAt.UTC()}
}

// inviteToGame handles POST /api/v1/games/{gameID}/invites: an owner invites an email address.
func (h *Handler) inviteToGame(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	inv, err := h.svc.InviteToGame(r.Context(), developerID(r.Context()), r.PathValue("gameID"), req.Email)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toGameInviteResponse(inv))
}

// listGameInvites handles GET /api/v1/games/{gameID}/invites: the pending invites of a game, for an owner.
func (h *Handler) listGameInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.svc.GameInvites(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := gameInviteListResponse{Invites: make([]gameInviteResponse, 0, len(invites))}
	for _, i := range invites {
		out.Invites = append(out.Invites, toGameInviteResponse(i))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// cancelGameInvite handles DELETE /api/v1/games/{gameID}/invites/{inviteID}: an owner takes an invite back.
func (h *Handler) cancelGameInvite(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.CancelGameInvite(r.Context(), developerID(r.Context()), r.PathValue("gameID"), r.PathValue("inviteID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listMyInvites handles GET /api/v1/invites: the invites sent to the logged-in developer's email address.
func (h *Handler) listMyInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.svc.MyInvites(r.Context(), developerID(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := myInviteListResponse{Invites: make([]myInviteResponse, 0, len(invites))}
	for _, i := range invites {
		out.Invites = append(out.Invites, myInviteResponse{ID: i.ID, GameID: i.GameID, GameName: i.GameName,
			InvitedByName: i.InvitedByName, CreatedAt: i.CreatedAt.UTC(), ExpiresAt: i.ExpiresAt.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// acceptInvite handles POST /api/v1/invites/{inviteID}/accept: the invited developer joins the game as a member.
func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	g, err := h.svc.AcceptInvite(r.Context(), developerID(r.Context()), r.PathValue("inviteID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toGameResponse(g))
}

// declineInvite handles POST /api/v1/invites/{inviteID}/decline: the invited developer says no.
func (h *Handler) declineInvite(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeclineInvite(r.Context(), developerID(r.Context()), r.PathValue("inviteID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

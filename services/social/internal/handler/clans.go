package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type clanResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
	MaxMembers  int    `json:"maxMembers"`
}

type clanListResponse struct {
	Clans []clanResponse `json:"clans"`
}

func toClanResponse(c service.Clan) clanResponse {
	return clanResponse{ID: c.ID, Name: c.Name, MemberCount: c.MemberCount, MaxMembers: c.MaxMembers}
}

// listClans handles GET /api/v1/clans?q=: the clans of the caller's game, optionally searched by name.
// The game comes only from the player token; q comes from the URL.
func (h *Handler) listClans(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	clans, err := h.svc.ListClans(r.Context(), caller.GameID, r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := clanListResponse{Clans: make([]clanResponse, 0, len(clans))}
	for _, c := range clans {
		out.Clans = append(out.Clans, toClanResponse(c))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type createClanRequest struct {
	Name string `json:"name"`
}

// createClan handles POST /api/v1/clans with { "name": "..." }: the caller creates a clan
// in their game and becomes its owner. The game and player come only from the token;
// the body carries just the name.
func (h *Handler) createClan(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	var req createClanRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.svc.CreateClan(r.Context(), caller.GameID, caller.ExternalID, caller.Name, req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toClanResponse(c))
}

// joinClan handles POST /api/v1/clans/{clanID}/join: the caller joins that clan of their
// game as a member and gets the clan back with its new member count. No body is needed.
func (h *Handler) joinClan(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	c, err := h.svc.JoinClan(r.Context(), caller.GameID, caller.ExternalID, caller.Name, r.PathValue("clanID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toClanResponse(c))
}

// leaveClan handles POST /api/v1/clans/{clanID}/leave: the caller leaves that clan of their
// game. No body is needed and nothing is returned (204). If the caller was the owner, the
// member who joined first becomes the new owner; if they were the last member, the clan is
// deleted.
func (h *Handler) leaveClan(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	if err := h.svc.LeaveClan(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type myClanResponse struct {
	Clan clanResponse `json:"clan"`
	Role string       `json:"role"`
}

// getMyClan handles GET /api/v1/players/me/clan: the caller's clan and role, or 404 not_in_clan.
func (h *Handler) getMyClan(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	pc, err := h.svc.MyClan(r.Context(), caller.GameID, caller.ExternalID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, myClanResponse{Clan: toClanResponse(pc.Clan), Role: pc.Role})
}

type memberResponse struct {
	PlayerID    string    `json:"playerId"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joinedAt"`
}

type memberListResponse struct {
	Members []memberResponse `json:"members"`
}

// listMembers handles GET /api/v1/clans/{clanID}/members: the members of a clan of the
// caller's game, owner first. Another game's clan answers 404 like a missing one.
func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	members, err := h.svc.ListMembers(r.Context(), caller.GameID, r.PathValue("clanID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := memberListResponse{Members: make([]memberResponse, 0, len(members))}
	for _, m := range members {
		out.Members = append(out.Members, memberResponse{
			PlayerID: m.PlayerID, DisplayName: m.DisplayName, Role: m.Role, JoinedAt: m.JoinedAt.UTC(),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

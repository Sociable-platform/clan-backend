package handler

import (
	"context"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type clanResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Left out of the JSON when the clan has neither, so a clan made before these fields
	// existed looks exactly as it did. A client reads a missing field as an empty one.
	Description string `json:"description,omitempty"`
	Badge       string `json:"badge,omitempty"`
	MemberCount int    `json:"memberCount"`
	MaxMembers  int    `json:"maxMembers"`
	// JoinPolicy is "open" or "closed". A closed clan takes players only after a leader accepts their request.
	JoinPolicy string `json:"joinPolicy"`
	// Requested is only in a clan list, and only when the caller has a pending request to this clan.
	Requested bool `json:"requested,omitempty"`
}

type clanListResponse struct {
	Clans []clanResponse `json:"clans"`
}

func toClanResponse(c service.Clan) clanResponse {
	return clanResponse{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Badge:       c.Badge,
		MemberCount: c.MemberCount,
		MaxMembers:  c.MaxMembers,
		JoinPolicy:  c.JoinPolicy,
		Requested:   c.Requested,
	}
}

// listClans handles GET /api/v1/clans?q=&limit=&sort=&hasSpace=: the clans of the caller's game,
// optionally searched by name, limited, sorted and filtered. The game comes only from the
// player token; the rest comes from the URL and is checked by the service.
func (h *Handler) listClans(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	params := r.URL.Query()
	clans, err := h.svc.ListClans(r.Context(), caller.GameID, service.ListClansInput{
		Query:    params.Get("q"),
		Limit:    params.Get("limit"),
		Sort:     params.Get("sort"),
		HasSpace: params.Get("hasSpace"),
		// Whose pending requests to mark: the caller's, from the token.
		ViewerExternalID: caller.ExternalID,
	})
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
	// Both are optional. Unknown fields are refused (httpx.DecodeJSON), so a form that
	// sends a description to a build without these fields gets 400 invalid_json.
	Description string `json:"description"`
	Badge       string `json:"badge"`
	// JoinPolicy is "open" (the default) or "closed": players of a closed clan are accepted by its leaders.
	JoinPolicy string `json:"joinPolicy"`
	// Tag is read and ignored: clans have no tags any more (TEAM-277). It stays here only so
	// that an older SDK that still sends it is not refused as an unknown field.
	Tag any `json:"tag"`
}

// createClan handles POST /api/v1/clans with { "name", "description", "badge" }:
// the caller creates a clan in their game and becomes its owner. The game and player come
// only from the token; everything but the name may be left out.
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
	c, err := h.svc.CreateClan(r.Context(), caller.GameID, caller.ExternalID, caller.Name, service.NewClan{
		Name:        req.Name,
		Description: req.Description,
		Badge:       req.Badge,
		JoinPolicy:  req.JoinPolicy,
	})
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

// renameClanRequest is the body of PATCH /api/v1/clans/{clanID}: a new name, or a new join policy.
type renameClanRequest struct {
	Name string `json:"name"`
	// JoinPolicy is "open" or "closed". A pointer tells "not sent" from an empty text.
	JoinPolicy *string `json:"joinPolicy"`
}

// renameClan handles PATCH /api/v1/clans/{clanID}: the clan's owner gives it a new name, or opens or
// closes it ({ "joinPolicy": "closed" }); one of the two per call. 200 with the clan as it is now.
func (h *Handler) renameClan(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	var req renameClanRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	var c service.Clan
	var err error
	switch {
	case req.JoinPolicy != nil && req.Name != "":
		err = httpx.NewError(http.StatusBadRequest, "invalid_clan_update", "Send either name or joinPolicy, not both")
	case req.JoinPolicy != nil:
		c, err = h.svc.SetJoinPolicy(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), *req.JoinPolicy)
	default:
		c, err = h.svc.RenameClan(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), req.Name)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toClanResponse(c))
}

// kickMember handles DELETE /api/v1/clans/{clanID}/members/{playerID}: the clan's owner
// removes a member. 204 with no body.
func (h *Handler) kickMember(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	err := h.svc.KickMember(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), r.PathValue("playerID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// memberRoleResponse is a member's role after a promote or demote.
type memberRoleResponse struct {
	PlayerID    string `json:"playerId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

// promoteMember handles POST /api/v1/clans/{clanID}/members/{playerID}/promote: one role up,
// as the game's clan roles allow. 200 with the member's new role. No body.
func (h *Handler) promoteMember(w http.ResponseWriter, r *http.Request) {
	h.changeRole(w, r, h.svc.PromoteMember)
}

// demoteMember handles POST /api/v1/clans/{clanID}/members/{playerID}/demote: one role down.
func (h *Handler) demoteMember(w http.ResponseWriter, r *http.Request) {
	h.changeRole(w, r, h.svc.DemoteMember)
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request,
	change func(ctx context.Context, gameID, externalID, clanID, playerID string) (service.MemberRole, error)) {
	caller, ok := auth.PlayerFrom(r.Context())
	if !ok {
		h.fail(w, r, errNoPlayerInContext)
		return
	}
	m, err := change(r.Context(), caller.GameID, caller.ExternalID, r.PathValue("clanID"), r.PathValue("playerID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberRoleResponse{PlayerID: m.PlayerID, DisplayName: m.DisplayName, Role: m.Role})
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

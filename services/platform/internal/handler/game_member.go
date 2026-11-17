package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// gameKey is the context key of the game requireGameMember found.
type gameKey struct{}

// gameFrom returns the game requireGameMember put on the request, with the developer's role on it
// (service.Game.Role). Only handlers behind requireGameMember have one.
func gameFrom(ctx context.Context) (service.Game, bool) {
	g, ok := ctx.Value(gameKey{}).(service.Game)
	return g, ok
}

// requireGameMember wraps next, which must already be behind requireDeveloper, so it only runs for a
// developer who belongs to the game in the URL ({gameID}). A game that does not exist, a malformed ID and
// a game the developer is not in all answer the same 404 game_not_found, so the answer never tells whether
// somebody else's game exists, and next does not run. The role is looked up here on every request: it is
// per game and changes while a login token is still valid, so it is never read from the token.
//
// Every developer route with a {gameID} is wrapped with it, and TestEveryGameRouteChecksMembership walks
// Routes() to fail for one that is not. The service methods check again (Service.MemberGame), so a
// handler that is added later and forgets this still cannot read or change another developer's game.
func (h *Handler) requireGameMember(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, err := h.svc.MemberGame(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), gameKey{}, g)))
	}
}

// requireGameOwner wraps next, which must already be behind requireGameMember, so it only runs for an owner of the
// game. A member gets 403 owner_required (they know the game, they just may not do this to it); a developer who is
// not in the game never gets here, requireGameMember already answered 404. Used for what is only for owners: API
// keys, allowed origins and, later, the team. The service checks again (Service.OwnerGame).
func (h *Handler) requireGameOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, ok := gameFrom(r.Context())
		if !ok {
			// A route that forgot requireGameMember: refuse rather than guess.
			h.fail(w, r, errors.New("requireGameOwner without requireGameMember"))
			return
		}
		if g.Role != "owner" {
			httpx.WriteError(w, http.StatusForbidden, "owner_required", "Only the game's owner can do this")
			return
		}
		next(w, r)
	}
}

type gameMemberResponse struct {
	DeveloperID string    `json:"developerId"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joinedAt"`
}

type gameMemberListResponse struct {
	Members []gameMemberResponse `json:"members"`
}

// listGameMembers handles GET /api/v1/games/{gameID}/members: everybody on the game, owners first. Any member of
// the game may look; a developer who is not in it gets 404.
func (h *Handler) listGameMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.svc.GameMembers(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := gameMemberListResponse{Members: make([]gameMemberResponse, 0, len(members))}
	for _, m := range members {
		out.Members = append(out.Members, gameMemberResponse{DeveloperID: m.DeveloperID, Name: m.Name, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

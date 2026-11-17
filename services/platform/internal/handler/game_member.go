package handler

import (
	"context"
	"net/http"

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

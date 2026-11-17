// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
	EnsurePlayer(ctx context.Context, gameID, externalID, displayName string) (service.Player, error)
	DeletePlayer(ctx context.Context, gameID, externalID string) error
	ListClans(ctx context.Context, gameID string, in service.ListClansInput) ([]service.Clan, error)
	CreateClan(ctx context.Context, gameID, externalID, displayName string, in service.NewClan) (service.Clan, error)
	JoinClan(ctx context.Context, gameID, externalID, displayName, clanID string) (service.Clan, error)
	LeaveClan(ctx context.Context, gameID, externalID, clanID string) error
	KickMember(ctx context.Context, gameID, externalID, clanID, playerID string) error
	PromoteMember(ctx context.Context, gameID, externalID, clanID, playerID string) (service.MemberRole, error)
	DemoteMember(ctx context.Context, gameID, externalID, clanID, playerID string) (service.MemberRole, error)
	RenameClan(ctx context.Context, gameID, externalID, clanID, name string) (service.Clan, error)
	MyClan(ctx context.Context, gameID, externalID string) (service.PlayerClan, error)
	SetJoinPolicy(ctx context.Context, gameID, externalID, clanID, policy string) (service.Clan, error)
	RequestJoin(ctx context.Context, gameID, externalID, displayName, clanID string) (service.JoinRequest, error)
	CancelJoinRequest(ctx context.Context, gameID, externalID, clanID string) error
	ListJoinRequests(ctx context.Context, gameID, externalID, clanID string) ([]service.JoinRequest, error)
	AcceptJoinRequest(ctx context.Context, gameID, externalID, clanID, requestID string) error
	DeclineJoinRequest(ctx context.Context, gameID, externalID, clanID, requestID string) error
	ListMembers(ctx context.Context, gameID, clanID string) ([]service.Member, error)
	SendFriendRequest(ctx context.Context, gameID, externalID, displayName, playerID string) (service.SendFriendRequestResult, error)
	AcceptFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) (service.Friend, error)
	ListFriendRequests(ctx context.Context, gameID, externalID, displayName string) (service.FriendRequests, error)
	ListFriends(ctx context.Context, gameID, externalID, displayName string) ([]service.Friend, error)
	DeclineFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) error
	CancelFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) error
	RemoveFriend(ctx context.Context, gameID, externalID, displayName, playerID string) error
	SearchPlayers(ctx context.Context, gameID, externalID, displayName string, in service.SearchPlayersInput) ([]service.PlayerSearchResult, error)
	BlockPlayer(ctx context.Context, gameID, externalID, displayName, playerID string) error
	UnblockPlayer(ctx context.Context, gameID, externalID, displayName, playerID string) error
	ListBlocks(ctx context.Context, gameID, externalID, displayName string) ([]service.BlockedPlayer, error)
	ListFriendshipsForSync(ctx context.Context, in service.SyncFriendshipsInput) (service.FriendshipPage, error)
}

// Handler holds the routes of the service.
type Handler struct {
	name          string
	svc           Service
	requirePlayer httpx.Middleware
	// requireServed runs after requirePlayer and refuses the requests of a suspended game (see
	// WithGameGate); nil lets every game through.
	requireServed httpx.Middleware
	// requireLive runs after requireServed and refuses the tokens of deleted players (see
	// WithPlayerGate); nil lets every token through.
	requireLive httpx.Middleware
	// requireInternal guards the /internal routes (see WithInternal); nil switches them off.
	requireInternal httpx.Middleware
	log             *slog.Logger
}

// New returns a Handler. name is the service name shown in /health.
// requirePlayer guards every /api/v1 route (see package auth); it must not be nil.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, requirePlayer httpx.Middleware, log *slog.Logger) *Handler {
	if requirePlayer == nil {
		panic("handler: requirePlayer middleware is required")
	}
	return &Handler{name: name, svc: svc, requirePlayer: requirePlayer, log: log}
}

// WithInternal sets the guard of the /internal routes, which other services call with a shared
// secret instead of a player token (see package httpx, RequireInternal). It returns h, so it can
// follow New. Without it those routes answer the same JSON 404 as an unknown route.
func (h *Handler) WithInternal(requireInternal httpx.Middleware) *Handler {
	h.requireInternal = requireInternal
	return h
}

// WithGameGate sets the guard that runs after the player token check on every /api/v1 route and
// refuses the requests of a suspended game (see auth.RequireServedGame). It returns h, so it can
// follow New. /health, /ready and the /internal routes are never gated: they have no game.
func (h *Handler) WithGameGate(requireServed httpx.Middleware) *Handler {
	h.requireServed = requireServed
	return h
}

// WithPlayerGate sets the guard that runs after the game gate on every /api/v1 route and refuses
// a token issued before its player was deleted (see auth.RejectDeletedPlayers). It returns h, so
// it can follow New.
func (h *Handler) WithPlayerGate(requireLive httpx.Middleware) *Handler {
	h.requireLive = requireLive
	return h
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here, for example:
//
//	{http.MethodPost, "/api/v1/clans", h.createClan},
//
// The OpenAPI file (openapi/social.yaml) must document exactly these routes;
// TestOpenAPIMatchesRoutes fails when the two drift apart.
func (h *Handler) Routes() []Route {
	return []Route{
		{http.MethodGet, "/health", h.health},
		{http.MethodGet, "/ready", h.ready},
		{http.MethodGet, "/api/v1/players/me", h.getMe},
		{http.MethodDelete, "/api/v1/players/me", h.deleteMe},
		{http.MethodGet, "/api/v1/players/me/clan", h.getMyClan},
		{http.MethodGet, "/api/v1/players", h.searchPlayers},
		{http.MethodGet, "/api/v1/players/me/blocks", h.listBlocks},
		{http.MethodPost, "/api/v1/players/{playerID}/block", h.blockPlayer},
		{http.MethodDelete, "/api/v1/players/{playerID}/block", h.unblockPlayer},
		{http.MethodGet, "/api/v1/clans", h.listClans},
		{http.MethodPost, "/api/v1/clans", h.createClan},
		{http.MethodPatch, "/api/v1/clans/{clanID}", h.renameClan},
		{http.MethodPost, "/api/v1/clans/{clanID}/join", h.joinClan},
		{http.MethodPost, "/api/v1/clans/{clanID}/leave", h.leaveClan},
		{http.MethodGet, "/api/v1/clans/{clanID}/requests", h.listJoinRequests},
		{http.MethodPost, "/api/v1/clans/{clanID}/requests", h.requestJoin},
		{http.MethodDelete, "/api/v1/clans/{clanID}/requests/me", h.cancelJoinRequest},
		{http.MethodPost, "/api/v1/clans/{clanID}/requests/{requestID}/accept", h.acceptJoinRequest},
		{http.MethodPost, "/api/v1/clans/{clanID}/requests/{requestID}/decline", h.declineJoinRequest},
		{http.MethodGet, "/api/v1/clans/{clanID}/members", h.listMembers},
		{http.MethodDelete, "/api/v1/clans/{clanID}/members/{playerID}", h.kickMember},
		{http.MethodPost, "/api/v1/clans/{clanID}/members/{playerID}/promote", h.promoteMember},
		{http.MethodPost, "/api/v1/clans/{clanID}/members/{playerID}/demote", h.demoteMember},
		{http.MethodGet, "/api/v1/friends", h.listFriends},
		{http.MethodGet, "/api/v1/friends/requests", h.listFriendRequests},
		{http.MethodPost, "/api/v1/friends/requests", h.sendFriendRequest},
		{http.MethodPost, "/api/v1/friends/requests/{requestID}/accept", h.acceptFriendRequest},
		{http.MethodPost, "/api/v1/friends/requests/{requestID}/decline", h.declineFriendRequest},
		{http.MethodDelete, "/api/v1/friends/requests/{requestID}", h.cancelFriendRequest},
		{http.MethodDelete, "/api/v1/friends/{playerID}", h.removeFriend},
		{http.MethodGet, "/internal/v1/friendships", h.listFriendshipsForSync},
	}
}

// Register adds all routes to mux, plus a JSON 404 for everything else.
// Every route under /api/v1/ runs only for a valid player token (requirePlayer);
// /health and /ready stay open so Docker and monitoring can call them.
func (h *Handler) Register(mux *http.ServeMux) {
	h.register(mux, h.Routes())
}

// register does the work of Register for the given routes. Tests use it to add
// extra routes (for example a dummy /api/v1 endpoint) to check the token guard.
func (h *Handler) register(mux *http.ServeMux, routes []Route) {
	for _, r := range routes {
		var handle http.Handler = r.Handle
		switch {
		case isPlayerRoute(r.Path):
			// Wrapped inside out, so they run: requirePlayer, requireServed, requireLive.
			if h.requireLive != nil {
				handle = h.requireLive(handle)
			}
			if h.requireServed != nil {
				handle = h.requireServed(handle) // inside requirePlayer: the token is proven first
			}
			handle = h.requirePlayer(handle)
		case isInternalRoute(r.Path):
			handle = h.internalGuard()(handle)
		}
		mux.Handle(r.Method+" "+r.Path, handle)
	}
	mux.HandleFunc("/", h.notFound)
}

// health answers as long as the process is running.
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": h.name})
}

// ready answers 200 only when the service can reach its database.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Ready(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "not_ready", "Database is not reachable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": h.name})
}

// errNoPlayerInContext means a player route ran without the token guard (a programming error).
var errNoPlayerInContext = errors.New("handler: no player in request context; route is missing the token guard")

// fail writes err as an error response. Expected errors (*httpx.Error) keep their status
// and code; anything else is logged with details and answered with a generic 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		h.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	httpx.WriteErr(w, err)
}

// isPlayerRoute reports whether path needs a player token.
func isPlayerRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/")
}

// isInternalRoute reports whether path is for other services and needs the internal token.
func isInternalRoute(path string) bool {
	return strings.HasPrefix(path, "/internal/")
}

// internalGuard is the guard of the /internal routes: the one that was set, or one that answers
// 404 while none was set.
func (h *Handler) internalGuard() httpx.Middleware {
	if h.requireInternal == nil {
		return httpx.RequireInternal("")
	}
	return h.requireInternal
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "Route not found")
}

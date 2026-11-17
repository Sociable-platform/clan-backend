// Package handler turns HTTP requests into service calls, and service results into JSON.
// Routes use the standard net/http ServeMux patterns ("GET /path/{id}").
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/ratelimit"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// Service is what the handlers need from the service layer.
type Service interface {
	Ready(ctx context.Context) error
	Register(ctx context.Context, in service.RegisterInput) (service.Developer, error)
	EmailVerificationOn() bool
	RequestRegistration(ctx context.Context, in service.RegisterInput) error
	VerifyRegistration(ctx context.Context, in service.VerifyRegistrationInput) (service.Developer, error)
	ResendRegistrationCode(ctx context.Context, email string) error
	Login(ctx context.Context, in service.LoginInput) (service.Session, error)
	AuthenticateDeveloper(ctx context.Context, token string) (developerID string, err error)
	RequestPasswordReset(ctx context.Context, email string) error
	ResetPassword(ctx context.Context, in service.ResetPasswordInput) error
	RefreshSession(ctx context.Context, refreshToken string) (service.Session, error)
	Logout(ctx context.Context, refreshToken string) error
	AdminLogin(ctx context.Context, in service.LoginInput) (service.Session, error)
	AuthenticateAdmin(ctx context.Context, token string) (adminID string, err error)
	AdminMe(ctx context.Context, adminID string) (service.Admin, error)
	ListDevelopers(ctx context.Context, in service.ListDevelopersInput) (service.DeveloperPage, error)
	AdminDeveloper(ctx context.Context, developerID string) (service.AdminDeveloper, error)
	AuditLog(ctx context.Context, in service.AuditLogInput) (service.AuditPage, error)
	SetDeveloperStatus(ctx context.Context, actor, developerID string, in service.SetStatusInput) (service.AdminDeveloper, error)
	SetGameStatus(ctx context.Context, actor, gameID string, in service.SetStatusInput) (service.AdminGame, error)
	Me(ctx context.Context, developerID string) (service.Developer, error)
	EnsureDeveloperActive(ctx context.Context, developerID string) error
	RegisterGame(ctx context.Context, developerID string, in service.RegisterGameInput) (service.Game, error)
	ListGames(ctx context.Context, developerID string) ([]service.Game, error)
	MemberGame(ctx context.Context, developerID, gameID string) (service.Game, error)
	GameMembers(ctx context.Context, developerID, gameID string) ([]service.GameMember, error)
	CreateAPIKey(ctx context.Context, developerID, gameID string) (service.NewAPIKey, error)
	ListAPIKeys(ctx context.Context, developerID, gameID string) ([]service.APIKey, error)
	RevokeAPIKey(ctx context.Context, developerID, keyID string) error
	GameForAPIKey(ctx context.Context, key string) (gameID string, err error)
	EnsureGameServed(ctx context.Context, gameID string) error
	IssuePlayerToken(gameID string, in service.PlayerTokenInput) (service.PlayerToken, error)
	WidgetDraft(ctx context.Context, developerID, gameID string) (service.WidgetDraft, error)
	SaveWidgetDraft(ctx context.Context, developerID, gameID string, config json.RawMessage) (service.WidgetDraft, error)
	PublishWidget(ctx context.Context, developerID, gameID string) (service.WidgetVersion, error)
	WidgetVersions(ctx context.Context, developerID, gameID string) ([]service.WidgetVersion, error)
	WidgetVersion(ctx context.Context, developerID, gameID string, version int) (service.WidgetVersion, error)
	RollbackWidget(ctx context.Context, developerID, gameID string, version int) (service.WidgetVersion, error)
	PublicWidgetConfig(ctx context.Context, gameID string) (service.WidgetVersion, error)
	SetAllowedOrigins(ctx context.Context, developerID, gameID string, origins []string) (service.Game, error)
	InternalGameConfig(ctx context.Context, gameID string) (service.InternalGameConfig, error)
}

// Handler holds the routes of the service.
type Handler struct {
	name            string
	svc             Service
	log             *slog.Logger
	requireInternal httpx.Middleware
	// Limits of the password reset endpoints (password_reset.go).
	resetByIP      *ratelimit.Limiter
	resetByEmail   *ratelimit.Limiter
	resetConfirmIP *ratelimit.Limiter
	// sessionByIP limits token/refresh and logout; insecureCookies drops Secure from the refresh
	// cookie for http://localhost (sessions.go).
	sessionByIP     *ratelimit.Limiter
	insecureCookies bool
	// Limits of registering with email verification (registration.go).
	registerByIP    *ratelimit.Limiter
	registerByEmail *ratelimit.Limiter
	verifyByIP      *ratelimit.Limiter
}

// New returns a Handler. name is the service name shown in /health.
// log receives the details of unexpected errors, which are never sent to the client.
func New(name string, svc Service, log *slog.Logger) *Handler {
	return &Handler{
		name: name, svc: svc, log: log,
		resetByIP:       ratelimit.New(resetPerIP, resetLimitWindow),
		resetByEmail:    ratelimit.New(resetPerEmail, resetLimitWindow),
		resetConfirmIP:  ratelimit.New(resetConfirmPerIP, resetLimitWindow),
		sessionByIP:     ratelimit.New(sessionPerIP, resetLimitWindow),
		registerByIP:    ratelimit.New(registerPerIP, resetLimitWindow),
		registerByEmail: ratelimit.New(registerPerEmail, resetLimitWindow),
		verifyByIP:      ratelimit.New(verifyPerIP, resetLimitWindow),
	}
}

// WithInternal sets the guard of the /internal routes, which other services call with a shared
// secret instead of a token (see httpx.RequireInternal). It returns h, so it can follow New.
// Without it those routes answer the same JSON 404 as an unknown route.
func (h *Handler) WithInternal(requireInternal httpx.Middleware) *Handler {
	h.requireInternal = requireInternal
	return h
}

// Route is one endpoint. Path uses the same {param} syntax as ServeMux and OpenAPI.
type Route struct {
	Method string
	Path   string
	Handle http.HandlerFunc
}

// Routes lists every endpoint of the service. Add new endpoints here. Endpoints that need
// a logged-in developer are wrapped with h.requireDeveloper; the one endpoint a game's own
// server calls (/players/auth) is wrapped with h.requireAPIKey. For example:
//
//	{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.requireWritable(h.registerGame))},
//
// Endpoints that change something for a developer (register a game, create an API key, save
// origins, save a draft, publish, roll back) are wrapped with h.requireWritable inside
// h.requireDeveloper, so a suspended developer can read everything but change nothing. Revoking an
// API key is left out on purpose: it only takes access away, and a developer must be able to kill
// a leaked key. TestWritesRefuseASuspendedDeveloper lists every write route and fails for a new one
// that is not guarded.
//
// The two password reset endpoints need no login (the developer forgot the password) and are
// rate limited instead; see password_reset.go.
//
// token/refresh and logout take no developer token either: the refresh cookie is their
// credential (sessions.go). A browser page on another origin cannot call them, because the CORS
// middleware refuses every origin that is not allowed.
//
// Endpoints under /api/v1/admin/ are for the distributor's admin panel. Every one except
// /admin/login is wrapped with h.requireAdmin, which accepts only an admin token.
//
// Endpoints under /internal/ are for other services: they need the shared secret in
// X-Internal-Token and are wrapped with h.internal. A browser must never reach them.
//
// Endpoints under /api/v1/public/ need no login at all and are open to every website
// (see the CORS setup in cmd/main.go); they must never return anything secret.
//
// Endpoints under /api/v1/games/{gameID} are wrapped with h.requireGameMember, inside h.requireDeveloper and
// h.requireWritable, so a developer can only reach a game they belong to, and the handler knows their role
// (gameFrom). The ones only an owner may use (API keys, allowed origins) add h.requireGameOwner. A developer who
// is not in the game gets 404 game_not_found; a member who is not an owner gets 403 owner_required.
// TestEveryGameRouteChecksMembership and TestGameRoutesAreForOwnersOrMembers fail for a route that is not there.
//
// The OpenAPI file (openapi/platform.yaml) must document exactly these routes;
// TestOpenAPIMatchesRoutes fails when the two drift apart.
func (h *Handler) Routes() []Route {
	return []Route{
		{http.MethodGet, "/health", h.health},
		{http.MethodGet, "/ready", h.ready},
		{http.MethodPost, "/api/v1/developers/register", h.registerDeveloper},
		{http.MethodPost, "/api/v1/developers/register/verify", h.verifyRegistration},
		{http.MethodPost, "/api/v1/developers/register/resend", h.resendRegistrationCode},
		{http.MethodPost, "/api/v1/developers/login", h.loginDeveloper},
		{http.MethodPost, "/api/v1/developers/password-reset", h.requestPasswordReset},
		{http.MethodPost, "/api/v1/developers/password-reset/confirm", h.confirmPasswordReset},
		{http.MethodPost, "/api/v1/developers/token/refresh", h.refreshDeveloperToken},
		{http.MethodPost, "/api/v1/developers/logout", h.logoutDeveloper},
		{http.MethodGet, "/api/v1/developers/me", h.requireDeveloper(h.me)},
		{http.MethodPost, "/api/v1/admin/login", h.adminLogin},
		{http.MethodGet, "/api/v1/admin/me", h.requireAdmin(h.adminMe)},
		{http.MethodGet, "/api/v1/admin/developers", h.requireAdmin(h.listAdminDevelopers)},
		{http.MethodGet, "/api/v1/admin/developers/{developerID}", h.requireAdmin(h.getAdminDeveloper)},
		{http.MethodPut, "/api/v1/admin/developers/{developerID}/status", h.requireAdmin(h.setDeveloperStatus)},
		{http.MethodPut, "/api/v1/admin/games/{gameID}/status", h.requireAdmin(h.setGameStatus)},
		{http.MethodGet, "/api/v1/admin/audit-log", h.requireAdmin(h.listAuditLog)},
		{http.MethodGet, "/api/v1/games", h.requireDeveloper(h.listGames)},
		{http.MethodPost, "/api/v1/games", h.requireDeveloper(h.requireWritable(h.registerGame))},
		{http.MethodGet, "/api/v1/games/{gameID}/members", h.requireDeveloper(h.requireGameMember(h.listGameMembers))},
		{http.MethodPost, "/api/v1/games/{gameID}/api-keys", h.requireDeveloper(h.requireWritable(h.requireGameMember(h.requireGameOwner(h.createAPIKey))))},
		{http.MethodGet, "/api/v1/games/{gameID}/api-keys", h.requireDeveloper(h.requireGameMember(h.requireGameOwner(h.listAPIKeys)))},
		{http.MethodDelete, "/api/v1/api-keys/{keyID}", h.requireDeveloper(h.revokeAPIKey)},
		{http.MethodPut, "/api/v1/games/{gameID}/allowed-origins", h.requireDeveloper(h.requireWritable(h.requireGameMember(h.requireGameOwner(h.setAllowedOrigins))))},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/draft", h.requireDeveloper(h.requireGameMember(h.getWidgetDraft))},
		{http.MethodPut, "/api/v1/games/{gameID}/widget/draft", h.requireDeveloper(h.requireWritable(h.requireGameMember(h.saveWidgetDraft)))},
		{http.MethodPost, "/api/v1/games/{gameID}/widget/publish", h.requireDeveloper(h.requireWritable(h.requireGameMember(h.publishWidget)))},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/versions", h.requireDeveloper(h.requireGameMember(h.listWidgetVersions))},
		{http.MethodGet, "/api/v1/games/{gameID}/widget/versions/{version}", h.requireDeveloper(h.requireGameMember(h.getWidgetVersion))},
		{http.MethodPost, "/api/v1/games/{gameID}/widget/versions/{version}/rollback", h.requireDeveloper(h.requireWritable(h.requireGameMember(h.rollbackWidget)))},
		{http.MethodGet, "/api/v1/public/games/{gameID}/widget-config", h.getPublicWidgetConfig},
		{http.MethodPost, "/api/v1/players/auth", h.requireAPIKey(h.authPlayer)},
		{http.MethodGet, "/internal/v1/games/{gameID}/config", h.internal(h.getInternalGameConfig)},
	}
}

// Register adds all routes to mux, plus a JSON 404 for everything else.
func (h *Handler) Register(mux *http.ServeMux) {
	for _, r := range h.Routes() {
		mux.HandleFunc(r.Method+" "+r.Path, r.Handle)
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

// internal wraps next so only a caller with the internal token reaches it. While no token is
// configured the guard answers 404, so the route looks like it does not exist.
func (h *Handler) internal(next http.HandlerFunc) http.HandlerFunc {
	guard := h.requireInternal
	if guard == nil {
		guard = httpx.RequireInternal("")
	}
	return guard(next).ServeHTTP
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "Route not found")
}

// fail writes err as the standard error response. Expected failures (*httpx.Error)
// are sent as they are; anything else is logged here and becomes a generic 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		h.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	httpx.WriteErr(w, err)
}

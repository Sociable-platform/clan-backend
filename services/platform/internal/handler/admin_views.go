package handler

import (
	"net/http"
	"strconv"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type adminDeveloperSummaryResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	GameCount int       `json:"gameCount"`
	CreatedAt time.Time `json:"createdAt"`
}

type adminDeveloperListResponse struct {
	Developers []adminDeveloperSummaryResponse `json:"developers"`
	// NextCursor is null on the last page.
	NextCursor *string `json:"nextCursor"`
}

type adminGameResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	Status          string     `json:"status"`
	StatusReason    *string    `json:"statusReason"`
	StatusChangedAt *time.Time `json:"statusChangedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	// Role is the developer's role on the game, in a developer's list of games; absent elsewhere.
	Role string `json:"role,omitempty"`
}

// adminGameMemberResponse is a developer on a game as the admin sees them: with their account status.
type adminGameMemberResponse struct {
	DeveloperID string    `json:"developerId"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	JoinedAt    time.Time `json:"joinedAt"`
}

type adminGameDetailResponse struct {
	adminGameResponse
	Members []adminGameMemberResponse `json:"members"`
}

type adminDeveloperResponse struct {
	ID              string              `json:"id"`
	Email           string              `json:"email"`
	Name            string              `json:"name"`
	Status          string              `json:"status"`
	StatusReason    *string             `json:"statusReason"`
	StatusChangedAt *time.Time          `json:"statusChangedAt"`
	CreatedAt       time.Time           `json:"createdAt"`
	Games           []adminGameResponse `json:"games"`
}

type auditEntryResponse struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	Reason     string    `json:"reason"`
	Note       *string   `json:"note"`
	CreatedAt  time.Time `json:"createdAt"`
}

type auditLogResponse struct {
	Entries    []auditEntryResponse `json:"entries"`
	NextCursor *string              `json:"nextCursor"`
}

// listAdminDevelopers handles GET /api/v1/admin/developers?q=&status=&limit=&cursor=.
func (h *Handler) listAdminDevelopers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.svc.ListDevelopers(r.Context(), service.ListDevelopersInput{
		Query: q.Get("q"), Status: q.Get("status"), Limit: limit, Cursor: q.Get("cursor"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := adminDeveloperListResponse{
		Developers: make([]adminDeveloperSummaryResponse, 0, len(page.Developers)),
		NextCursor: nilIfEmpty(page.NextCursor),
	}
	for _, d := range page.Developers {
		out.Developers = append(out.Developers, adminDeveloperSummaryResponse{
			ID: d.ID, Email: d.Email, Name: d.Name, Status: d.Status, GameCount: d.GameCount, CreatedAt: d.CreatedAt.UTC(),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getAdminDeveloper handles GET /api/v1/admin/developers/{developerID}.
func (h *Handler) getAdminDeveloper(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.AdminDeveloper(r.Context(), r.PathValue("developerID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAdminDeveloperResponse(d))
}

func toAdminDeveloperResponse(d service.AdminDeveloper) adminDeveloperResponse {
	out := adminDeveloperResponse{
		ID: d.ID, Email: d.Email, Name: d.Name, Status: d.Status, StatusReason: nilIfEmpty(d.StatusReason),
		StatusChangedAt: utcOrNil(d.StatusChangedAt), CreatedAt: d.CreatedAt.UTC(),
		Games: make([]adminGameResponse, 0, len(d.Games)),
	}
	for _, g := range d.Games {
		out.Games = append(out.Games, toAdminGameResponse(g))
	}
	return out
}

func toAdminGameResponse(g service.AdminGame) adminGameResponse {
	return adminGameResponse{
		ID: g.ID, Name: g.Name, Description: g.Description, Status: g.Status, StatusReason: nilIfEmpty(g.StatusReason),
		StatusChangedAt: utcOrNil(g.StatusChangedAt), CreatedAt: g.CreatedAt.UTC(), Role: g.Role,
	}
}

// getAdminGame handles GET /api/v1/admin/games/{gameID}: the game with the developers on it.
func (h *Handler) getAdminGame(w http.ResponseWriter, r *http.Request) {
	g, err := h.svc.AdminGameDetail(r.Context(), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := adminGameDetailResponse{adminGameResponse: toAdminGameResponse(g.AdminGame), Members: make([]adminGameMemberResponse, 0, len(g.Members))}
	for _, m := range g.Members {
		out.Members = append(out.Members, adminGameMemberResponse{DeveloperID: m.DeveloperID, Name: m.Name, Email: m.Email,
			Role: m.Role, Status: m.Status, JoinedAt: m.JoinedAt.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// listAuditLog handles GET /api/v1/admin/audit-log?targetId=&limit=&cursor=.
func (h *Handler) listAuditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.svc.AuditLog(r.Context(), service.AuditLogInput{
		TargetID: q.Get("targetId"), Limit: limit, Cursor: q.Get("cursor"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := auditLogResponse{Entries: make([]auditEntryResponse, 0, len(page.Entries)), NextCursor: nilIfEmpty(page.NextCursor)}
	for _, e := range page.Entries {
		out.Entries = append(out.Entries, auditEntryResponse{
			ID: e.ID, Actor: e.Actor, Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
			Reason: e.Reason, Note: nilIfEmpty(e.Note), CreatedAt: e.CreatedAt.UTC(),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// parseLimit reads the limit query parameter: 0 when it is absent (the service applies the
// default), 400 invalid_limit when it is not a whole number. The service checks the range.
func parseLimit(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n == 0 {
		return 0, httpx.NewError(http.StatusBadRequest, "invalid_limit", "Limit must be a whole number of at least 1")
	}
	return n, nil
}

// nilIfEmpty makes an empty string JSON null.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

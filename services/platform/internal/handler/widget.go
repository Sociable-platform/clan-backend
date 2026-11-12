package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// saveWidgetDraftRequest is the body of PUT .../widget/draft.
type saveWidgetDraftRequest struct {
	Config json.RawMessage `json:"config"`
}

// widgetDraftResponse is a draft as the dashboard sees it. UpdatedAt is null while
// nothing was saved and Config is the default.
type widgetDraftResponse struct {
	Config    json.RawMessage `json:"config"`
	UpdatedAt *time.Time      `json:"updatedAt"`
}

func toWidgetDraftResponse(d service.WidgetDraft) widgetDraftResponse {
	r := widgetDraftResponse{Config: d.Config}
	if d.UpdatedAt != nil {
		t := d.UpdatedAt.UTC()
		r.UpdatedAt = &t
	}
	return r
}

// getWidgetDraft handles GET /api/v1/games/{gameID}/widget/draft.
func (h *Handler) getWidgetDraft(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.WidgetDraft(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWidgetDraftResponse(d))
}

// saveWidgetDraft handles PUT /api/v1/games/{gameID}/widget/draft. The body replaces the
// whole draft; the game comes from the URL and is checked against the logged-in developer.
func (h *Handler) saveWidgetDraft(w http.ResponseWriter, r *http.Request) {
	var req saveWidgetDraftRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.SaveWidgetDraft(r.Context(), developerID(r.Context()), r.PathValue("gameID"), req.Config)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWidgetDraftResponse(d))
}

// widgetVersionSummary is a published version as lists show it: no config.
type widgetVersionSummary struct {
	Version     int       `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	PublishedBy string    `json:"publishedBy"`
}

// widgetVersionResponse is one published version with its config.
type widgetVersionResponse struct {
	widgetVersionSummary
	Config json.RawMessage `json:"config"`
}

type widgetVersionListResponse struct {
	Versions []widgetVersionSummary `json:"versions"`
}

func toWidgetVersionSummary(v service.WidgetVersion) widgetVersionSummary {
	return widgetVersionSummary{Version: v.Version, PublishedAt: v.PublishedAt.UTC(), PublishedBy: v.PublishedBy}
}

func toWidgetVersionResponse(v service.WidgetVersion) widgetVersionResponse {
	return widgetVersionResponse{widgetVersionSummary: toWidgetVersionSummary(v), Config: v.Config}
}

// versionNumber matches a positive whole number without a sign or leading zeros.
var versionNumber = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// versionFromPath reads {version} from the URL. Anything that is not a positive whole
// number is a version that does not exist (404), not a bad request.
func versionFromPath(r *http.Request) (int, error) {
	raw := r.PathValue("version")
	if !versionNumber.MatchString(raw) {
		return 0, httpx.NewError(http.StatusNotFound, "widget_version_not_found", "Widget version not found")
	}
	return strconv.Atoi(raw)
}

// publishWidget handles POST /api/v1/games/{gameID}/widget/publish. There is no body: it
// publishes the draft the server holds, so a stale browser tab cannot publish another config.
func (h *Handler) publishWidget(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.PublishWidget(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toWidgetVersionResponse(v))
}

// listWidgetVersions handles GET /api/v1/games/{gameID}/widget/versions, newest first.
func (h *Handler) listWidgetVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := h.svc.WidgetVersions(r.Context(), developerID(r.Context()), r.PathValue("gameID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := widgetVersionListResponse{Versions: make([]widgetVersionSummary, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, toWidgetVersionSummary(v))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getWidgetVersion handles GET /api/v1/games/{gameID}/widget/versions/{version}.
func (h *Handler) getWidgetVersion(w http.ResponseWriter, r *http.Request) {
	n, err := versionFromPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v, err := h.svc.WidgetVersion(r.Context(), developerID(r.Context()), r.PathValue("gameID"), n)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWidgetVersionResponse(v))
}

// rollbackWidget handles POST /api/v1/games/{gameID}/widget/versions/{version}/rollback.
// It publishes that old version again as a new version; the history only grows.
func (h *Handler) rollbackWidget(w http.ResponseWriter, r *http.Request) {
	n, err := versionFromPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v, err := h.svc.RollbackWidget(r.Context(), developerID(r.Context()), r.PathValue("gameID"), n)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toWidgetVersionResponse(v))
}

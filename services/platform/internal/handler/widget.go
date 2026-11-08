package handler

import (
	"encoding/json"
	"net/http"
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

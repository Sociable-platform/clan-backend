package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

// publicWidgetCacheControl lets browsers and CDNs keep the config for a minute, so a game
// page that loads often does not hit the database every time. After a publish or rollback,
// players see the new config within this time. Change the number here to trade freshness
// for load.
const publicWidgetCacheControl = "public, max-age=60"

// publicWidgetConfigResponse is everything a player's page gets: the config and which
// version it is. Nothing about who published it or when.
type publicWidgetConfigResponse struct {
	Version int             `json:"version"`
	Config  json.RawMessage `json:"config"`
}

// widgetETag is the strong ETag of a version. It is built from the version number alone, so
// it changes exactly when a publish or rollback adds a new version, and never otherwise.
func widgetETag(version int) string {
	return `"v` + strconv.Itoa(version) + `"`
}

// etagMatches reports whether an If-None-Match header names etag. The header is a list of
// ETags, or "*" for "any version you have"; a weak ETag (W/"v3") counts as a match, as
// the rules for If-None-Match say.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// getPublicWidgetConfig handles GET /api/v1/public/games/{gameID}/widget-config.
//
// It needs no login: a game's page loads it in the player's browser, and the game ID is
// public. It only ever returns a published version, never a draft. Errors are not cached,
// so a game that has just been published is seen at once.
func (h *Handler) getPublicWidgetConfig(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.PublicWidgetConfig(r.Context(), r.PathValue("gameID"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		h.fail(w, r, err)
		return
	}
	etag := widgetETag(v.Version)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", publicWidgetCacheControl)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, publicWidgetConfigResponse{Version: v.Version, Config: v.Config})
}

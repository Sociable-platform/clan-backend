package handler

import (
	"errors"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const (
	// refreshCookieName is the cookie that holds a developer's refresh token.
	refreshCookieName = "clan_refresh"
	// refreshCookiePath limits where the browser sends the cookie: to the developer endpoints
	// (login sets it, token/refresh and logout read it) and to nothing else of the API.
	refreshCookiePath = "/api/v1/developers"
	// sessionPerIP limits token/refresh and logout together, per IP address and resetLimitWindow.
	// A refresh token is 256 random bits, so this is not against guessing; it only keeps one
	// address from flooding the endpoints. It is high because the dashboard refreshes on every
	// page load and several developers can share one address.
	sessionPerIP = 300
)

// WithInsecureCookies lets the refresh cookie travel over plain http. It is for a developer's
// machine, where the dashboard and platform run on http://localhost; everywhere else the cookie
// must stay Secure, which is the default. It returns h, so it can follow New.
func (h *Handler) WithInsecureCookies() *Handler {
	h.insecureCookies = true
	return h
}

// setRefreshCookie stores the refresh token in the browser. JavaScript cannot read the cookie
// (HttpOnly), and other sites cannot make the browser send it (SameSite=Lax). The dashboard and
// platform are different origins of the same site, so the dashboard's own requests carry it.
func (h *Handler) setRefreshCookie(w http.ResponseWriter, token string, maxAgeSeconds int) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   !h.insecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearRefreshCookie makes the browser forget the refresh token.
func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   !h.insecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// refreshTokenOf returns the refresh token the browser sent, or "" when there is none.
func refreshTokenOf(r *http.Request) string {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// writeSession answers a login or a refresh: the developer token in the body, the refresh token
// (when the session has one) in the cookie. Neither may be cached.
func (h *Handler) writeSession(w http.ResponseWriter, s service.Session) {
	if s.RefreshToken != "" {
		h.setRefreshCookie(w, s.RefreshToken, s.RefreshExpiresIn)
	}
	w.Header().Set("Cache-Control", "no-store") // the response holds a credential
	httpx.WriteJSON(w, http.StatusOK, loginResponse{Token: s.Token, ExpiresIn: s.ExpiresIn})
}

// refreshDeveloperToken handles POST /api/v1/developers/token/refresh. The request has no body
// and no Authorization header: the refresh cookie is the credential. The answer is a new
// developer token and a new cookie; the old refresh token stops working.
func (h *Handler) refreshDeveloperToken(w http.ResponseWriter, r *http.Request) {
	if !h.sessionByIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.sessionByIP)
		return
	}
	s, err := h.svc.RefreshSession(r.Context(), refreshTokenOf(r))
	if err != nil {
		// A refused token will never work again, so the browser may as well forget it.
		var apiErr *httpx.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			h.clearRefreshCookie(w)
		}
		h.fail(w, r, err)
		return
	}
	h.writeSession(w, s)
}

// logoutDeveloper handles POST /api/v1/developers/logout. It ends the session of the refresh
// cookie and clears the cookie. It answers 204 with or without a session, so the dashboard can
// always finish logging out.
func (h *Handler) logoutDeveloper(w http.ResponseWriter, r *http.Request) {
	if !h.sessionByIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.sessionByIP)
		return
	}
	if err := h.svc.Logout(r.Context(), refreshTokenOf(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

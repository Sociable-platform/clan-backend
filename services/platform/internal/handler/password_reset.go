package handler

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/ratelimit"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// Limits of the password reset endpoints, per resetLimitWindow. They are counted in memory
// (pkg/ratelimit), so each platform pod counts on its own.
//
// The limit per email is the one that protects a person: nobody can make platform send them more
// than a few emails. The limits per IP address are looser, because several developers can share
// one address (an office, or a proxy in front of platform that hides the real one).
const (
	resetLimitWindow  = 15 * time.Minute
	resetPerEmail     = 3
	resetPerIP        = 20
	resetConfirmPerIP = 20
	// maxLimitedEmailLen is the longest valid email address (the service's own limit).
	maxLimitedEmailLen = 254
)

type passwordResetRequest struct {
	Email string `json:"email"`
}

type passwordResetConfirmRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type messageResponse struct {
	Message string `json:"message"`
}

// requestPasswordReset handles POST /api/v1/developers/password-reset.
// The answer is 202 whether or not the email belongs to an account, so the endpoint cannot be
// used to find out who is registered.
func (h *Handler) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !h.resetByIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.resetByIP)
		return
	}
	var req passwordResetRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	// Counted for every email, registered or not, so a 429 says nothing about the account either.
	// An address too long to be real is not counted (it would only fill the limiter's memory);
	// the service refuses it as invalid_email.
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if len(email) <= maxLimitedEmailLen && !h.resetByEmail.Allow(email) {
		tooManyRequests(w, h.resetByEmail)
		return
	}
	if err := h.svc.RequestPasswordReset(r.Context(), req.Email); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, messageResponse{
		Message: "If an account with this email exists, a link to reset the password has been sent to it.",
	})
}

// confirmPasswordReset handles POST /api/v1/developers/password-reset/confirm.
func (h *Handler) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !h.resetConfirmIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.resetConfirmIP)
		return
	}
	var req passwordResetConfirmRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), service.ResetPasswordInput{Token: req.Token, Password: req.Password}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tooManyRequests(w http.ResponseWriter, l *ratelimit.Limiter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(l.RetryAfter().Seconds())))
	httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again in a few minutes.")
}

// clientIP returns the address the request came from, as a rate limit key. Behind the ingress the
// connection comes from the proxy, which puts the caller's address last in X-Forwarded-For; the
// entries before it are whatever the caller sent and are ignored. Without the header it is the
// address of the connection.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

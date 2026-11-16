package handler

import (
	"net/http"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// Limits of registering with email verification, per resetLimitWindow and counted in memory like
// the password reset limits (password_reset.go).
//
// The limit per email covers register and "send a new code" together and protects a person:
// nobody can make platform send them more than a few codes. The service adds its own rule of one
// email a minute per registration. Guessing a code is stopped elsewhere, by the service's limit
// on wrong codes per registration; the limit per IP address on verify only keeps one address from
// trying many registrations.
const (
	registerPerEmail = 5
	registerPerIP    = 20
	verifyPerIP      = 60
)

type verifyRegistrationRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type resendRegistrationRequest struct {
	Email string `json:"email"`
}

// allowRegistrationEmail counts one code email for an address and for the caller's IP address,
// and answers 429 itself when either is over its limit.
func (h *Handler) allowRegistrationEmail(w http.ResponseWriter, r *http.Request, email string) bool {
	if !h.registerByIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.registerByIP)
		return false
	}
	// An address too long to be real is not counted; the service refuses it as invalid_email.
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) <= maxLimitedEmailLen && !h.registerByEmail.Allow(email) {
		tooManyRequests(w, h.registerByEmail)
		return false
	}
	return true
}

// requestRegistration is register with email verification on: nothing is created yet. The answer
// is the same 202 whether or not the address already has an account.
func (h *Handler) requestRegistration(w http.ResponseWriter, r *http.Request, req registerRequest) {
	if !h.allowRegistrationEmail(w, r, req.Email) {
		return
	}
	in := service.RegisterInput{Email: req.Email, Password: req.Password, Name: req.Name}
	if err := h.svc.RequestRegistration(r.Context(), in); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, messageResponse{
		Message: "We sent a code to this email address. Enter it to finish registering.",
	})
}

// verifyRegistration handles POST /api/v1/developers/register/verify: the code from the email
// creates the developer. While email verification is off the route answers 404.
func (h *Handler) verifyRegistration(w http.ResponseWriter, r *http.Request) {
	if !h.svc.EmailVerificationOn() {
		h.notFound(w, r)
		return
	}
	if !h.verifyByIP.Allow(clientIP(r)) {
		tooManyRequests(w, h.verifyByIP)
		return
	}
	var req verifyRegistrationRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.VerifyRegistration(r.Context(), service.VerifyRegistrationInput{Email: req.Email, Code: req.Code})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toDeveloperResponse(d))
}

// resendRegistrationCode handles POST /api/v1/developers/register/resend: a new code for a
// registration that is still waiting. The answer is 202 whether or not there is one. While email
// verification is off the route answers 404.
func (h *Handler) resendRegistrationCode(w http.ResponseWriter, r *http.Request) {
	if !h.svc.EmailVerificationOn() {
		h.notFound(w, r)
		return
	}
	var req resendRegistrationRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.allowRegistrationEmail(w, r, req.Email) {
		return
	}
	if err := h.svc.ResendRegistrationCode(r.Context(), req.Email); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, messageResponse{
		Message: "If a registration is waiting for this email address, a new code has been sent to it.",
	})
}

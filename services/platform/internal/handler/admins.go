package handler

import (
	"context"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type adminResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// adminLogin handles POST /api/v1/admin/login. It is the only admin route without requireAdmin.
func (h *Handler) adminLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	s, err := h.svc.AdminLogin(r.Context(), service.LoginInput{Email: req.Email, Password: req.Password})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the response holds a credential
	httpx.WriteJSON(w, http.StatusOK, loginResponse{Token: s.Token, ExpiresIn: s.ExpiresIn})
}

// adminMe handles GET /api/v1/admin/me.
func (h *Handler) adminMe(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.AdminMe(r.Context(), adminID(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminResponse{ID: a.ID, Email: a.Email, Name: a.Name, CreatedAt: a.CreatedAt.UTC()})
}

type adminCtxKey struct{}

// adminID returns the admin ID that requireAdmin stored in ctx.
func adminID(ctx context.Context) string {
	id, _ := ctx.Value(adminCtxKey{}).(string)
	return id
}

// requireAdmin wraps next so it only runs for a valid admin token ("Authorization: Bearer <token>")
// of an admin who is not disabled. Anything else, including a developer or player token, gets 401.
// Every route under /api/v1/admin/ except login goes through it; TestAdminRoutesRequireAdmin checks that.
func (h *Handler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "A valid admin token is required")
			return
		}
		id, err := h.svc.AuthenticateAdmin(r.Context(), token)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), adminCtxKey{}, id)))
	}
}

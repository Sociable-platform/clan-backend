package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type developerResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"` // seconds
}

func toDeveloperResponse(d service.Developer) developerResponse {
	return developerResponse{ID: d.ID, Email: d.Email, Name: d.Name, CreatedAt: d.CreatedAt.UTC()}
}

// registerDeveloper handles POST /api/v1/developers/register.
func (h *Handler) registerDeveloper(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.Register(r.Context(), service.RegisterInput{Email: req.Email, Password: req.Password, Name: req.Name})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toDeveloperResponse(d))
}

// loginDeveloper handles POST /api/v1/developers/login.
func (h *Handler) loginDeveloper(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	s, err := h.svc.Login(r.Context(), service.LoginInput{Email: req.Email, Password: req.Password})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the response holds a credential
	httpx.WriteJSON(w, http.StatusOK, loginResponse{Token: s.Token, ExpiresIn: s.ExpiresIn})
}

// me handles GET /api/v1/developers/me.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Me(r.Context(), developerID(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeveloperResponse(d))
}

type ctxKey struct{}

// developerID returns the developer ID that requireDeveloper stored in ctx.
func developerID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// requireDeveloper wraps next so it only runs for a valid developer token
// ("Authorization: Bearer <token>"). Anything else, including a player token, gets 401.
func (h *Handler) requireDeveloper(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "A valid developer token is required")
			return
		}
		id, err := h.svc.AuthenticateDeveloper(token)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	}
}

// bearerToken reads the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

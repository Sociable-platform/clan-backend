package handler

import (
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

type registerGameRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type gameResponse struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	AllowedOrigins []string  `json:"allowedOrigins"`
	CreatedAt      time.Time `json:"createdAt"`
}

type gameListResponse struct {
	Games []gameResponse `json:"games"`
}

func toGameResponse(g service.Game) gameResponse {
	origins := g.AllowedOrigins
	if origins == nil {
		origins = []string{} // JSON [] and not null
	}
	return gameResponse{ID: g.ID, Name: g.Name, Description: g.Description, AllowedOrigins: origins, CreatedAt: g.CreatedAt.UTC()}
}

// registerGame handles POST /api/v1/games: it records an existing game for the logged-in developer.
func (h *Handler) registerGame(w http.ResponseWriter, r *http.Request) {
	var req registerGameRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	g, err := h.svc.RegisterGame(r.Context(), developerID(r.Context()),
		service.RegisterGameInput{Name: req.Name, Description: req.Description})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toGameResponse(g))
}

// listGames handles GET /api/v1/games: only the logged-in developer's games.
func (h *Handler) listGames(w http.ResponseWriter, r *http.Request) {
	games, err := h.svc.ListGames(r.Context(), developerID(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := gameListResponse{Games: make([]gameResponse, 0, len(games))}
	for _, g := range games {
		out.Games = append(out.Games, toGameResponse(g))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

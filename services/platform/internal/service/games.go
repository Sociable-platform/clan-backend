package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Limits for game input.
const (
	maxGameNameLen        = 100
	maxGameDescriptionLen = 500
)

// uuidPattern is the text form of a UUID. Checking it first keeps a malformed ID from
// reaching the database, where it would be an error instead of a plain "not found".
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Game is a developer's integration record for an existing game.
type Game struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
}

// RegisterGameInput is what a developer sends to register a game.
type RegisterGameInput struct {
	Name        string
	Description string
}

func errGameNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
}

// RegisterGame records an existing game for developerID. It does not create a game.
func (s *Service) RegisterGame(ctx context.Context, developerID string, in RegisterGameInput) (Game, error) {
	name := strings.TrimSpace(in.Name)
	description := strings.TrimSpace(in.Description)
	if n := utf8.RuneCountInString(name); n == 0 || n > maxGameNameLen {
		return Game{}, httpx.NewError(http.StatusBadRequest, "invalid_name",
			fmt.Sprintf("Game name must be 1 to %d characters", maxGameNameLen))
	}
	if utf8.RuneCountInString(description) > maxGameDescriptionLen {
		return Game{}, httpx.NewError(http.StatusBadRequest, "invalid_description",
			fmt.Sprintf("Description must be at most %d characters", maxGameDescriptionLen))
	}

	g, err := s.store.CreateGame(ctx, developerID, name, description)
	if errors.Is(err, repository.ErrNotFound) {
		return Game{}, errUnauthorized() // the account behind a valid token is gone
	}
	if err != nil {
		return Game{}, fmt.Errorf("create game: %w", err)
	}
	return toGame(g), nil
}

// ListGames returns only the games owned by developerID, newest first.
func (s *Service) ListGames(ctx context.Context, developerID string) ([]Game, error) {
	rows, err := s.store.GamesByDeveloper(ctx, developerID)
	if err != nil {
		return nil, fmt.Errorf("list games of developer %s: %w", developerID, err)
	}
	games := make([]Game, 0, len(rows))
	for _, g := range rows {
		games = append(games, toGame(g))
	}
	return games, nil
}

// OwnedGame returns the game gameID if developerID owns it. Another developer's game,
// a game that does not exist and a malformed ID all give the same 404, so the answer
// never reveals whether someone else's game exists. Endpoints under /games/{gameID}
// call this before doing anything else.
func (s *Service) OwnedGame(ctx context.Context, developerID, gameID string) (Game, error) {
	if !uuidPattern.MatchString(gameID) {
		return Game{}, errGameNotFound()
	}
	g, err := s.store.GameOfDeveloper(ctx, gameID, developerID)
	if errors.Is(err, repository.ErrNotFound) {
		return Game{}, errGameNotFound()
	}
	if err != nil {
		return Game{}, fmt.Errorf("find game %s: %w", gameID, err)
	}
	return toGame(g), nil
}

func toGame(g repository.Game) Game {
	return Game{ID: g.ID, Name: g.Name, Description: g.Description, CreatedAt: g.CreatedAt}
}

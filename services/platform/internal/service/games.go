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
	ID string
	// Role is the asking developer's role on the game: "owner" or "member" (empty when the game was read
	// without a developer). It is looked up on every request and is never part of the login token.
	Role        string
	Name        string
	Description string
	// AllowedOrigins are the websites that may open a WebSocket for the game; never nil.
	AllowedOrigins []string
	CreatedAt      time.Time
}

// RegisterGameInput is what a developer sends to register a game.
type RegisterGameInput struct {
	Name        string
	Description string
}

func errGameNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
}

// errOwnerRequired is what a member gets for an action only the game's owner may do. It is a 403, not a 404: the
// developer already knows the game, they just may not do this to it.
func errOwnerRequired() *httpx.Error {
	return httpx.NewError(http.StatusForbidden, "owner_required", "Only the game's owner can do this")
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

// ListGames returns only the games developerID belongs to, newest first.
func (s *Service) ListGames(ctx context.Context, developerID string) ([]Game, error) {
	rows, err := s.store.GamesOfMember(ctx, developerID)
	if err != nil {
		return nil, fmt.Errorf("list games of developer %s: %w", developerID, err)
	}
	games := make([]Game, 0, len(rows))
	for _, g := range rows {
		games = append(games, toGame(g))
	}
	return games, nil
}

// MemberGame returns the game gameID, with developerID's role on it, if developerID belongs to it. A game
// of other developers, a game that does not exist and a malformed ID all give the same 404, so the answer
// never reveals whether someone else's game exists. Every endpoint under /games/{gameID} goes through
// this: the handler's requireGameMember asks it first, and each service method asks it again.
func (s *Service) MemberGame(ctx context.Context, developerID, gameID string) (Game, error) {
	if !uuidPattern.MatchString(gameID) {
		return Game{}, errGameNotFound()
	}
	g, err := s.store.GameOfMember(ctx, gameID, developerID)
	if errors.Is(err, repository.ErrNotFound) {
		return Game{}, errGameNotFound()
	}
	if err != nil {
		return Game{}, fmt.Errorf("find game %s: %w", gameID, err)
	}
	return toGame(g), nil
}

// OwnerGame is MemberGame for an action only an owner may do (API keys, allowed origins, the team): a member of
// the game gets 403 owner_required, and a developer who is not in the game still gets the 404 of MemberGame,
// so nobody learns that a game exists by being refused.
func (s *Service) OwnerGame(ctx context.Context, developerID, gameID string) (Game, error) {
	g, err := s.MemberGame(ctx, developerID, gameID)
	if err != nil {
		return Game{}, err
	}
	if g.Role != repository.RoleOwner {
		return Game{}, errOwnerRequired()
	}
	return g, nil
}

// GameMember is a developer on a game, as the team list shows them.
type GameMember struct {
	DeveloperID string
	Name        string
	Email       string
	Role        string // "owner" or "member"
	JoinedAt    time.Time
}

// GameMembers lists everybody on the game, owners first, for any member of it: a team knows who is on it. A
// developer who is not in the game gets the 404 of MemberGame.
func (s *Service) GameMembers(ctx context.Context, developerID, gameID string) ([]GameMember, error) {
	if _, err := s.MemberGame(ctx, developerID, gameID); err != nil {
		return nil, err
	}
	rows, err := s.store.GameMembers(ctx, gameID)
	if err != nil {
		return nil, fmt.Errorf("list members of game %s: %w", gameID, err)
	}
	members := make([]GameMember, 0, len(rows))
	for _, m := range rows {
		members = append(members, GameMember{DeveloperID: m.DeveloperID, Name: m.Name, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt})
	}
	return members, nil
}

func toGame(g repository.Game) Game {
	origins := g.AllowedOrigins
	if origins == nil {
		origins = []string{}
	}
	return Game{ID: g.ID, Role: g.Role, Name: g.Name, Description: g.Description, AllowedOrigins: origins, CreatedAt: g.CreatedAt}
}

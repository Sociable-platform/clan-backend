package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Limits for the allowed origins of a game.
const (
	maxAllowedOrigins   = 20
	maxAllowedOriginLen = 255
)

// InternalGameConfig is what another service (realtime) learns about a game: the active
// published config and the websites allowed to connect. It is built from Platform's own data and
// must only go to a caller that proved it is another service.
type InternalGameConfig struct {
	GameID string
	// Status is StatusSuspended when the game or its developer is suspended, else StatusActive.
	// Other services never see the developer: this is the one word they act on.
	Status string
	// Version is the newest published version, 0 when the game was never published.
	Version int
	// Config is that version's config; nil when Version is 0.
	Config json.RawMessage
	// AllowedOrigins is never nil. Empty means the developer has set none.
	AllowedOrigins []string
}

func errInvalidOrigins(message string) *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, "invalid_origins", message)
}

// SetAllowedOrigins replaces the allowed origins of a game the developer owns. Each origin is
// written as the browser sends it in the Origin header, scheme://host[:port]: http or https, no
// path, query, fragment or user name, no trailing slash, no wildcard. Hosts are lower-cased and
// repeats are dropped. An empty list is allowed and clears the setting.
func (s *Service) SetAllowedOrigins(ctx context.Context, developerID, gameID string, origins []string) (Game, error) {
	if _, err := s.MemberGame(ctx, developerID, gameID); err != nil {
		return Game{}, err
	}
	if len(origins) > maxAllowedOrigins {
		return Game{}, errInvalidOrigins(fmt.Sprintf("A game can have at most %d allowed origins", maxAllowedOrigins))
	}

	clean := make([]string, 0, len(origins))
	seen := make(map[string]bool, len(origins))
	for _, raw := range origins {
		o, err := normalizeOrigin(raw)
		if err != nil {
			return Game{}, err
		}
		if !seen[o] {
			seen[o] = true
			clean = append(clean, o)
		}
	}

	g, err := s.store.SetAllowedOrigins(ctx, gameID, developerID, clean)
	if errors.Is(err, repository.ErrNotFound) {
		return Game{}, errGameNotFound()
	}
	if err != nil {
		return Game{}, fmt.Errorf("set allowed origins of game %s: %w", gameID, err)
	}
	return toGame(g), nil
}

// normalizeOrigin checks one origin and returns it in the form the browser sends.
func normalizeOrigin(raw string) (string, error) {
	o := strings.TrimSpace(raw)
	bad := func(why string) error {
		return errInvalidOrigins(fmt.Sprintf("%q is not a valid origin: %s. Use scheme://host[:port], like https://play.example.com", raw, why))
	}
	if o == "" || len(o) > maxAllowedOriginLen {
		return "", bad(fmt.Sprintf("it must be 1 to %d characters", maxAllowedOriginLen))
	}
	if strings.Contains(o, "*") {
		return "", bad("wildcards are not supported")
	}
	u, err := url.Parse(o)
	if err != nil {
		return "", bad("it cannot be read as a URL")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "http" && scheme != "https":
		return "", bad("the scheme must be http or https")
	case u.Hostname() == "":
		return "", bad("the host is missing")
	case strings.HasSuffix(u.Host, ":"):
		return "", bad("the port is missing after the colon")
	case u.User != nil:
		return "", bad("it must not contain a user name")
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(o, "?") || strings.HasSuffix(o, "#"):
		return "", bad("it must not contain a path, query or trailing slash")
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// InternalGameConfig returns the published config and the allowed origins of a game, for another
// service. The game ID is not secret, so an unknown ID and a malformed one both give 404
// game_not_found. A game that exists but was never published is not an error: its Version is 0,
// because realtime needs the origins of such a game too. Status is the effective one, the same
// rule that decides whether a game gets a player token.
func (s *Service) InternalGameConfig(ctx context.Context, gameID string) (InternalGameConfig, error) {
	if !uuidPattern.MatchString(gameID) {
		return InternalGameConfig{}, errGameNotFound()
	}
	g, err := s.store.GameByID(ctx, gameID)
	if errors.Is(err, repository.ErrNotFound) {
		return InternalGameConfig{}, errGameNotFound()
	}
	if err != nil {
		return InternalGameConfig{}, fmt.Errorf("find game %s: %w", gameID, err)
	}

	why, err := s.suspension(ctx, gameID)
	if err != nil {
		return InternalGameConfig{}, err
	}
	status := StatusActive
	if why != notSuspended {
		status = StatusSuspended
	}

	out := InternalGameConfig{GameID: g.ID, Status: status, AllowedOrigins: toGame(g).AllowedOrigins}
	v, err := s.store.LatestWidgetVersion(ctx, gameID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// never published: version 0, no config
	case err != nil:
		return InternalGameConfig{}, fmt.Errorf("get latest widget version of game %s: %w", gameID, err)
	default:
		out.Version, out.Config = v.Version, json.RawMessage(v.Config)
	}
	return out, nil
}

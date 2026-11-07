package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

// PlayerTokenTTL is how long a player token stays valid. It is short on purpose:
// the browser holds it, so the game's server hands out a fresh one when it expires.
const PlayerTokenTTL = time.Hour

// Limits for the player the game's server vouches for.
const (
	maxExternalPlayerIDLen = 128
	maxDisplayNameLen      = 50
)

// PlayerTokenInput is what a game's server sends to get a token for one of its players.
type PlayerTokenInput struct {
	ExternalPlayerID string // the game's own ID for the player, e.g. "u_42"
	DisplayName      string
}

// PlayerToken is a freshly issued player token.
type PlayerToken struct {
	Token     string
	ExpiresIn int // seconds
}

// IssuePlayerToken signs a short-lived player token for a player of gameID.
//
// gameID must come from a verified API key (GameForAPIKey), never from the request:
// it is the only thing that ties the token to a game, and social trusts it completely.
func (s *Service) IssuePlayerToken(gameID string, in PlayerTokenInput) (PlayerToken, error) {
	playerID := strings.TrimSpace(in.ExternalPlayerID)
	name := strings.TrimSpace(in.DisplayName)
	if n := utf8.RuneCountInString(playerID); n == 0 || n > maxExternalPlayerIDLen || hasControlChars(playerID) {
		return PlayerToken{}, httpx.NewError(http.StatusBadRequest, "invalid_external_player_id",
			fmt.Sprintf("externalPlayerId must be 1 to %d characters without control characters", maxExternalPlayerIDLen))
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxDisplayNameLen || hasControlChars(name) {
		return PlayerToken{}, httpx.NewError(http.StatusBadRequest, "invalid_display_name",
			fmt.Sprintf("displayName must be 1 to %d characters without control characters", maxDisplayNameLen))
	}

	token, err := jwt.Sign(s.key, jwt.Claims{
		Subject: playerID,
		GameID:  gameID,
		Name:    name,
		Type:    jwt.TypePlayer,
	}, PlayerTokenTTL, s.now())
	if err != nil {
		return PlayerToken{}, fmt.Errorf("sign player token: %w", err)
	}
	return PlayerToken{Token: token, ExpiresIn: int(PlayerTokenTTL.Seconds())}, nil
}

func hasControlChars(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

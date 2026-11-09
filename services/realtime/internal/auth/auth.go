// Package auth checks the player token a client sends when it opens a WebSocket.
//
// Browsers cannot set headers on a WebSocket, so the token comes in the query string
// (?token=...). Verify checks it with platform's public key: no call to platform is needed.
// The game ID and the player come only from the verified token, never from anything else
// the client sends.
package auth

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

// Player is the caller, as proven by their token.
type Player struct {
	GameID     string    // the token's gid: the game the player belongs to (a lowercase UUID)
	ExternalID string    // the token's sub: the game's own player ID, e.g. "u_42"
	Name       string    // the token's name: display name
	ExpiresAt  time.Time // the token's exp: the connection must be closed when this passes
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Verifier checks player tokens with platform's public key.
type Verifier struct {
	key ed25519.PublicKey
	now func() time.Time
}

// NewVerifier returns a Verifier. now is the clock: time.Now in production, a fixed time in tests.
func NewVerifier(key ed25519.PublicKey, now func() time.Time) *Verifier {
	return &Verifier{key: key, now: now}
}

// Verify returns the player named by token. Every failure is a 401 *httpx.Error with a stable code:
// missing_token, token_expired, wrong_token_type or invalid_token. The token itself is never put
// in an error message.
func (v *Verifier) Verify(token string) (Player, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Player{}, unauthorized("missing_token", "Send a player token: ?token=<token>")
	}

	claims, err := jwt.Verify(v.key, token, jwt.TypePlayer, v.now())
	switch {
	case errors.Is(err, jwt.ErrExpired):
		return Player{}, unauthorized("token_expired", "The player token has expired; get a new one from the game")
	case errors.Is(err, jwt.ErrType):
		return Player{}, unauthorized("wrong_token_type", "This endpoint needs a player token")
	case err != nil:
		return Player{}, unauthorized("invalid_token", "The player token is not valid")
	}

	// The signature is valid, but the token must also name a player and a real game ID.
	if claims.Subject == "" || !uuidPattern.MatchString(claims.GameID) {
		return Player{}, unauthorized("invalid_token", "The player token is missing the player or the game")
	}

	return Player{
		GameID:     strings.ToLower(claims.GameID),
		ExternalID: claims.Subject,
		Name:       claims.Name,
		ExpiresAt:  time.Unix(claims.ExpiresAt, 0),
	}, nil
}

func unauthorized(code, message string) *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, code, message)
}

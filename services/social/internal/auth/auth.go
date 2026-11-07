// Package auth checks player tokens on social's API routes.
//
// Every /api/v1 request must carry a player token that platform signed:
//
//	Authorization: Bearer <token>
//
// RequirePlayer verifies the token with platform's public key (no call to platform),
// answers 401 for anything else, and stores the player in the request context.
// Handlers read the caller with PlayerFrom. The game ID used in every database query
// comes from there, never from the request body or URL, so a player can only ever
// see and change data of their own game.
package auth

import (
	"context"
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
	GameID     string // the token's gid: the game the player belongs to (a UUID)
	ExternalID string // the token's sub: the game's own player ID, e.g. "u_42"
	Name       string // the token's name: display name
}

// ctxKey is private, so no other package can overwrite the player in the context.
type ctxKey struct{}

// PlayerFrom returns the player that RequirePlayer stored in the request context.
// ok is false when the request did not pass through RequirePlayer (a programming error).
func PlayerFrom(ctx context.Context) (Player, bool) {
	p, ok := ctx.Value(ctxKey{}).(Player)
	return p, ok
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RequirePlayer returns middleware that lets a request through only with a valid player token.
// now is the clock: time.Now in production, a fixed time in tests.
func RequirePlayer(key ed25519.PublicKey, now func() time.Time) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				unauthorized(w, "missing_token", "Send a player token: Authorization: Bearer <token>")
				return
			}

			claims, err := jwt.Verify(key, token, jwt.TypePlayer, now())
			switch {
			case errors.Is(err, jwt.ErrExpired):
				unauthorized(w, "token_expired", "The player token has expired; get a new one from the game")
				return
			case errors.Is(err, jwt.ErrType):
				unauthorized(w, "wrong_token_type", "This endpoint needs a player token")
				return
			case err != nil:
				unauthorized(w, "invalid_token", "The player token is not valid")
				return
			}

			// The signature is valid, but the token must also name a player and a real game ID.
			if claims.Subject == "" || !uuidPattern.MatchString(claims.GameID) {
				unauthorized(w, "invalid_token", "The player token is missing the player or the game")
				return
			}

			player := Player{
				GameID:     strings.ToLower(claims.GameID),
				ExternalID: claims.Subject,
				Name:       claims.Name,
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, player)))
		})
	}
}

// bearerToken reads "Authorization: Bearer <token>". The word Bearer is case-insensitive.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// unauthorized answers 401 in the standard error format. WWW-Authenticate is the
// standard header that tells clients which kind of credentials to send.
func unauthorized(w http.ResponseWriter, code, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="social"`)
	httpx.WriteError(w, http.StatusUnauthorized, code, message)
}

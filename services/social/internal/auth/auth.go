// Package auth checks player tokens on social's API routes.
//
// Every /api/v1 request must carry a player token that platform signed:
//
//	Authorization: Bearer <token>
//
// RequirePlayer verifies the token with platform's public key (no call to platform),
// answers 401 for anything else, and stores the player in the request context.
// RequireServedGame then refuses the requests of a suspended game with 403, and
// RejectDeletedPlayers the tokens issued before their player was deleted with 401.
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
	GameID     string    // the token's gid: the game the player belongs to (a UUID)
	ExternalID string    // the token's sub: the game's own player ID, e.g. "u_42"
	Name       string    // the token's name: display name
	IssuedAt   time.Time // the token's iat, to the second
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
				IssuedAt:   time.Unix(claims.IssuedAt, 0),
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, player)))
		})
	}
}

// RequireServedGame returns middleware that refuses a request with 403 game_suspended while the
// caller's game is suspended. It must run after RequirePlayer, so the token is proven first: a
// stranger without a valid token gets 401 and learns nothing about any game. isSuspended gets
// the game ID of the token and must never fail: when it cannot tell, it says false (fail open).
//
// Nothing is changed or deleted for a suspended game; its friends, clans and players stay as
// they were and work again as soon as the game is resumed.
func RequireServedGame(isSuspended func(ctx context.Context, gameID string) bool) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			player, ok := PlayerFrom(r.Context())
			if !ok {
				// RequirePlayer did not run first: a wiring mistake, never a client error.
				httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong")
				return
			}
			if isSuspended(r.Context(), player.GameID) {
				// Same code and message as Platform, so a player never learns whose switch is off.
				httpx.WriteError(w, http.StatusForbidden, "game_suspended", "This game is suspended")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RejectDeletedPlayers returns middleware that refuses a token issued before its player was
// deleted (TEAM-249) with 401 player_deleted. A player token cannot be revoked and lives for an
// hour, so without this a deleted player could keep using it, and the next request would even
// create them again. It must run after RequirePlayer. deletedSince gets the caller and must never
// fail: when it cannot tell, it says false (fail open), like RequireServedGame, and the request
// meets the same trouble further on.
//
// A token issued after the deletion passes: the game vouches for a new account with that player
// ID, and it gets a new, empty profile.
func RejectDeletedPlayers(deletedSince func(ctx context.Context, p Player) bool) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			player, ok := PlayerFrom(r.Context())
			if !ok {
				// RequirePlayer did not run first: a wiring mistake, never a client error.
				httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong")
				return
			}
			if deletedSince(r.Context(), player) {
				unauthorized(w, "player_deleted", "This player was deleted; the game must issue a new token")
				return
			}
			next.ServeHTTP(w, r)
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

package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// servedCall sends req through RequirePlayer and then RequireServedGame, like the service does,
// and reports what happened. asked lists the game IDs isSuspended was asked about.
func servedCall(t *testing.T, suspended map[string]bool, req *http.Request, pub ed25519.PublicKey) (status int, code string, reached bool, asked []string) {
	t.Helper()
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	isSuspended := func(_ context.Context, gameID string) bool {
		asked = append(asked, gameID)
		return suspended[gameID]
	}
	rec := httptest.NewRecorder()
	h := RequirePlayer(pub, func() time.Time { return testNow })(RequireServedGame(isSuspended)(endpoint))
	h.ServeHTTP(rec, req)
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Error.Code, reached, asked
}

func TestRequireServedGameRefusesASuspendedGame(t *testing.T) {
	pub, priv := testKeys(t)
	tok := sign(t, priv, playerClaims("u_42", "A6C1E2F0-1111-4222-8333-944455556666"), time.Hour) // upper case in the token

	status, code, reached, asked := servedCall(t, map[string]bool{testGame: true}, withAuth("Bearer "+tok), pub)
	if status != http.StatusForbidden || code != "game_suspended" || reached {
		t.Fatalf("status %d, code %q, reached %v; want 403 game_suspended and the endpoint not reached", status, code, reached)
	}
	// it is asked about the game of the token, lower-cased like everywhere else
	if len(asked) != 1 || asked[0] != testGame {
		t.Fatalf("asked about %v, want [%s]", asked, testGame)
	}
}

func TestRequireServedGameLetsAServedGameThrough(t *testing.T) {
	pub, priv := testKeys(t)
	tok := sign(t, priv, playerClaims("u_42", testGame), time.Hour)

	// another game is suspended; this one is not
	status, _, reached, _ := servedCall(t, map[string]bool{"99999999-9999-4999-8999-999999999999": true}, withAuth("Bearer "+tok), pub)
	if status != http.StatusOK || !reached {
		t.Fatalf("status %d, reached %v; want 200 and the endpoint reached", status, reached)
	}
}

// The token is proven first, so somebody without one learns nothing about any game.
func TestRequireServedGameNeverRunsBeforeTheTokenIsProven(t *testing.T) {
	pub, priv := testKeys(t)
	suspended := map[string]bool{testGame: true}
	expired := sign(t, priv, playerClaims("u_42", testGame), -time.Minute)

	for name, header := range map[string]string{"no token": "", "expired token": "Bearer " + expired, "garbage": "Bearer nope"} {
		t.Run(name, func(t *testing.T) {
			status, code, reached, asked := servedCall(t, suspended, withAuth(header), pub)
			if status != http.StatusUnauthorized || reached || len(asked) != 0 {
				t.Fatalf("status %d (%s), reached %v, asked %v; want 401 and no question about any game", status, code, reached, asked)
			}
		})
	}
}

// Without RequirePlayer in front there is no player: that is a wiring mistake, so it is a 500,
// and never an answer that lets the request through.
func TestRequireServedGameWithoutRequirePlayerIsAnError(t *testing.T) {
	reached := false
	h := RequireServedGame(func(context.Context, string) bool { return false })(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withAuth(""))
	if rec.Code != http.StatusInternalServerError || reached {
		t.Fatalf("status %d, reached %v; want 500 and the endpoint not reached", rec.Code, reached)
	}
}

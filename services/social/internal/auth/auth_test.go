package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

// A fixed clock makes the expiry tests independent of when they run.
var testNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const testGame = "a6c1e2f0-1111-4222-8333-944455556666"

// testKeys returns a fresh pair: the private key plays platform, the public key plays social.
func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// sign makes a token the way platform will: same claims, same signing code.
func sign(t *testing.T, key ed25519.PrivateKey, c jwt.Claims, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.Sign(key, c, ttl, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func playerClaims(sub, game string) jwt.Claims {
	return jwt.Claims{Subject: sub, GameID: game, Name: "Ali", Type: jwt.TypePlayer}
}

// result is what one request through RequirePlayer produced.
type result struct {
	status    int
	code      string // error code from the JSON body, "" on success
	challenge string // WWW-Authenticate header
	reached   bool   // did the endpoint behind the guard run?
	player    Player // the player the endpoint saw
}

// call sends req through RequirePlayer(pub) to a dummy endpoint.
func call(t *testing.T, pub ed25519.PublicKey, req *http.Request) result {
	t.Helper()
	var res result
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res.reached = true
		res.player, _ = PlayerFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	RequirePlayer(pub, func() time.Time { return testNow })(endpoint).ServeHTTP(rec, req)

	res.status = rec.Code
	res.challenge = rec.Header().Get("WWW-Authenticate")
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	res.code = body.Error.Code
	return res
}

func withAuth(header string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	return r
}

func TestRequirePlayerRejects(t *testing.T) {
	pub, priv := testKeys(t)
	_, otherPriv := testKeys(t)

	tests := []struct {
		name     string
		header   string
		wantCode string
	}{
		{"no Authorization header", "", "missing_token"},
		{"other scheme (Basic)", "Basic dXNlcjpwYXNz", "missing_token"},
		{"Bearer without a token", "Bearer ", "missing_token"},
		{"token without Bearer", sign(t, priv, playerClaims("u_42", testGame), time.Hour), "missing_token"},
		{"not a token at all", "Bearer abc.def.ghi", "invalid_token"},
		{"signed with a different key", "Bearer " + sign(t, otherPriv, playerClaims("u_42", testGame), time.Hour), "invalid_token"},
		{"expired", "Bearer " + sign(t, priv, playerClaims("u_42", testGame), -time.Minute), "token_expired"},
		{"developer token", "Bearer " + sign(t, priv, jwt.Claims{Subject: "d_1", Type: jwt.TypeDeveloper}, time.Hour), "wrong_token_type"},
		{"game ID is not a UUID", "Bearer " + sign(t, priv, playerClaims("u_42", "1; DROP TABLE clans"), time.Hour), "invalid_token"},
		{"no game ID", "Bearer " + sign(t, priv, playerClaims("u_42", ""), time.Hour), "invalid_token"},
		{"no player ID", "Bearer " + sign(t, priv, playerClaims("", testGame), time.Hour), "invalid_token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, pub, withAuth(tc.header))
			if res.status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", res.status)
			}
			if res.code != tc.wantCode {
				t.Errorf("error code = %q, want %q", res.code, tc.wantCode)
			}
			if !strings.HasPrefix(res.challenge, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", res.challenge)
			}
			if res.reached {
				t.Error("the endpoint ran although the token was rejected")
			}
		})
	}
}

func TestRequirePlayerAcceptsValidToken(t *testing.T) {
	pub, priv := testKeys(t)
	tok := sign(t, priv, playerClaims("u_42", "A6C1E2F0-1111-4222-8333-944455556666"), time.Hour)

	for _, scheme := range []string{"Bearer", "bearer"} {
		res := call(t, pub, withAuth(scheme+" "+tok))
		if res.status != http.StatusOK || !res.reached {
			t.Fatalf("%s: status %d, reached %v", scheme, res.status, res.reached)
		}
		// game ID lower-cased; the issue time is the token's iat (sign uses testNow)
		want := Player{GameID: testGame, ExternalID: "u_42", Name: "Ali", IssuedAt: time.Unix(testNow.Unix(), 0)}
		if res.player != want {
			t.Errorf("%s: player = %+v, want %+v", scheme, res.player, want)
		}
	}
}

// The game ID must come only from the signed token, never from the URL or body,
// so a player cannot reach another game's data by sending a different ID.
func TestGameIDComesOnlyFromToken(t *testing.T) {
	pub, priv := testKeys(t)
	tok := sign(t, priv, playerClaims("u_42", testGame), time.Hour)
	otherGame := "99999999-9999-4999-8999-999999999999"

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/clans?game_id="+otherGame+"&gid="+otherGame,
		strings.NewReader(`{"game_id":"`+otherGame+`","gid":"`+otherGame+`"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Game-ID", otherGame)

	res := call(t, pub, req)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d", res.status)
	}
	if res.player.GameID != testGame {
		t.Fatalf("game ID = %q, want the token's %q", res.player.GameID, testGame)
	}
}

func TestPlayerFromWithoutMiddleware(t *testing.T) {
	if _, ok := PlayerFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Fatal("PlayerFrom should report ok=false when RequirePlayer did not run")
	}
}

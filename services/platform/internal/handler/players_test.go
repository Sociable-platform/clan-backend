package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const playersAuthPath = "/api/v1/players/auth"

// callWithKey posts body to path the way a game's server does: no Authorization header,
// the secret in X-API-Key (left out when apiKey is empty).
func callWithKey(t *testing.T, svc Service, method, path, body, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, discardLog()).Register(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// keyedFake accepts exactly one API key, "clk_good", which belongs to game someGameID.
func keyedFake() fakeService {
	return fakeService{gameForAPIKey: func(key string) (string, error) {
		if key != "clk_good" {
			return "", httpx.NewError(http.StatusUnauthorized, "invalid_api_key", "API key is missing, wrong or revoked")
		}
		return someGameID, nil
	}}
}

func TestAuthPlayer_IssuesToken(t *testing.T) {
	var gotGame string
	var gotIn service.PlayerTokenInput
	svc := keyedFake()
	svc.issuePlayerToken = func(gameID string, in service.PlayerTokenInput) (service.PlayerToken, error) {
		gotGame, gotIn = gameID, in
		return service.PlayerToken{Token: "tok.en.sig", ExpiresIn: 3600}, nil
	}

	rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, `{"externalPlayerId":"u_42","displayName":"Rashad"}`, "clk_good")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"token":"tok.en.sig","expiresIn":3600}`+"\n" {
		t.Fatalf("body %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	if gotGame != someGameID {
		t.Fatalf("game %q: it must come from the API key", gotGame)
	}
	if gotIn != (service.PlayerTokenInput{ExternalPlayerID: "u_42", DisplayName: "Rashad"}) {
		t.Fatalf("service got %+v", gotIn)
	}
}

func TestAuthPlayer_BodyCannotChooseTheGame(t *testing.T) {
	var called bool
	svc := keyedFake()
	svc.issuePlayerToken = func(string, service.PlayerTokenInput) (service.PlayerToken, error) {
		called = true
		return service.PlayerToken{}, nil
	}

	for _, body := range []string{
		`{"externalPlayerId":"u_42","displayName":"R","gameId":"` + someGameID + `"}`,
		`{"externalPlayerId":"u_42","displayName":"R","gid":"x"}`,
	} {
		rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, body, "clk_good")
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
			t.Fatalf("body %s: got %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if called {
		t.Fatal("no token may be issued for a body with extra fields")
	}
}

func TestAuthPlayer_BadKeyIs401BeforeTheBodyIsRead(t *testing.T) {
	svc := keyedFake() // issuePlayerToken is nil: any call would panic

	for name, key := range map[string]string{"missing": "", "wrong": "clk_bad"} {
		t.Run(name, func(t *testing.T) {
			// A broken body must not change the answer: the key is checked first.
			for _, body := range []string{`{"externalPlayerId":"u_42","displayName":"R"}`, `not json`, ``} {
				rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, body, key)
				if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_api_key" {
					t.Fatalf("body %q: got %d %s", body, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "clk_") {
					t.Fatalf("the response must not echo a key: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestAuthPlayer_IgnoresBearerTokens(t *testing.T) {
	// Only the API key opens this endpoint; a developer token in Authorization does not.
	svc := keyedFake()
	svc.auth = func(string) (string, error) { return "d1", nil }

	mux := http.NewServeMux()
	New("test", svc, discardLog()).Register(mux)
	req := httptest.NewRequest(http.MethodPost, playersAuthPath, strings.NewReader(`{"externalPlayerId":"u","displayName":"R"}`))
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_api_key" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthPlayer_MapsErrors(t *testing.T) {
	svc := keyedFake()
	svc.issuePlayerToken = func(string, service.PlayerTokenInput) (service.PlayerToken, error) {
		return service.PlayerToken{}, httpx.NewError(http.StatusBadRequest, "invalid_display_name", "bad name")
	}

	rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, `{"externalPlayerId":"u_42"}`, "clk_good")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_display_name" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	rec = callWithKey(t, svc, http.MethodPost, playersAuthPath, `{"externalPlayerId":`, "clk_good")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthPlayer_UnexpectedErrorIsNotLeaked(t *testing.T) {
	svc := fakeService{gameForAPIKey: func(string) (string, error) {
		return "", errTest("pq: connection refused to platform_user@postgres")
	}}

	rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, `{}`, "clk_good")

	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "platform_user") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// TestPlayerAuthFlow runs the whole story through the real handler and service:
// a developer makes a key, the game's server trades it for a player token, social
// (public key only) accepts that token, and a revoked key gets nothing.
func TestPlayerAuthFlow(t *testing.T) {
	pub, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, signingKey, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	creds := `"email":"alice@example.com","password":"correct horse"`
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var login loginResponse
	_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "").Body.Bytes(), &login)

	mkGame := func(name string) string {
		var g gameResponse
		_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"`+name+`"}`, login.Token).Body.Bytes(), &g)
		return g.ID
	}
	mkKey := func(gameID string) newAPIKeyResponse {
		var k newAPIKeyResponse
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/"+gameID+"/api-keys", "", login.Token)
		_ = json.Unmarshal(rec.Body.Bytes(), &k)
		return k
	}
	gameA, gameB := mkGame("Space Raiders"), mkGame("Second Game")
	keyA, keyB := mkKey(gameA), mkKey(gameB)

	const body = `{"externalPlayerId":"u_42","displayName":"Rashad"}`
	issue := func(key string) (playerAuthResponse, *httptest.ResponseRecorder) {
		rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, body, key)
		var out playerAuthResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out, rec
	}

	// Each key gives a token for its own game; verified with the public key only.
	for _, tt := range []struct{ key, game string }{{keyA.Key, gameA}, {keyB.Key, gameB}} {
		out, rec := issue(tt.key)
		if rec.Code != http.StatusOK || out.ExpiresIn != 3600 {
			t.Fatalf("auth: %d %s", rec.Code, rec.Body.String())
		}
		c, err := jwt.Verify(pub, out.Token, jwt.TypePlayer, time.Now())
		if err != nil {
			t.Fatalf("social could not verify the token: %v", err)
		}
		if c.GameID != tt.game || c.Subject != "u_42" || c.Name != "Rashad" || c.ExpiresAt-c.IssuedAt != 3600 {
			t.Fatalf("claims %+v, want game %s", c, tt.game)
		}
		// Platform's own developer endpoints refuse it.
		if rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/developers/me", "", out.Token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("player token on a developer endpoint: %d", rec.Code)
		}
	}

	// A developer token is not an API key, and neither is the key's own prefix.
	for _, key := range []string{login.Token, keyA.KeyPrefix, ""} {
		if _, rec := issue(key); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_api_key" {
			t.Fatalf("key %q: %d %s", key, rec.Code, rec.Body.String())
		}
	}

	// Revoke key A: it stops at once, key B is not affected.
	if rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/api-keys/"+keyA.ID, "", login.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	out, rec := issue(keyA.Key)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_api_key" || out.Token != "" {
		t.Fatalf("revoked key: %d %s", rec.Code, rec.Body.String())
	}
	if _, rec := issue(keyB.Key); rec.Code != http.StatusOK {
		t.Fatalf("other key after revoke: %d %s", rec.Code, rec.Body.String())
	}
}

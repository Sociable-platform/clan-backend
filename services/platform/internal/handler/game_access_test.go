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

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const goodPlayerBody = `{"externalPlayerId":"u_42","displayName":"Rashad"}`

func TestAuthPlayer_SuspendedGameIs403(t *testing.T) {
	for _, code := range []string{"game_suspended", "account_suspended"} {
		t.Run(code, func(t *testing.T) {
			issued := false
			svc := keyedFake()
			svc.ensureGameServed = func(string) error {
				return httpx.NewError(http.StatusForbidden, code, "suspended")
			}
			svc.issuePlayerToken = func(string, service.PlayerTokenInput) (service.PlayerToken, error) {
				issued = true
				return service.PlayerToken{Token: "tok", ExpiresIn: 3600}, nil
			}

			// Even a body that would be rejected gets the 403: the game is checked before the body is read.
			for _, body := range []string{goodPlayerBody, `not json`} {
				rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, body, "clk_good")
				if rec.Code != http.StatusForbidden || errorCode(t, rec) != code {
					t.Fatalf("body %q: %d %s", body, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "tok") {
					t.Fatalf("a token leaked: %s", rec.Body.String())
				}
			}
			if issued {
				t.Fatal("a token was issued for a suspended game")
			}
		})
	}
}

func TestAuthPlayer_BadKeyIsStill401ForASuspendedGame(t *testing.T) {
	checked := false
	svc := keyedFake()
	svc.ensureGameServed = func(string) error {
		checked = true
		return httpx.NewError(http.StatusForbidden, "game_suspended", "suspended")
	}
	rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, goodPlayerBody, "clk_wrong")
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_api_key" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if checked {
		t.Fatal("the suspension was looked up for a key that is not valid, which would tell a stranger a game exists")
	}
}

func TestAuthPlayer_AccessCheckFailureIsNotLeaked(t *testing.T) {
	svc := keyedFake()
	svc.ensureGameServed = func(string) error { return errTest("pq: connection to 10.0.0.5 refused") }
	rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, goodPlayerBody, "clk_good")
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestPublicWidgetConfig_SuspendedIs403AndNotCached(t *testing.T) {
	svc := fakeService{publicWidgetConfig: func(string) (service.WidgetVersion, error) {
		return service.WidgetVersion{}, httpx.NewError(http.StatusForbidden, "game_suspended", "This game is suspended")
	}}
	// With or without a conditional header: no 304, no ETag, nothing cacheable.
	for _, inm := range []string{"", `"v3"`, "*"} {
		rec := getPublic(t, svc, publicConfigPath, inm)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "game_suspended" {
			t.Fatalf("If-None-Match %q: %d %s", inm, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("ETag") != "" {
			t.Fatalf("If-None-Match %q: Cache-Control %q, ETag %q", inm, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
		}
	}
}

// TestSuspensionFlow runs the story of the ticket through the real handler and service: an admin
// suspends a game and then a developer through the API, and the game's server and the players'
// browser are cut off at once, with old and new keys alike, until the admin resumes.
func TestSuspensionFlow(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{admins: []repository.Admin{{ID: "a1", Email: "ops@example.com", PasswordHash: string(hash), Name: "Ops", CreatedAt: time.Now()}}}
	svc, err := service.New(store, signingKey, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body, token string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), method, path, body, token)
	}

	// A developer with two games, one key for the first, and a published widget.
	creds := `"email":"alice@example.com","password":"correct horse"`
	var dev developerResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, "").Body.Bytes(), &dev)
	var login loginResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "").Body.Bytes(), &login)
	mkGame := func(name string) string {
		var g gameResponse
		_ = json.Unmarshal(do(http.MethodPost, "/api/v1/games", `{"name":"`+name+`"}`, login.Token).Body.Bytes(), &g)
		return g.ID
	}
	mkKey := func(gameID string) string {
		var k newAPIKeyResponse
		_ = json.Unmarshal(do(http.MethodPost, "/api/v1/games/"+gameID+"/api-keys", "", login.Token).Body.Bytes(), &k)
		return k.Key
	}
	gameA, gameB := mkGame("Space Raiders"), mkGame("Second Game")
	oldKeyA, keyB := mkKey(gameA), mkKey(gameB)
	for _, g := range []string{gameA, gameB} {
		base := "/api/v1/games/" + g + "/widget"
		do(http.MethodPut, base+"/draft", `{"config":`+cfg("Look")+`}`, login.Token)
		if rec := do(http.MethodPost, base+"/publish", "", login.Token); rec.Code != http.StatusCreated {
			t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
		}
	}

	var admin loginResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/admin/login", `{"email":"ops@example.com","password":"correct horse"}`, "").Body.Bytes(), &admin)
	setStatus := func(path, body string) {
		t.Helper()
		if rec := do(http.MethodPut, path, body, admin.Token); rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// expect checks both public entry points of a game: the game server's key and the players' config.
	expect := func(step, gameID, key string, wantCode int, wantError string) {
		t.Helper()
		rec := callWithKey(t, svc, http.MethodPost, playersAuthPath, goodPlayerBody, key)
		if rec.Code != wantCode || (wantError != "" && errorCode(t, rec) != wantError) {
			t.Fatalf("%s: players/auth: %d %s, want %d %s", step, rec.Code, rec.Body.String(), wantCode, wantError)
		}
		widgetCode, widgetError := http.StatusOK, ""
		if wantCode != http.StatusOK {
			widgetCode, widgetError = http.StatusForbidden, "game_suspended" // players never see "account_suspended"
		}
		for _, inm := range []string{"", `"v1"`} {
			pub := getPublic(t, svc, "/api/v1/public/games/"+gameID+"/widget-config", inm)
			wantWidget := widgetCode
			if widgetCode == http.StatusOK && inm != "" {
				wantWidget = http.StatusNotModified
			}
			if pub.Code != wantWidget || (widgetError != "" && errorCode(t, pub) != widgetError) {
				t.Fatalf("%s: widget-config (If-None-Match %q): %d %s, want %d %s", step, inm, pub.Code, pub.Body.String(), wantWidget, widgetError)
			}
			if widgetCode != http.StatusOK && pub.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: a refusal must not be cacheable, Cache-Control %q", step, pub.Header().Get("Cache-Control"))
			}
		}
	}

	expect("before", gameA, oldKeyA, http.StatusOK, "")
	expect("before (game B)", gameB, keyB, http.StatusOK, "")

	// Suspend game A: its old key is refused, and so is a key made afterwards. Game B is untouched.
	setStatus("/api/v1/admin/games/"+gameA+"/status", `{"status":"suspended","reason":"abuse"}`)
	newKeyA := mkKey(gameA)
	expect("game A suspended, old key", gameA, oldKeyA, http.StatusForbidden, "game_suspended")
	expect("game A suspended, new key", gameA, newKeyA, http.StatusForbidden, "game_suspended")
	expect("game A suspended, game B", gameB, keyB, http.StatusOK, "")

	// Resume game A: both entry points work again at once, with the same keys.
	setStatus("/api/v1/admin/games/"+gameA+"/status", `{"status":"active"}`)
	expect("game A resumed, old key", gameA, oldKeyA, http.StatusOK, "")
	expect("game A resumed, new key", gameA, newKeyA, http.StatusOK, "")

	// Suspend the developer: every game goes down. The game server is told why, the players are not.
	setStatus("/api/v1/admin/developers/"+dev.ID+"/status", `{"status":"suspended","reason":"non_payment","note":"unpaid"}`)
	expect("developer suspended, game A", gameA, oldKeyA, http.StatusForbidden, "account_suspended")
	expect("developer suspended, game B", gameB, keyB, http.StatusForbidden, "account_suspended")

	// A developer who is suspended and has a suspended game: the developer's switch is the one reported,
	// and resuming only the developer leaves that game off.
	setStatus("/api/v1/admin/games/"+gameA+"/status", `{"status":"suspended","reason":"abuse"}`)
	setStatus("/api/v1/admin/developers/"+dev.ID+"/status", `{"status":"active"}`)
	expect("developer resumed, game A still suspended", gameA, oldKeyA, http.StatusForbidden, "game_suspended")
	expect("developer resumed, game B", gameB, keyB, http.StatusOK, "")

	setStatus("/api/v1/admin/games/"+gameA+"/status", `{"status":"active"}`)
	expect("everything resumed", gameA, oldKeyA, http.StatusOK, "")
}

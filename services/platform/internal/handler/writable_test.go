package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"crypto/ed25519"
	"crypto/rand"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// suspendedDeveloper is a fake service for developer "d1" whose account is suspended: the token is
// accepted, but every write check answers 403. Any other call a handler makes panics on its nil
// field, which the tests below report as "not guarded".
func suspendedDeveloper() fakeService {
	return fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		ensureDeveloperActive: func(string) error {
			return httpx.NewError(http.StatusForbidden, "account_suspended", "Your account is suspended.")
		},
	}
}

// exemptFromTheWriteGuard lists the routes that are not GET, are not under /api/v1/admin/ and are
// not guarded by requireWritable, with the reason. Everything else that changes data must be.
var exemptFromTheWriteGuard = map[string]string{
	"POST /api/v1/developers/register":        "before login: there is no account yet",
	"POST /api/v1/developers/register/verify": "before login: it creates the account",
	"POST /api/v1/developers/register/resend": "before login: there is no account yet",
	"POST /api/v1/developers/login":           "a suspended developer must be able to log in and see why",
	"POST /api/v1/players/auth":               "guarded by the game's status instead (EnsureGameServed)",
	"DELETE /api/v1/api-keys/{keyID}":         "revoking only takes access away; a developer must be able to kill a leaked key",
	// Before login, like the two above: a suspended developer who forgot the password must still get in.
	"POST /api/v1/developers/password-reset":         "before login: the developer forgot the password",
	"POST /api/v1/developers/password-reset/confirm": "before login: the reset link is the proof",
	"POST /api/v1/developers/token/refresh":          "a suspended developer stays logged in, read-only",
	"POST /api/v1/developers/logout":                 "logging out only takes access away",
}

// TestWritesRefuseASuspendedDeveloper walks Routes(): every route that changes something for a
// developer must answer 403 account_suspended while the account is suspended, before it reads the
// body or touches anything. A new write route that forgets requireWritable fails here.
func TestWritesRefuseASuspendedDeveloper(t *testing.T) {
	h := New("test", suspendedDeveloper(), discardLog())
	checked := 0
	for _, r := range h.Routes() {
		key := r.Method + " " + r.Path
		if r.Method == http.MethodGet || strings.HasPrefix(r.Path, "/api/v1/admin/") || strings.HasPrefix(r.Path, "/internal/") {
			continue
		}
		if _, ok := exemptFromTheWriteGuard[key]; ok {
			continue
		}
		checked++
		t.Run(key, func(t *testing.T) {
			path := strings.NewReplacer("{gameID}", "g1", "{version}", "1", "{keyID}", "k1").Replace(r.Path)
			// An invalid body: the 403 must come before the body is read.
			req := httptest.NewRequest(r.Method, path, strings.NewReader("not json"))
			req.SetPathValue("gameID", "g1")
			req.Header.Set("Authorization", "Bearer dev-token")
			rec := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("not guarded: the handler ran and panicked on %v", p)
					}
				}()
				r.Handle(rec, req)
			}()
			if rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
				t.Fatalf("%d %s, want 403 account_suspended", rec.Code, rec.Body.String())
			}
		})
	}
	if checked < 6 {
		t.Fatalf("only %d write routes checked; the five from the ticket plus the draft save make 6", checked)
	}
}

// The exemption list must not rot: every entry has to be a real route.
func TestWriteGuardExemptionsAreRealRoutes(t *testing.T) {
	have := map[string]bool{}
	for _, r := range New("test", fakeService{}, discardLog()).Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for key := range exemptFromTheWriteGuard {
		if !have[key] {
			t.Errorf("%q is exempt but is not a route", key)
		}
	}
}

func TestWriteGuard_NotLoggedInIs401NotForbidden(t *testing.T) {
	svc := suspendedDeveloper()
	svc.auth = func(string) (string, error) {
		return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
	}
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"x"}`, "bad")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestWriteGuard_ActiveDeveloperGoesThrough(t *testing.T) {
	var gotDeveloper string
	svc := fakeService{
		auth:                  func(string) (string, error) { return "d1", nil },
		ensureDeveloperActive: func(id string) error { gotDeveloper = id; return nil },
		registerGame: func(developerID string, in service.RegisterGameInput) (service.Game, error) {
			return service.Game{ID: "g1", Name: in.Name}, nil
		},
	}
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Farm"}`, "tok")
	if rec.Code != http.StatusCreated || gotDeveloper != "d1" {
		t.Fatalf("%d %s (guard saw developer %q)", rec.Code, rec.Body.String(), gotDeveloper)
	}
}

func TestWriteGuard_FailureIsNotLeaked(t *testing.T) {
	svc := suspendedDeveloper()
	svc.ensureDeveloperActive = func(string) error { return errTest("pq: relation developers at 10.0.0.5 is gone") }
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"x"}`, "tok")
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Reads and login never ask whether the account is active.
func TestReadsDoNotCheckTheDeveloperStatus(t *testing.T) {
	svc := suspendedDeveloper()
	svc.ensureDeveloperActive = func(string) error {
		t.Error("a read asked whether the account is active")
		return nil
	}
	svc.me = func(id string) (service.Developer, error) {
		return service.Developer{ID: id, Status: "suspended", StatusReason: "non_payment"}, nil
	}
	svc.listGames = func(string) ([]service.Game, error) { return nil, nil }
	svc.login = func(service.LoginInput) (service.Session, error) {
		return service.Session{Token: "t", ExpiresIn: 1}, nil
	}

	for _, tt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/developers/me", ""},
		{http.MethodGet, "/api/v1/games", ""},
		{http.MethodPost, "/api/v1/developers/login", `{"email":"a@b.co","password":"x"}`},
	} {
		if rec := call(t, svc, discardLog(), tt.method, tt.path, tt.body, "tok"); rec.Code != http.StatusOK {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestRevokeAPIKeyStaysAllowedWhileSuspended(t *testing.T) {
	revoked := false
	svc := suspendedDeveloper()
	svc.revokeAPIKey = func(developerID, keyID string) error { revoked = true; return nil }
	rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/api-keys/k1", "", "tok")
	if rec.Code != http.StatusNoContent || !revoked {
		t.Fatalf("%d %s (revoked %v)", rec.Code, rec.Body.String(), revoked)
	}
}

func TestMe_ShowsStatusAndReasonButNeverTheNote(t *testing.T) {
	for _, tt := range []struct {
		name        string
		dev         service.Developer
		wantStatus  string
		wantReason  any
		wantInBody  []string
		wantMissing []string
	}{
		{"active", service.Developer{ID: "d1", Status: "active"}, "active", nil, []string{`"statusReason":null`}, nil},
		{"suspended", service.Developer{ID: "d1", Status: "suspended", StatusReason: "non_payment"}, "suspended", "non_payment", nil, []string{"note"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := fakeService{
				auth: func(string) (string, error) { return "d1", nil },
				me:   func(string) (service.Developer, error) { return tt.dev, nil },
			}
			rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/developers/me", "", "tok")
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || body["status"] != tt.wantStatus || body["statusReason"] != tt.wantReason {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			for _, s := range tt.wantInBody {
				if !strings.Contains(rec.Body.String(), s) {
					t.Errorf("body lacks %s: %s", s, rec.Body.String())
				}
			}
			for _, s := range tt.wantMissing {
				if strings.Contains(rec.Body.String(), s) {
					t.Errorf("body must not contain %q: %s", s, rec.Body.String())
				}
			}
		})
	}
}

// TestReadOnlyWhileSuspendedFlow is the story of the ticket through the real handler and service: an
// admin suspends a developer through the API; the developer can still log in and read everything but
// every write is refused; the admin resumes and writes work again at once.
func TestReadOnlyWhileSuspendedFlow(t *testing.T) {
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

	creds := `"email":"alice@example.com","password":"correct horse"`
	var dev developerResponse
	reg := do(http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, "")
	_ = json.Unmarshal(reg.Body.Bytes(), &dev)
	if dev.Status != "active" || dev.StatusReason != nil {
		t.Fatalf("a new developer must be active: %s", reg.Body.String())
	}
	var login loginResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "").Body.Bytes(), &login)

	var game gameResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, login.Token).Body.Bytes(), &game)
	gamePath := "/api/v1/games/" + game.ID
	var key newAPIKeyResponse
	_ = json.Unmarshal(do(http.MethodPost, gamePath+"/api-keys", "", login.Token).Body.Bytes(), &key)
	if rec := do(http.MethodPut, gamePath+"/widget/draft", `{"config":`+cfg("Look")+`}`, login.Token); rec.Code != http.StatusOK {
		t.Fatalf("save draft: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, gamePath+"/widget/publish", "", login.Token); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	var admin loginResponse
	_ = json.Unmarshal(do(http.MethodPost, "/api/v1/admin/login", `{"email":"ops@example.com","password":"correct horse"}`, "").Body.Bytes(), &admin)
	setStatus := func(body string) {
		t.Helper()
		if rec := do(http.MethodPut, "/api/v1/admin/developers/"+dev.ID+"/status", body, admin.Token); rec.Code != http.StatusOK {
			t.Fatalf("set status: %d %s", rec.Code, rec.Body.String())
		}
	}

	type request struct{ method, path, body string }
	writes := []request{
		{http.MethodPost, "/api/v1/games", `{"name":"Another"}`},
		{http.MethodPost, gamePath + "/api-keys", ""},
		{http.MethodPut, gamePath + "/allowed-origins", `{"allowedOrigins":["https://play.example.com"]}`},
		{http.MethodPut, gamePath + "/widget/draft", `{"config":` + cfg("Look2") + `}`},
		{http.MethodPost, gamePath + "/widget/publish", ""},
		{http.MethodPost, gamePath + "/widget/versions/1/rollback", ""},
	}
	reads := []request{
		{http.MethodGet, "/api/v1/developers/me", ""},
		{http.MethodGet, "/api/v1/games", ""},
		{http.MethodGet, gamePath + "/api-keys", ""},
		{http.MethodGet, gamePath + "/widget/draft", ""},
		{http.MethodGet, gamePath + "/widget/versions", ""},
		{http.MethodGet, gamePath + "/widget/versions/1", ""},
	}

	// Suspended, with an internal note that must never reach the developer.
	setStatus(`{"status":"suspended","reason":"non_payment","note":"SECRET-NOTE invoice 1042"}`)

	if rec := do(http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("a suspended developer must still log in: %d %s", rec.Code, rec.Body.String())
	}
	for _, rq := range reads {
		rec := do(rq.method, rq.path, rq.body, login.Token)
		if rec.Code != http.StatusOK {
			t.Errorf("suspended, %s %s: %d %s, want 200", rq.method, rq.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "SECRET-NOTE") || strings.Contains(rec.Body.String(), "invoice") {
			t.Errorf("suspended, %s %s: the internal note leaked: %s", rq.method, rq.path, rec.Body.String())
		}
	}
	var me developerResponse
	_ = json.Unmarshal(do(http.MethodGet, "/api/v1/developers/me", "", login.Token).Body.Bytes(), &me)
	if me.Status != "suspended" || me.StatusReason == nil || *me.StatusReason != "non_payment" {
		t.Fatalf("me while suspended: %+v", me)
	}
	for _, rq := range writes {
		rec := do(rq.method, rq.path, rq.body, login.Token)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
			t.Errorf("suspended, %s %s: %d %s, want 403 account_suspended", rq.method, rq.path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(http.MethodGet, "/api/v1/games", "", login.Token); !strings.Contains(rec.Body.String(), `"Space Raiders"`) || strings.Contains(rec.Body.String(), `"Another"`) {
		t.Fatalf("a refused write must not have changed anything: %s", rec.Body.String())
	}
	// Taking access away stays possible.
	if rec := do(http.MethodDelete, "/api/v1/api-keys/"+key.ID, "", login.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke while suspended: %d %s", rec.Code, rec.Body.String())
	}

	// Resumed: the very next write goes through.
	setStatus(`{"status":"active"}`)
	for _, rq := range writes {
		if rec := do(rq.method, rq.path, rq.body, login.Token); rec.Code >= 300 {
			t.Errorf("resumed, %s %s: %d %s, want success", rq.method, rq.path, rec.Code, rec.Body.String())
		}
	}
	_ = json.Unmarshal(do(http.MethodGet, "/api/v1/developers/me", "", login.Token).Body.Bytes(), &me)
	if me.Status != "active" || me.StatusReason != nil {
		t.Fatalf("me after resume: %+v", me)
	}
}

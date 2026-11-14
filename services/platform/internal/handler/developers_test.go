package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// call sends one request through the real mux and returns the response.
// token, when not empty, is sent as "Authorization: Bearer <token>".
func call(t *testing.T, svc Service, log *slog.Logger, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, log).Register(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

// errorCode returns error.code from an error response body.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body is not the error shape: %s", rec.Body.String())
	}
	return b.Error.Code
}

func TestRegisterDeveloper_Created(t *testing.T) {
	created := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var got service.RegisterInput
	svc := fakeService{register: func(in service.RegisterInput) (service.Developer, error) {
		got = in
		return service.Developer{ID: "d1", Email: in.Email, Name: in.Name, CreatedAt: created, Status: "active"}, nil
	}}

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register",
		`{"email":"dev@example.com","password":"correct horse","name":"Dev"}`, "")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got != (service.RegisterInput{Email: "dev@example.com", Password: "correct horse", Name: "Dev"}) {
		t.Fatalf("service got %+v", got)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	want := map[string]any{"id": "d1", "email": "dev@example.com", "name": "Dev", "createdAt": "2026-11-06T10:00:00Z", "status": "active", "statusReason": nil}
	if len(body) != len(want) {
		t.Fatalf("body has unexpected fields (password or hash leaked?): %s", rec.Body.String())
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("field %s = %v, want %v", k, body[k], v)
		}
	}
}

func TestRegisterDeveloper_Errors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"duplicate email", `{"email":"a@b.co","password":"x","name":"n"}`,
			httpx.NewError(http.StatusConflict, "email_taken", "taken"), http.StatusConflict, "email_taken"},
		{"validation failure", `{"email":"bad","password":"x","name":"n"}`,
			httpx.NewError(http.StatusBadRequest, "invalid_email", "bad"), http.StatusBadRequest, "invalid_email"},
		{"not json", `nope`, nil, http.StatusBadRequest, "invalid_json"},
		{"unknown field", `{"email":"a@b.co","password":"x","name":"n","admin":true}`, nil, http.StatusBadRequest, "invalid_json"},
		{"empty body", ``, nil, http.StatusBadRequest, "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := fakeService{register: func(service.RegisterInput) (service.Developer, error) {
				if tt.svcErr == nil {
					t.Fatal("service must not be called for an unreadable body")
				}
				return service.Developer{}, tt.svcErr
			}}
			rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", tt.body, "")
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantCode)
			}
		})
	}
}

func TestLoginDeveloper(t *testing.T) {
	svc := fakeService{login: func(in service.LoginInput) (service.Session, error) {
		if in.Password != "correct horse" {
			return service.Session{}, httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "wrong")
		}
		return service.Session{Token: "tok", ExpiresIn: 3600}, nil
	}}

	t.Run("success", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login",
			`{"email":"dev@example.com","password":"correct horse"}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != `{"token":"tok","expiresIn":3600}`+"\n" {
			t.Fatalf("body %s", rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login",
			`{"email":"dev@example.com","password":"nope"}`, "")
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_credentials" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("not json", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `x`, "")
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestMe_RequiresDeveloperToken(t *testing.T) {
	svc := fakeService{
		auth: func(token string) (string, error) {
			if token != "good" {
				return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
			}
			return "d1", nil
		},
		me: func(id string) (service.Developer, error) {
			return service.Developer{ID: id, Email: "dev@example.com", Name: "Dev"}, nil
		},
	}

	for _, tt := range []struct {
		name, header string
		want         int
	}{
		{"valid token", "Bearer good", http.StatusOK},
		{"scheme is case-insensitive", "bearer good", http.StatusOK},
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic good", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"token only", "good", http.StatusUnauthorized},
		{"rejected token", "Bearer bad", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			New("test", svc, discardLog()).Register(mux)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/developers/me", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"id":"d1"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
			if tt.want == http.StatusUnauthorized && errorCode(t, rec) != "unauthorized" {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}

func TestUnexpectedErrorIsLoggedAndNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	svc := fakeService{login: func(service.LoginInput) (service.Session, error) {
		return service.Session{}, errors.New("pq: password authentication failed for user platform_user")
	}}

	rec := call(t, svc, log, http.MethodPost, "/api/v1/developers/login", `{"email":"a@b.co","password":"x"}`, "")

	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "platform_user") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "platform_user") {
		t.Fatalf("error was not logged: %s", logs.String())
	}
}

// memStore is a tiny in-memory repository for the end-to-end test below.
type memStore struct {
	devs   []repository.Developer
	admins []repository.Admin
	// devStatus and gameStatus hold the status by ID; a missing entry means active.
	devStatus  map[string]string
	devReason  map[string]string // reason of a suspension, by developer ID
	gameStatus map[string]string
	games      []repository.Game
	keys       []memKey
	drafts     map[string]repository.WidgetDraft     // by game ID
	versions   map[string][]repository.WidgetVersion // by game ID, oldest first
}

func (m *memStore) Ping(context.Context) error { return nil }

// InTx runs fn; the handler tests do not check rollbacks (the service tests do).
func (m *memStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// AddEvents keeps nothing; the service tests check the events.
func (m *memStore) AddEvents(context.Context, ...events.Event) error { return nil }

func (m *memStore) ListDevelopers(context.Context, repository.DeveloperFilter) ([]repository.DeveloperSummary, error) {
	return nil, nil
}

func (m *memStore) AdminDeveloperByID(context.Context, string) (repository.AdminDeveloper, error) {
	return repository.AdminDeveloper{}, repository.ErrNotFound
}

func (m *memStore) AdminGamesByDeveloper(context.Context, string) ([]repository.AdminGame, error) {
	return nil, nil
}

func (m *memStore) AuditEntries(context.Context, repository.AuditFilter) ([]repository.AuditEntry, error) {
	return nil, nil
}

// Statuses are kept in two maps by ID; a missing entry means active.
func (m *memStore) statusOf(statuses map[string]string, id string) string {
	if st := statuses[id]; st != "" {
		return st
	}
	return repository.StatusActive
}

func (m *memStore) SetDeveloperStatus(_ context.Context, id string, c repository.StatusChange) (repository.AdminDeveloper, bool, error) {
	for _, d := range m.devs {
		if d.ID != id {
			continue
		}
		out := repository.AdminDeveloper{ID: d.ID, Email: d.Email, Name: d.Name, CreatedAt: d.CreatedAt}
		if m.statusOf(m.devStatus, id) == c.Status {
			out.Status = c.Status
			return out, false, nil
		}
		if m.devStatus == nil {
			m.devStatus = map[string]string{}
		}
		m.devStatus[id] = c.Status
		if m.devReason == nil {
			m.devReason = map[string]string{}
		}
		m.devReason[id] = ""
		if c.Status == repository.StatusSuspended {
			m.devReason[id] = c.Reason
		}
		out.Status = c.Status
		return out, true, nil
	}
	return repository.AdminDeveloper{}, false, repository.ErrNotFound
}

func (m *memStore) SetGameStatus(_ context.Context, id string, c repository.StatusChange) (repository.AdminGame, bool, error) {
	for _, g := range m.games {
		if g.ID != id {
			continue
		}
		out := repository.AdminGame{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt}
		if m.statusOf(m.gameStatus, id) == c.Status {
			out.Status = c.Status
			return out, false, nil
		}
		if m.gameStatus == nil {
			m.gameStatus = map[string]string{}
		}
		m.gameStatus[id] = c.Status
		out.Status = c.Status
		return out, true, nil
	}
	return repository.AdminGame{}, false, repository.ErrNotFound
}

func (m *memStore) GameAccessOf(_ context.Context, gameID string) (repository.GameAccess, error) {
	for _, g := range m.games {
		if g.ID == gameID {
			return repository.GameAccess{
				GameStatus:      m.statusOf(m.gameStatus, g.ID),
				DeveloperStatus: m.statusOf(m.devStatus, g.DeveloperID),
			}, nil
		}
	}
	return repository.GameAccess{}, repository.ErrNotFound
}

func (m *memStore) AdminByEmail(_ context.Context, email string) (repository.Admin, error) {
	for _, a := range m.admins {
		if a.Email == email {
			return a, nil
		}
	}
	return repository.Admin{}, repository.ErrNotFound
}

func (m *memStore) AdminByID(_ context.Context, id string) (repository.Admin, error) {
	for _, a := range m.admins {
		if a.ID == id {
			return a, nil
		}
	}
	return repository.Admin{}, repository.ErrNotFound
}

func (m *memStore) CreateDeveloper(_ context.Context, email, hash, name string) (repository.Developer, error) {
	for _, d := range m.devs {
		if d.Email == email {
			return repository.Developer{}, repository.ErrEmailTaken
		}
	}
	// the ID is a UUID, like in the real table
	d := repository.Developer{ID: fmt.Sprintf("00000000-0000-4000-8001-%012d", len(m.devs)+1), Email: email, PasswordHash: hash, Name: name, CreatedAt: time.Now(), Status: "active"}
	m.devs = append(m.devs, d)
	return d, nil
}

func (m *memStore) DeveloperByEmail(_ context.Context, email string) (repository.Developer, error) {
	for _, d := range m.devs {
		if d.Email == email {
			return m.withStatus(d), nil
		}
	}
	return repository.Developer{}, repository.ErrNotFound
}

func (m *memStore) DeveloperByID(_ context.Context, id string) (repository.Developer, error) {
	for _, d := range m.devs {
		if d.ID == id {
			return m.withStatus(d), nil
		}
	}
	return repository.Developer{}, repository.ErrNotFound
}

// withStatus applies the status the admin endpoints set, like the real table's status columns.
func (m *memStore) withStatus(d repository.Developer) repository.Developer {
	d.Status = m.statusOf(m.devStatus, d.ID)
	d.StatusReason = m.devReason[d.ID]
	return d
}

func (m *memStore) DeveloperStatusOf(_ context.Context, id string) (string, error) {
	d, err := m.DeveloperByID(context.Background(), id)
	return d.Status, err
}

// TestDeveloperAuthFlow runs register -> login -> me through the real handler and service.
func TestDeveloperAuthFlow(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), http.MethodPost, path, body, "")
	}
	const creds = `{"email":"Dev@Example.com","password":"correct horse","name":"Dev One"}`

	if rec := post("/api/v1/developers/register", creds); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/v1/developers/register", creds); rec.Code != http.StatusConflict || errorCode(t, rec) != "email_taken" {
		t.Fatalf("duplicate register: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/v1/developers/login", `{"email":"dev@example.com","password":"wrong password"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}

	rec := post("/api/v1/developers/login", `{"email":"dev@example.com","password":"correct horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var login loginResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &login)

	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/developers/me", "", login.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"dev@example.com"`) {
		t.Fatalf("me with login token: %d %s", rec.Code, rec.Body.String())
	}

	// A player token signed by the same key must not open a developer endpoint.
	playerToken, err := jwt.Sign(key, jwt.Claims{Subject: "u_42", GameID: "g1", Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/developers/me", "", playerToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me with player token: %d %s", rec.Code, rec.Body.String())
	}
}

func (m *memStore) CreateGame(_ context.Context, developerID, name, description string) (repository.Game, error) {
	g := repository.Game{
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", len(m.games)+1),
		DeveloperID: developerID, Name: name, Description: description, CreatedAt: time.Now(),
	}
	m.games = append(m.games, g)
	return g, nil
}

func (m *memStore) GamesByDeveloper(_ context.Context, developerID string) ([]repository.Game, error) {
	out := []repository.Game{}
	for _, g := range m.games {
		if g.DeveloperID == developerID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *memStore) GameOfDeveloper(_ context.Context, gameID, developerID string) (repository.Game, error) {
	for _, g := range m.games {
		if g.ID == gameID && g.DeveloperID == developerID {
			return g, nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const internalToken = "0123456789abcdef0123456789abcdef"

func (f fakeService) SetAllowedOrigins(_ context.Context, developerID, gameID string, origins []string) (service.Game, error) {
	return f.setAllowedOrigins(developerID, gameID, origins)
}

func (f fakeService) InternalGameConfig(_ context.Context, gameID string) (service.InternalGameConfig, error) {
	return f.internalGameConfig(gameID)
}

// These methods make memStore keep the allowed origins of a game, like the games table.

func (m *memStore) GameByID(_ context.Context, gameID string) (repository.Game, error) {
	for _, g := range m.games {
		if g.ID == gameID {
			return g, nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

func (m *memStore) SetAllowedOrigins(_ context.Context, gameID, developerID string, origins []string) (repository.Game, error) {
	for i, g := range m.games {
		if g.ID == gameID && g.DeveloperID == developerID {
			m.games[i].AllowedOrigins = origins
			return m.games[i], nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

// internalCall serves one request with the internal guard set to internalToken (or to a guard for
// the given token). header is the value of X-Internal-Token; "" sends none.
func internalCall(t *testing.T, svc Service, guard httpx.Middleware, header, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h := New("test", svc, discardLog())
	if guard != nil {
		h = h.WithInternal(guard)
	}
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if header != "" {
		req.Header.Set(httpx.InternalTokenHeader, header)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSetAllowedOrigins_Handler(t *testing.T) {
	var gotDev, gotGame string
	var gotOrigins []string
	svc := authedFake()
	svc.setAllowedOrigins = func(dev, game string, origins []string) (service.Game, error) {
		gotDev, gotGame, gotOrigins = dev, game, origins
		return service.Game{
			ID: game, Name: "Space Raiders", AllowedOrigins: origins,
			CreatedAt: time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC),
		}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPut, "/api/v1/games/"+someGameID+"/allowed-origins",
		`{"allowedOrigins":["https://play.example.com","http://localhost:5174"]}`, "good")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotGame != someGameID {
		t.Errorf("developer %q and game %q must come from the token and the path", gotDev, gotGame)
	}
	if len(gotOrigins) != 2 || gotOrigins[0] != "https://play.example.com" {
		t.Errorf("service got %v", gotOrigins)
	}
	want := `{"id":"` + someGameID + `","name":"Space Raiders","description":"",` +
		`"allowedOrigins":["https://play.example.com","http://localhost:5174"],"createdAt":"2026-11-07T09:30:00Z"}` + "\n"
	if rec.Body.String() != want {
		t.Errorf("body %s\nwant %s", rec.Body.String(), want)
	}
}

func TestSetAllowedOrigins_HandlerRefusesBadBodies(t *testing.T) {
	svc := authedFake()
	svc.setAllowedOrigins = func(string, string, []string) (service.Game, error) {
		t.Fatal("service must not be called")
		return service.Game{}, nil
	}
	tests := []struct{ name, body, code string }{
		{"missing field", `{}`, "invalid_origins"},
		{"null", `{"allowedOrigins":null}`, "invalid_origins"},
		{"not a list", `{"allowedOrigins":"https://play.example.com"}`, "invalid_json"},
		{"unknown field", `{"allowedOrigins":[],"developerId":"x"}`, "invalid_json"},
		{"not json", `nope`, "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(t, svc, discardLog(), http.MethodPut, "/api/v1/games/"+someGameID+"/allowed-origins", tt.body, "good")
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != tt.code {
				t.Errorf("status %d code %q, want 400 %q: %s", rec.Code, errorCode(t, rec), tt.code, rec.Body.String())
			}
		})
	}
}

func TestSetAllowedOrigins_HandlerMapsServiceErrorsAndNeedsALogin(t *testing.T) {
	svc := authedFake()
	svc.setAllowedOrigins = func(string, string, []string) (service.Game, error) {
		return service.Game{}, httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
	}
	path := "/api/v1/games/" + someGameID + "/allowed-origins"

	rec := call(t, svc, discardLog(), http.MethodPut, path, `{"allowedOrigins":[]}`, "good")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, token := range []string{"", "bad"} {
		rec := call(t, svc, discardLog(), http.MethodPut, path, `{"allowedOrigins":[]}`, token)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, rec.Code)
		}
	}
}

func TestGameResponsesAlwaysHaveAnAllowedOriginsList(t *testing.T) {
	svc := authedFake()
	svc.listGames = func(string) ([]service.Game, error) { return []service.Game{{ID: "g1", Name: "One"}}, nil }

	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", "good")

	if !strings.Contains(rec.Body.String(), `"allowedOrigins":[]`) {
		t.Errorf("body %s, want allowedOrigins as [] and not null", rec.Body.String())
	}
}

func TestInternalGameConfig_Handler(t *testing.T) {
	published := service.InternalGameConfig{
		GameID: someGameID, Version: 3, Status: "active", Config: json.RawMessage(`{"schemaVersion":1}`),
		AllowedOrigins: []string{"https://play.example.com"},
	}
	never := service.InternalGameConfig{GameID: someGameID, Status: "active", AllowedOrigins: []string{}}
	suspended := service.InternalGameConfig{GameID: someGameID, Version: 3, Status: "suspended", Config: json.RawMessage(`{"schemaVersion":1}`), AllowedOrigins: []string{}}
	guard := httpx.RequireInternal(internalToken)

	tests := []struct {
		name string
		cfg  service.InternalGameConfig
		want string
	}{
		{"published", published, `{"gameId":"` + someGameID + `","version":3,"status":"active","config":{"schemaVersion":1},"allowedOrigins":["https://play.example.com"]}`},
		{"never published: config is null", never, `{"gameId":"` + someGameID + `","version":0,"status":"active","config":null,"allowedOrigins":[]}`},
		{"suspended: the status says so and the rest is unchanged", suspended, `{"gameId":"` + someGameID + `","version":3,"status":"suspended","config":{"schemaVersion":1},"allowedOrigins":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotGame string
			svc := fakeService{internalGameConfig: func(game string) (service.InternalGameConfig, error) {
				gotGame = game
				return tt.cfg, nil
			}}
			rec := internalCall(t, svc, guard, internalToken, "/internal/v1/games/"+someGameID+"/config")

			if rec.Code != http.StatusOK || gotGame != someGameID {
				t.Fatalf("status %d, game %q: %s", rec.Code, gotGame, rec.Body.String())
			}
			if rec.Body.String() != tt.want+"\n" {
				t.Errorf("body %s\nwant %s", rec.Body.String(), tt.want)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
		})
	}
}

func TestInternalGameConfig_HandlerNeedsTheInternalToken(t *testing.T) {
	svc := fakeService{internalGameConfig: func(string) (service.InternalGameConfig, error) {
		t.Fatal("service must not be called without the token")
		return service.InternalGameConfig{}, nil
	}}
	path := "/internal/v1/games/" + someGameID + "/config"
	guard := httpx.RequireInternal(internalToken)

	tests := []struct {
		name       string
		guard      httpx.Middleware
		header     string
		wantStatus int
		wantCode   string
	}{
		{"no token", guard, "", http.StatusUnauthorized, "invalid_internal_token"},
		{"wrong token", guard, "wrong", http.StatusUnauthorized, "invalid_internal_token"},
		{"a developer token is not the internal token", guard, "eyJhbGciOiJFZERTQSJ9.e30.sig", http.StatusUnauthorized, "invalid_internal_token"},
		{"switched off (no token configured)", httpx.RequireInternal(""), internalToken, http.StatusNotFound, "not_found"},
		{"switched off (no guard set at all)", nil, internalToken, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := internalCall(t, svc, tt.guard, tt.header, path)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantCode {
				t.Errorf("status %d code %q, want %d %q: %s", rec.Code, errorCode(t, rec), tt.wantStatus, tt.wantCode, rec.Body.String())
			}
		})
	}
}

func TestInternalGameConfig_HandlerMapsServiceErrors(t *testing.T) {
	guard := httpx.RequireInternal(internalToken)
	path := "/internal/v1/games/" + someGameID + "/config"

	svc := fakeService{internalGameConfig: func(string) (service.InternalGameConfig, error) {
		return service.InternalGameConfig{}, httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
	}}
	rec := internalCall(t, svc, guard, internalToken, path)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}

	svc = fakeService{internalGameConfig: func(string) (service.InternalGameConfig, error) {
		return service.InternalGameConfig{}, context.DeadlineExceeded
	}}
	rec = internalCall(t, svc, guard, internalToken, path)
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAllowedOriginsFlow uses the real handler and service: set the origins, publish, and read
// both back from the internal endpoint.
func TestAllowedOriginsFlow(t *testing.T) {
	svc := newRealService(t)
	register := func(email string) string {
		t.Helper()
		creds := `"email":"` + email + `","password":"correct horse"`
		if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, ""); rec.Code != http.StatusCreated {
			t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
		}
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "")
		var l loginResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		return l.Token
	}
	alice := register("alice@example.com")

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, alice)
	var game gameResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &game)
	if len(game.AllowedOrigins) != 0 || !strings.Contains(rec.Body.String(), `"allowedOrigins":[]`) {
		t.Fatalf("a new game has no origins: %s", rec.Body.String())
	}
	gameBase := "/api/v1/games/" + game.ID
	internalPath := "/internal/v1/games/" + game.ID + "/config"
	guard := httpx.RequireInternal(internalToken)
	readInternal := func() internalGameConfigResponse {
		t.Helper()
		rec := internalCall(t, svc, guard, internalToken, internalPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("internal: %d %s", rec.Code, rec.Body.String())
		}
		var out internalGameConfigResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Not published yet: version 0 and no origins.
	if c := readInternal(); c.Version != 0 || string(c.Config) != "null" || len(c.AllowedOrigins) != 0 {
		t.Fatalf("before anything: %+v", c)
	}

	// Alice sets the origins (the list is cleaned). That a stranger cannot is tested in the service.
	rec = call(t, svc, discardLog(), http.MethodPut, gameBase+"/allowed-origins",
		`{"allowedOrigins":["HTTPS://Play.Example.com","https://play.example.com","http://localhost:5174"]}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("set origins: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, svc, discardLog(), http.MethodPut, gameBase+"/allowed-origins", `{"allowedOrigins":["*"]}`, alice); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_origins" {
		t.Fatalf("wildcard: %d %s", rec.Code, rec.Body.String())
	}

	// The game list shows them to the owner.
	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", alice)
	var list gameListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Games) != 1 || strings.Join(list.Games[0].AllowedOrigins, ",") != "https://play.example.com,http://localhost:5174" {
		t.Fatalf("list: %s", rec.Body.String())
	}

	// Published: the internal endpoint has the config and the origins together.
	call(t, svc, discardLog(), http.MethodPut, gameBase+"/widget/draft", `{"config":`+cfg("One")+`}`, alice)
	if rec := call(t, svc, discardLog(), http.MethodPost, gameBase+"/widget/publish", "", alice); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	c := readInternal()
	if c.GameID != game.ID || c.Version != 1 || string(c.Config) != cfg("One") ||
		strings.Join(c.AllowedOrigins, ",") != "https://play.example.com,http://localhost:5174" {
		t.Fatalf("after publish: %+v", c)
	}

	// Clearing the list works, and an unknown game is a 404.
	call(t, svc, discardLog(), http.MethodPut, gameBase+"/allowed-origins", `{"allowedOrigins":[]}`, alice)
	if c := readInternal(); len(c.AllowedOrigins) != 0 {
		t.Fatalf("after clearing: %+v", c)
	}
	rec = internalCall(t, svc, guard, internalToken, "/internal/v1/games/00000000-0000-4000-8000-0000000000ff/config")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Fatalf("unknown game: %d %s", rec.Code, rec.Body.String())
	}
}

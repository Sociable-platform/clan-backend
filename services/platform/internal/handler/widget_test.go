package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

const widgetDraftPath = "/api/v1/games/" + someGameID + "/widget/draft"

// These methods let memStore keep one widget draft per game, like the widget_drafts table.

func (m *memStore) WidgetDraftOfGame(_ context.Context, gameID string) (repository.WidgetDraft, error) {
	d, ok := m.drafts[gameID]
	if !ok {
		return repository.WidgetDraft{}, repository.ErrNotFound
	}
	return d, nil
}

func (m *memStore) SaveWidgetDraft(_ context.Context, gameID string, config []byte) (repository.WidgetDraft, error) {
	if m.drafts == nil {
		m.drafts = map[string]repository.WidgetDraft{}
	}
	d := repository.WidgetDraft{GameID: gameID, Config: config, UpdatedAt: time.Now()}
	m.drafts[gameID] = d
	return d, nil
}

func (m *memStore) AddWidgetVersion(_ context.Context, gameID, developerID string, config []byte) (repository.WidgetVersion, error) {
	if m.versions == nil {
		m.versions = map[string][]repository.WidgetVersion{}
	}
	v := repository.WidgetVersion{
		GameID: gameID, Version: len(m.versions[gameID]) + 1, Config: config,
		PublishedAt: time.Now(), PublishedBy: developerID,
	}
	m.versions[gameID] = append(m.versions[gameID], v)
	return v, nil
}

func (m *memStore) WidgetVersionsOfGame(_ context.Context, gameID string) ([]repository.WidgetVersion, error) {
	out := []repository.WidgetVersion{}
	all := m.versions[gameID]
	for i := len(all) - 1; i >= 0; i-- { // newest first, without configs
		v := all[i]
		v.Config = nil
		out = append(out, v)
	}
	return out, nil
}

func (m *memStore) WidgetVersionOfGame(_ context.Context, gameID string, version int) (repository.WidgetVersion, error) {
	if version < 1 || version > len(m.versions[gameID]) {
		return repository.WidgetVersion{}, repository.ErrNotFound
	}
	return m.versions[gameID][version-1], nil
}

// fakeService answers the widget calls from fields, like its other calls.
// (Its fields, widgetDraft and saveWidgetDraft, are declared with the others in handler_test.go.)

func (f fakeService) WidgetDraft(_ context.Context, developerID, gameID string) (service.WidgetDraft, error) {
	return f.widgetDraft(developerID, gameID)
}

func (f fakeService) SaveWidgetDraft(_ context.Context, developerID, gameID string, config json.RawMessage) (service.WidgetDraft, error) {
	return f.saveWidgetDraft(developerID, gameID, config)
}

func TestGetWidgetDraft_OwnerGetsTheDraft(t *testing.T) {
	var gotDev, gotGame string
	svc := authedFake()
	svc.widgetDraft = func(dev, game string) (service.WidgetDraft, error) {
		gotDev, gotGame = dev, game
		// a time in another zone: the response must show it in UTC
		saved := time.Date(2026, 11, 8, 16, 0, 0, 0, time.FixedZone("+04", 4*3600))
		return service.WidgetDraft{Config: json.RawMessage(`{"schemaVersion":1}`), UpdatedAt: &saved}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, widgetDraftPath, "", "good")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotGame != someGameID {
		t.Fatalf("developer must come from the token and the game from the URL, got %q / %q", gotDev, gotGame)
	}
	want := `{"config":{"schemaVersion":1},"updatedAt":"2026-11-08T12:00:00Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGetWidgetDraft_NothingSavedHasNoTime(t *testing.T) {
	svc := authedFake()
	svc.widgetDraft = func(string, string) (service.WidgetDraft, error) {
		return service.WidgetDraft{Config: json.RawMessage(`{"schemaVersion":1}`)}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, widgetDraftPath, "", "good")

	want := `{"config":{"schemaVersion":1},"updatedAt":null}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSaveWidgetDraft_SavesTheBody(t *testing.T) {
	var gotDev, gotGame, gotConfig string
	svc := authedFake()
	svc.saveWidgetDraft = func(dev, game string, config json.RawMessage) (service.WidgetDraft, error) {
		gotDev, gotGame, gotConfig = dev, game, string(config)
		saved := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		return service.WidgetDraft{Config: config, UpdatedAt: &saved}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPut, widgetDraftPath,
		`{"config":{"schemaVersion":1,"theme":{"accent":"#8B65EE"}}}`, "good")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotGame != someGameID {
		t.Fatalf("developer must come from the token and the game from the URL, got %q / %q", gotDev, gotGame)
	}
	if gotConfig != `{"schemaVersion":1,"theme":{"accent":"#8B65EE"}}` {
		t.Fatalf("service got config %s", gotConfig)
	}
	want := `{"config":{"schemaVersion":1,"theme":{"accent":"#8B65EE"}},"updatedAt":"2026-11-08T12:00:00Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestSaveWidgetDraft_RejectsBrokenBodies(t *testing.T) {
	svc := authedFake() // saveWidgetDraft is nil: any call to the service would panic
	for _, tt := range []struct{ name, body string }{
		{"not json", `nope`},
		{"empty body", ``},
		{"unknown field", `{"config":{},"gameId":"someone-else"}`},
		{"two documents", `{"config":{}} {"config":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(t, svc, discardLog(), http.MethodPut, widgetDraftPath, tt.body, "good")
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWidgetDraftEndpoints_MapServiceErrors(t *testing.T) {
	notFound := httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
	badConfig := httpx.NewError(http.StatusBadRequest, "invalid_widget_config", "Config must be a JSON object")
	svc := authedFake()
	svc.widgetDraft = func(string, string) (service.WidgetDraft, error) { return service.WidgetDraft{}, notFound }

	rec := call(t, svc, discardLog(), http.MethodGet, widgetDraftPath, "", "good")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Fatalf("GET another developer's game: %d %s", rec.Code, rec.Body.String())
	}

	svc.saveWidgetDraft = func(string, string, json.RawMessage) (service.WidgetDraft, error) {
		return service.WidgetDraft{}, notFound
	}
	rec = call(t, svc, discardLog(), http.MethodPut, widgetDraftPath, `{"config":{}}`, "good")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Fatalf("PUT another developer's game: %d %s", rec.Code, rec.Body.String())
	}

	svc.saveWidgetDraft = func(string, string, json.RawMessage) (service.WidgetDraft, error) {
		return service.WidgetDraft{}, badConfig
	}
	rec = call(t, svc, discardLog(), http.MethodPut, widgetDraftPath, `{"config":[]}`, "good")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_widget_config" {
		t.Fatalf("PUT bad config: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWidgetDraftEndpoints_RequireDeveloperToken(t *testing.T) {
	svc := authedFake() // the widget functions are nil: any call would panic
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, token := range []string{"", "bad"} {
			rec := call(t, svc, discardLog(), method, widgetDraftPath, `{"config":{}}`, token)
			if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
				t.Fatalf("%s with token %q: got %d %s", method, token, rec.Code, rec.Body.String())
			}
		}
	}
}

// TestWidgetDraftFlow runs the draft endpoints through the real handler and service:
// default, save, read back, replace, another developer locked out, bad configs refused.
func TestWidgetDraftFlow(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, signingKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

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
	alice, bob := register("alice@example.com"), register("bob@example.com")

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, alice)
	var game gameResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &game)
	path := "/api/v1/games/" + game.ID + "/widget/draft"

	get := func(token string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), http.MethodGet, path, "", token)
	}
	put := func(token, body string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), http.MethodPut, path, body, token)
	}

	// nothing saved yet: the default config and no time
	if rec := get(alice); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updatedAt":null`) {
		t.Fatalf("default: %d %s", rec.Code, rec.Body.String())
	}

	// save, then read it back
	const first = `{"schemaVersion":1,"theme":{"accent":"#8B65EE"}}`
	if rec := put(alice, `{"config":`+first+`}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var got widgetDraftResponse
	rec = get(alice)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || string(got.Config) != first || got.UpdatedAt == nil {
		t.Fatalf("read back: %d %s", rec.Code, rec.Body.String())
	}

	// a second save replaces the first
	if rec := put(alice, `{"config":{"schemaVersion":1}}`); rec.Code != http.StatusOK {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}
	rec = get(alice)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if string(got.Config) != `{"schemaVersion":1}` {
		t.Fatalf("second save should replace the first, got %s", rec.Body.String())
	}

	// bob cannot read or change alice's draft
	for _, rec := range []*httptest.ResponseRecorder{get(bob), put(bob, `{"config":{"hacked":true}}`)} {
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
			t.Fatalf("bob: want 404 game_not_found, got %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := get(alice); strings.Contains(rec.Body.String(), "hacked") {
		t.Fatalf("bob changed alice's draft: %s", rec.Body.String())
	}

	// configs that are not objects, or are too large, are refused and change nothing
	tooLarge := `{"config":{"a":"` + strings.Repeat("x", 70*1024) + `"}}`
	for _, body := range []string{`{}`, `{"config":null}`, `{"config":[]}`, `{"config":"x"}`, `{"config":1}`, tooLarge} {
		rec := put(alice, body)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_widget_config" {
			t.Fatalf("body %.40s: want 400 invalid_widget_config, got %d %s", body, rec.Code, rec.Body.String())
		}
	}
	rec = get(alice)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if string(got.Config) != `{"schemaVersion":1}` {
		t.Fatalf("refused configs must not change the draft, got %s", rec.Body.String())
	}

	// a game that does not exist, or a malformed ID, is a plain 404
	for _, id := range []string{"00000000-0000-4000-8000-00000000dead", "not-a-uuid"} {
		rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/"+id+"/widget/draft", "", alice)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
			t.Fatalf("game %q: got %d %s", id, rec.Code, rec.Body.String())
		}
	}

	// no token
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d %s", rec.Code, rec.Body.String())
	}
}

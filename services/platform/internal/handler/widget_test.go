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

func (m *memStore) LatestWidgetVersion(_ context.Context, gameID string) (repository.WidgetVersion, error) {
	all := m.versions[gameID]
	if len(all) == 0 {
		return repository.WidgetVersion{}, repository.ErrNotFound
	}
	return all[len(all)-1], nil
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
	first := cfg("Accent")
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
	if rec := put(alice, `{"config":`+cfg("Plain")+`}`); rec.Code != http.StatusOK {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}
	rec = get(alice)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if string(got.Config) != cfg("Plain") {
		t.Fatalf("second save should replace the first, got %s", rec.Body.String())
	}

	// bob cannot read or change alice's draft
	for _, rec := range []*httptest.ResponseRecorder{get(bob), put(bob, `{"config":`+cfg("Hacked")+`}`)} {
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
	if string(got.Config) != cfg("Plain") {
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

func (f fakeService) PublishWidget(_ context.Context, developerID, gameID string) (service.WidgetVersion, error) {
	return f.publishWidget(developerID, gameID)
}

func (f fakeService) WidgetVersions(_ context.Context, developerID, gameID string) ([]service.WidgetVersion, error) {
	return f.widgetVersions(developerID, gameID)
}

func (f fakeService) WidgetVersion(_ context.Context, developerID, gameID string, version int) (service.WidgetVersion, error) {
	return f.widgetVersion(developerID, gameID, version)
}

func (f fakeService) RollbackWidget(_ context.Context, developerID, gameID string, version int) (service.WidgetVersion, error) {
	return f.rollbackWidget(developerID, gameID, version)
}

const widgetVersionsPath = "/api/v1/games/" + someGameID + "/widget/versions"

// publishedAt is a time in another zone: responses must show it in UTC.
func publishedAt() time.Time {
	return time.Date(2026, 11, 8, 16, 0, 0, 0, time.FixedZone("+04", 4*3600))
}

func TestPublishWidget_OwnerPublishesTheDraft(t *testing.T) {
	var gotDev, gotGame string
	svc := authedFake()
	svc.publishWidget = func(dev, game string) (service.WidgetVersion, error) {
		gotDev, gotGame = dev, game
		return service.WidgetVersion{Version: 3, Config: json.RawMessage(`{"schemaVersion":1}`), PublishedAt: publishedAt(), PublishedBy: "d1"}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/"+someGameID+"/widget/publish", "", "good")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotGame != someGameID {
		t.Fatalf("developer must come from the token and the game from the URL, got %q / %q", gotDev, gotGame)
	}
	want := `{"version":3,"publishedAt":"2026-11-08T12:00:00Z","publishedBy":"d1","config":{"schemaVersion":1}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestListWidgetVersions_NoConfigs(t *testing.T) {
	svc := authedFake()
	svc.widgetVersions = func(string, string) ([]service.WidgetVersion, error) {
		return []service.WidgetVersion{
			{Version: 2, PublishedAt: publishedAt(), PublishedBy: "d1"},
			{Version: 1, PublishedAt: publishedAt(), PublishedBy: "d1"},
		}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, widgetVersionsPath, "", "good")

	want := `{"versions":[{"version":2,"publishedAt":"2026-11-08T12:00:00Z","publishedBy":"d1"},` +
		`{"version":1,"publishedAt":"2026-11-08T12:00:00Z","publishedBy":"d1"}]}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListWidgetVersions_EmptyIsAnEmptyArray(t *testing.T) {
	svc := authedFake()
	svc.widgetVersions = func(string, string) ([]service.WidgetVersion, error) { return nil, nil }

	rec := call(t, svc, discardLog(), http.MethodGet, widgetVersionsPath, "", "good")

	if rec.Code != http.StatusOK || rec.Body.String() != `{"versions":[]}`+"\n" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetWidgetVersion_ReadsTheNumberFromTheURL(t *testing.T) {
	var gotGame string
	var gotNumber int
	svc := authedFake()
	svc.widgetVersion = func(_, game string, n int) (service.WidgetVersion, error) {
		gotGame, gotNumber = game, n
		return service.WidgetVersion{Version: n, Config: json.RawMessage(`{"a":1}`), PublishedAt: publishedAt(), PublishedBy: "d1"}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, widgetVersionsPath+"/12", "", "good")

	want := `{"version":12,"publishedAt":"2026-11-08T12:00:00Z","publishedBy":"d1","config":{"a":1}}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if gotGame != someGameID || gotNumber != 12 {
		t.Fatalf("service got game %q version %d", gotGame, gotNumber)
	}
}

func TestRollbackWidget_PublishesAnOldVersionAgain(t *testing.T) {
	var gotDev string
	var gotNumber int
	svc := authedFake()
	svc.rollbackWidget = func(dev, _ string, n int) (service.WidgetVersion, error) {
		gotDev, gotNumber = dev, n
		return service.WidgetVersion{Version: 5, Config: json.RawMessage(`{"a":1}`), PublishedAt: publishedAt(), PublishedBy: dev}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPost, widgetVersionsPath+"/2/rollback", "", "good")

	want := `{"version":5,"publishedAt":"2026-11-08T12:00:00Z","publishedBy":"d1","config":{"a":1}}` + "\n"
	if rec.Code != http.StatusCreated || rec.Body.String() != want {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotNumber != 2 {
		t.Fatalf("service got developer %q version %d", gotDev, gotNumber)
	}
}

// A version that is not a positive whole number does not exist; the service is never called.
func TestWidgetVersionNumber_MustBeAPositiveWholeNumber(t *testing.T) {
	svc := authedFake() // the version functions are nil: any call would panic
	for _, n := range []string{"abc", "0", "-1", "+1", "1.5", "01", "1e3", "99999999999", "%20"} {
		for _, tt := range []struct{ method, path string }{
			{http.MethodGet, widgetVersionsPath + "/" + n},
			{http.MethodPost, widgetVersionsPath + "/" + n + "/rollback"},
		} {
			rec := call(t, svc, discardLog(), tt.method, tt.path, "", "good")
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_version_not_found" {
				t.Fatalf("%s %s: got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestWidgetPublishEndpoints_MapServiceErrors(t *testing.T) {
	noGame := httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
	noVersion := httpx.NewError(http.StatusNotFound, "widget_version_not_found", "Widget version not found")
	noDraft := httpx.NewError(http.StatusConflict, "widget_draft_missing", "Save a draft before publishing")
	svc := authedFake()
	one := func() (service.WidgetVersion, error) { return service.WidgetVersion{}, noGame }
	svc.publishWidget = func(string, string) (service.WidgetVersion, error) { return one() }
	svc.widgetVersions = func(string, string) ([]service.WidgetVersion, error) { return nil, noGame }
	svc.widgetVersion = func(string, string, int) (service.WidgetVersion, error) { return one() }
	svc.rollbackWidget = func(string, string, int) (service.WidgetVersion, error) { return one() }

	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/games/" + someGameID + "/widget/publish"},
		{http.MethodGet, widgetVersionsPath},
		{http.MethodGet, widgetVersionsPath + "/1"},
		{http.MethodPost, widgetVersionsPath + "/1/rollback"},
	} {
		rec := call(t, svc, discardLog(), tt.method, tt.path, "", "good")
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
			t.Fatalf("%s %s: got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}

	svc.publishWidget = func(string, string) (service.WidgetVersion, error) { return service.WidgetVersion{}, noDraft }
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/"+someGameID+"/widget/publish", "", "good")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "widget_draft_missing" {
		t.Fatalf("publish without a draft: %d %s", rec.Code, rec.Body.String())
	}

	svc.widgetVersion = func(string, string, int) (service.WidgetVersion, error) { return service.WidgetVersion{}, noVersion }
	svc.rollbackWidget = func(string, string, int) (service.WidgetVersion, error) { return service.WidgetVersion{}, noVersion }
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, widgetVersionsPath + "/7"},
		{http.MethodPost, widgetVersionsPath + "/7/rollback"},
	} {
		rec := call(t, svc, discardLog(), tt.method, tt.path, "", "good")
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_version_not_found" {
			t.Fatalf("%s %s: got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestWidgetPublishEndpoints_RequireDeveloperToken(t *testing.T) {
	svc := authedFake() // the widget functions are nil: any call would panic
	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/games/" + someGameID + "/widget/publish"},
		{http.MethodGet, widgetVersionsPath},
		{http.MethodGet, widgetVersionsPath + "/1"},
		{http.MethodPost, widgetVersionsPath + "/1/rollback"},
	} {
		for _, token := range []string{"", "bad"} {
			rec := call(t, svc, discardLog(), tt.method, tt.path, "", token)
			if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
				t.Fatalf("%s %s token %q: got %d %s", tt.method, tt.path, token, rec.Code, rec.Body.String())
			}
		}
	}
}

// TestWidgetPublishFlow runs draft -> publish -> edit -> publish -> list -> read -> roll back
// through the real handler and service.
func TestWidgetPublishFlow(t *testing.T) {
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
	newGameOf := func(token string) string {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, token)
		var g gameResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &g)
		return g.ID
	}
	gameID := newGameOf(alice)
	base := "/api/v1/games/" + gameID + "/widget"
	do := func(token, method, path, body string) *httptest.ResponseRecorder {
		return call(t, svc, discardLog(), method, base+path, body, token)
	}
	versionNumbers := func(token string) []int {
		t.Helper()
		rec := do(token, http.MethodGet, "/versions", "")
		var l widgetVersionListResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || strings.Contains(rec.Body.String(), `"config"`) {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out []int
		for _, v := range l.Versions {
			out = append(out, v.Version)
		}
		return out
	}

	// nothing to publish yet; the list is empty
	if rec := do(alice, http.MethodPost, "/publish", ""); rec.Code != http.StatusConflict || errorCode(t, rec) != "widget_draft_missing" {
		t.Fatalf("publish without draft: %d %s", rec.Code, rec.Body.String())
	}
	if got := versionNumbers(alice); len(got) != 0 {
		t.Fatalf("want no versions, got %v", got)
	}

	// draft A -> version 1
	if rec := do(alice, http.MethodPut, "/draft", `{"config":`+cfg("LookA")+`}`); rec.Code != http.StatusOK {
		t.Fatalf("save A: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(alice, http.MethodPost, "/publish", "")
	var v widgetVersionResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusCreated || v.Version != 1 || string(v.Config) != cfg("LookA") {
		t.Fatalf("publish 1: %d %s", rec.Code, rec.Body.String())
	}

	// draft B -> version 2
	do(alice, http.MethodPut, "/draft", `{"config":`+cfg("LookB")+`}`)
	if rec := do(alice, http.MethodPost, "/publish", ""); rec.Code != http.StatusCreated {
		t.Fatalf("publish 2: %d %s", rec.Code, rec.Body.String())
	}
	if got := versionNumbers(alice); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("want [2 1], got %v", got)
	}

	// version 1 still has A
	rec = do(alice, http.MethodGet, "/versions/1", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusOK || string(v.Config) != cfg("LookA") {
		t.Fatalf("version 1: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(alice, http.MethodGet, "/versions/3", ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_version_not_found" {
		t.Fatalf("version 3 does not exist yet: %d %s", rec.Code, rec.Body.String())
	}

	// roll back to 1: a new version 3 with A; the draft stays B
	rec = do(alice, http.MethodPost, "/versions/1/rollback", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusCreated || v.Version != 3 || string(v.Config) != cfg("LookA") {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	if got := versionNumbers(alice); len(got) != 3 || got[0] != 3 {
		t.Fatalf("want 3 versions, newest 3, got %v", got)
	}
	var d widgetDraftResponse
	_ = json.Unmarshal(do(alice, http.MethodGet, "/draft", "").Body.Bytes(), &d)
	if string(d.Config) != cfg("LookB") {
		t.Fatalf("rollback must not touch the draft, got %s", d.Config)
	}

	// bob is locked out of every endpoint, and changes nothing
	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/publish"}, {http.MethodGet, "/versions"},
		{http.MethodGet, "/versions/1"}, {http.MethodPost, "/versions/1/rollback"},
	} {
		if rec := do(bob, tt.method, tt.path, ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
			t.Fatalf("bob %s %s: want 404 game_not_found, got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}
	if got := versionNumbers(alice); len(got) != 3 {
		t.Fatalf("bob must not add versions, got %v", got)
	}
}

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
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

func (f fakeService) PublicWidgetConfig(_ context.Context, gameID string) (service.WidgetVersion, error) {
	return f.publicWidgetConfig(gameID)
}

const publicConfigPath = "/api/v1/public/games/" + someGameID + "/widget-config"

// getPublic calls the public endpoint with no login, and with an If-None-Match header if given.
func getPublic(t *testing.T, svc Service, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, discardLog()).Register(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func publishedFake(version int, config string) fakeService {
	return fakeService{publicWidgetConfig: func(string) (service.WidgetVersion, error) {
		return service.WidgetVersion{Version: version, Config: json.RawMessage(config), PublishedAt: publishedAt(), PublishedBy: "secret-developer-id"}, nil
	}}
}

func TestPublicWidgetConfig_NoLoginNeededAndOnlyConfigAndVersion(t *testing.T) {
	var gotGame string
	svc := fakeService{publicWidgetConfig: func(game string) (service.WidgetVersion, error) {
		gotGame = game
		return service.WidgetVersion{Version: 4, Config: json.RawMessage(`{"schemaVersion":1}`), PublishedAt: publishedAt(), PublishedBy: "secret-developer-id"}, nil
	}}

	rec := getPublic(t, svc, publicConfigPath, "")
	if rec.Code != http.StatusOK || gotGame != someGameID {
		t.Fatalf("status %d, game %q: %s", rec.Code, gotGame, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || string(body["version"]) != "4" || string(body["config"]) != `{"schemaVersion":1}` {
		t.Fatalf("want only version and config, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-developer-id") || strings.Contains(rec.Body.String(), "publishedAt") {
		t.Fatalf("who published and when must stay private: %s", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"v4"` {
		t.Fatalf("ETag %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatalf("Cache-Control %q", got)
	}
}

func TestPublicWidgetConfig_NotModified(t *testing.T) {
	svc := publishedFake(4, `{"a":1}`)
	for _, tt := range []struct {
		name, header string
		want         int
	}{
		{"same version", `"v4"`, http.StatusNotModified},
		{"weak form of the same version", `W/"v4"`, http.StatusNotModified},
		{"in a list", `"v1", "v4"`, http.StatusNotModified},
		{"star", `*`, http.StatusNotModified},
		{"older version", `"v3"`, http.StatusOK},
		{"newer version", `"v5"`, http.StatusOK},
		{"without quotes", `v4`, http.StatusOK},
		{"empty", ``, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := getPublic(t, svc, publicConfigPath, tt.header)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
			if rec.Header().Get("ETag") != `"v4"` || rec.Header().Get("Cache-Control") != "public, max-age=60" {
				t.Fatalf("headers must be sent on every answer: %v", rec.Header())
			}
			if tt.want == http.StatusNotModified && rec.Body.Len() != 0 {
				t.Fatalf("a 304 has no body, got %q", rec.Body.String())
			}
		})
	}
}

func TestPublicWidgetConfig_NotPublishedIs404AndNotCached(t *testing.T) {
	svc := fakeService{publicWidgetConfig: func(string) (service.WidgetVersion, error) {
		return service.WidgetVersion{}, httpx.NewError(http.StatusNotFound, "widget_not_published", "This game has no published widget")
	}}
	rec := getPublic(t, svc, publicConfigPath, `"v1"`)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_not_published" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("ETag") != "" {
		t.Fatalf("an error must not be cached or tagged: %v", rec.Header())
	}
}

func TestPublicWidgetConfig_ServerErrorHidesDetails(t *testing.T) {
	svc := fakeService{publicWidgetConfig: func(string) (service.WidgetVersion, error) {
		return service.WidgetVersion{}, context.DeadlineExceeded
	}}
	rec := getPublic(t, svc, publicConfigPath, "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestPublicWidgetConfig_OnlyGET(t *testing.T) {
	mux := http.NewServeMux()
	New("test", publishedFake(1, `{}`), discardLog()).Register(mux)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, publicConfigPath, strings.NewReader("{}")))
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
			t.Fatalf("%s must not work, got %d", m, rec.Code)
		}
	}
}

func TestEtagMatches(t *testing.T) {
	for header, want := range map[string]bool{
		`"v2"`: true, `W/"v2"`: true, `"v1","v2"`: true, ` "v1" , "v2" `: true, `*`: true,
		`"v20"`: false, `"v1"`: false, `v2`: false, ``: false, `"V2"`: false,
	} {
		if got := etagMatches(header, `"v2"`); got != want {
			t.Errorf("etagMatches(%q) = %v, want %v", header, got, want)
		}
	}
}

// TestPublicWidgetFlow runs the real service on an in-memory store: what players see follows
// publishes and rollbacks, and never follows the draft.
func TestPublicWidgetFlow(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, signingKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	creds := `"email":"alice@example.com","password":"correct horse"`
	call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, "")
	var l loginResponse
	_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "").Body.Bytes(), &l)
	var g gameResponse
	_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, l.Token).Body.Bytes(), &g)
	base := "/api/v1/games/" + g.ID + "/widget"
	dev := func(method, path, body string) {
		t.Helper()
		if rec := call(t, svc, discardLog(), method, base+path, body, l.Token); rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	public := "/api/v1/public/games/" + g.ID + "/widget-config"
	players := func() (int, publicWidgetConfigResponse, string) {
		rec := getPublic(t, svc, public, "")
		var p publicWidgetConfigResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		return rec.Code, p, rec.Header().Get("ETag")
	}

	// nothing published, even with a draft saved
	if rec := getPublic(t, svc, public, ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_not_published" {
		t.Fatalf("before publish: %d %s", rec.Code, rec.Body.String())
	}
	dev(http.MethodPut, "/draft", `{"config":`+cfg("LookA")+`}`)
	if rec := getPublic(t, svc, public, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a draft must stay hidden, got %d %s", rec.Code, rec.Body.String())
	}

	dev(http.MethodPost, "/publish", "")
	code, p, tag1 := players()
	if code != http.StatusOK || p.Version != 1 || string(p.Config) != cfg("LookA") || tag1 != `"v1"` {
		t.Fatalf("after publish 1: %d %+v %s", code, p, tag1)
	}

	// editing the draft changes nothing for players, and the ETag stays: 304
	dev(http.MethodPut, "/draft", `{"config":`+cfg("LookB")+`}`)
	code, p, tag := players()
	if code != http.StatusOK || p.Version != 1 || tag != tag1 {
		t.Fatalf("draft edit leaked: %d %+v %s", code, p, tag)
	}
	if rec := getPublic(t, svc, public, tag1); rec.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", rec.Code)
	}

	// publish B: new version, new ETag, the old ETag no longer matches
	dev(http.MethodPost, "/publish", "")
	code, p, tag2 := players()
	if code != http.StatusOK || p.Version != 2 || tag2 == tag1 {
		t.Fatalf("after publish 2: %d %+v %s", code, p, tag2)
	}
	if rec := getPublic(t, svc, public, tag1); rec.Code != http.StatusOK {
		t.Fatalf("old ETag must not match, got %d", rec.Code)
	}

	// rollback to 1 is a new version 3 with the old config
	dev(http.MethodPost, "/versions/1/rollback", "")
	code, p, tag3 := players()
	if code != http.StatusOK || p.Version != 3 || string(p.Config) != cfg("LookA") || tag3 == tag2 {
		t.Fatalf("after rollback: %d %+v %s", code, p, tag3)
	}

	// another game's ID, and a made-up one, look the same: not published
	for _, id := range []string{"00000000-0000-4000-8000-00000000dead", "nope"} {
		if rec := getPublic(t, svc, "/api/v1/public/games/"+id+"/widget-config", ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "widget_not_published" {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
}

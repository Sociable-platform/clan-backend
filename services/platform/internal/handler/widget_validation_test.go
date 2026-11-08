package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// invalidConfigBody is the JSON a Studio client would see in a 400 invalid_widget_config.
type invalidConfigBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Path string `json:"path"`
			Rule string `json:"rule"`
		} `json:"details"`
	} `json:"error"`
}

func TestWidgetDraft_InvalidConfigAnswersWithFieldLevelDetails(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, signingKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	creds := `"email":"dev@example.com","password":"correct horse"`
	call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, "")
	var l loginResponse
	_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "").Body.Bytes(), &l)
	var game gameResponse
	_ = json.Unmarshal(call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, l.Token).Body.Bytes(), &game)
	path := "/api/v1/games/" + game.ID + "/widget/draft"

	put := func(config string) (*invalidConfigBody, int, string) {
		rec := call(t, svc, discardLog(), http.MethodPut, path, `{"config":`+config+`}`, l.Token)
		var b invalidConfigBody
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return &b, rec.Code, rec.Header().Get("Cache-Control")
	}

	t.Run("the shared invalid fixtures", func(t *testing.T) {
		dir := "../widgetconfig/testdata/examples"
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Cases []struct {
				File   string `json:"file"`
				Valid  bool   `json:"valid"`
				Errors []struct{ Path, Rule string }
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for _, c := range m.Cases {
			doc, err := os.ReadFile(filepath.Join(dir, c.File))
			if err != nil {
				t.Fatal(err)
			}
			b, code, _ := put(string(doc))
			if c.Valid {
				if code != http.StatusOK {
					t.Errorf("%s: valid fixture got %d", c.File, code)
				}
				continue
			}
			if code != http.StatusBadRequest || b.Error.Code != "invalid_widget_config" {
				t.Errorf("%s: want 400 invalid_widget_config, got %d %s", c.File, code, b.Error.Code)
				continue
			}
			for _, want := range c.Errors {
				found := false
				for _, d := range b.Error.Details {
					found = found || (d.Path == want.Path && d.Rule == want.Rule)
				}
				if !found {
					t.Errorf("%s: response lacks %q %q: %+v", c.File, want.Path, want.Rule, b.Error.Details)
				}
			}
		}
	})

	t.Run("oversized and malformed are one issue each", func(t *testing.T) {
		// (whitespace around the document is not part of the config, so make real content big)
		big := strings.Replace(cfg("Big"), `"labels":{}`, `"labels":{"x":"`+strings.Repeat("a", 70*1024)+`"}`, 1)
		b, code, _ := put(big)
		if code != http.StatusBadRequest || len(b.Error.Details) != 1 || b.Error.Details[0].Rule != "size" {
			t.Fatalf("oversized: %d %+v", code, b.Error)
		}
		// a body that is not even JSON never reaches the config check: plain invalid_request
		rec := call(t, svc, discardLog(), http.MethodPut, path, `{"config":`, l.Token)
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), `"details"`) {
			t.Fatalf("broken body: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("errors from other endpoints have no details key", func(t *testing.T) {
		rec := call(t, svc, discardLog(), http.MethodGet, path, "", "bad-token")
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), `"details"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}

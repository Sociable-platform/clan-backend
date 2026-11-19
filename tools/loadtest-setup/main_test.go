package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlatform is the part of Platform the tool talks to, in memory.
type fakePlatform struct {
	mu           sync.Mutex
	verification bool              // registration answers 202, as with email verification on
	developers   map[string]string // email -> password
	games        map[string]fakeGame
	keys         map[string]*fakeKey
	nextID       int
	published    map[string]map[string]any // game id -> last published config
	versions     map[string]int
}

type fakeGame struct {
	id, name, owner string
}

type fakeKey struct {
	id, gameID, key string
	revoked         bool
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		developers: map[string]string{},
		games:      map[string]fakeGame{},
		keys:       map[string]*fakeKey{},
		published:  map[string]map[string]any{},
		versions:   map[string]int{},
	}
}

func (f *fakePlatform) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakePlatform) handler() http.Handler {
	mux := http.NewServeMux()
	developer := func(r *http.Request) string {
		return strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "tok-")
	}
	ownGame := func(w http.ResponseWriter, r *http.Request) (fakeGame, bool) {
		g, ok := f.games[r.PathValue("id")]
		if !ok || g.owner != developer(r) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "game_not_found"}})
			return fakeGame{}, false
		}
		return g, true
	}

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /api/v1/developers/register", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.verification {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_required"})
			return
		}
		f.developers[body["email"]] = body["password"]
		writeJSON(w, http.StatusCreated, map[string]string{"email": body["email"]})
	})
	mux.HandleFunc("POST /api/v1/developers/login", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if pw, ok := f.developers[body["email"]]; !ok || pw != body["password"] {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "invalid_credentials"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": "tok-" + body["email"]})
	})
	mux.HandleFunc("GET /api/v1/games", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		games := []map[string]string{}
		for _, g := range f.games {
			if g.owner == developer(r) {
				games = append(games, map[string]string{"id": g.id, "name": g.name, "role": "owner"})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"games": games})
	})
	mux.HandleFunc("POST /api/v1/games", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		g := fakeGame{id: f.id("game"), name: body["name"], owner: developer(r)}
		f.games[g.id] = g
		writeJSON(w, http.StatusCreated, map[string]string{"id": g.id, "name": g.name})
	})
	mux.HandleFunc("PUT /api/v1/games/{id}/widget/draft", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		g, ok := ownGame(w, r)
		if !ok {
			return
		}
		var body struct {
			Config map[string]any `json:"config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Config["game_id"] != g.id {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "game_id_mismatch"}})
			return
		}
		f.published[g.id] = body.Config
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	mux.HandleFunc("POST /api/v1/games/{id}/widget/publish", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		g, ok := ownGame(w, r)
		if !ok {
			return
		}
		f.versions[g.id]++
		writeJSON(w, http.StatusCreated, map[string]int{"version": f.versions[g.id]})
	})
	mux.HandleFunc("GET /api/v1/games/{id}/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		g, ok := ownGame(w, r)
		if !ok {
			return
		}
		list := []map[string]any{}
		for _, k := range f.keys {
			if k.gameID == g.id {
				var revokedAt any
				if k.revoked {
					revokedAt = "2026-11-19T10:00:00Z"
				}
				list = append(list, map[string]any{"id": k.id, "gameId": g.id, "keyPrefix": "clk_x", "revokedAt": revokedAt})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"apiKeys": list})
	})
	mux.HandleFunc("POST /api/v1/games/{id}/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		g, ok := ownGame(w, r)
		if !ok {
			return
		}
		k := &fakeKey{id: f.id("key"), gameID: g.id}
		k.key = "clk_secret_" + k.id
		f.keys[k.id] = k
		writeJSON(w, http.StatusCreated, map[string]any{"id": k.id, "gameId": g.id, "keyPrefix": "clk_x", "key": k.key, "revokedAt": nil})
	})
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		k, ok := f.keys[r.PathValue("id")]
		if !ok || f.games[k.gameID].owner != developer(r) {
			writeJSON(w, http.StatusNotFound, map[string]any{})
			return
		}
		k.revoked = true
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (f *fakePlatform) activeKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for _, k := range f.keys {
		if !k.revoked {
			keys = append(keys, k.key)
		}
	}
	return keys
}

// cleanEnv clears the variables the tool reads, so the developer's own shell does not leak in.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"LOADTEST_PLATFORM_URL", "LOADTEST_DEV_EMAIL", "LOADTEST_DEV_PASSWORD"} {
		t.Setenv(name, "")
	}
}

// readSettings parses a .ps1 settings file back into its variables.
func readSettings(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, " = '"); ok && strings.HasPrefix(name, "$env:") {
			vars[strings.TrimPrefix(name, "$env:")] = strings.ReplaceAll(strings.TrimSuffix(value, "'"), "''", "'")
		}
	}
	return vars
}

func runTool(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, strings.NewReader(""), &out)
	return out.String(), err
}

func TestLocalFirstRunRegistersPublishesAndWritesSettings(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "local.ps1")

	log, err := runTool(t, "-platform", srv.URL, "-social", "http://social.test", "-realtime", "http://realtime.test", "-out", out)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}

	vars := readSettings(t, out)
	if vars["LOADTEST_PLATFORM_URL"] != srv.URL || vars["LOADTEST_SOCIAL_URL"] != "http://social.test" || vars["LOADTEST_REALTIME_URL"] != "http://realtime.test" {
		t.Errorf("addresses = %v", vars)
	}
	if len(fake.games) != 1 {
		t.Fatalf("games = %d, want 1", len(fake.games))
	}
	var gameID string
	for id, g := range fake.games {
		gameID = id
		if g.name != defaultGameName {
			t.Errorf("game name = %q", g.name)
		}
	}
	if vars["LOADTEST_GAME_ID"] != gameID {
		t.Errorf("LOADTEST_GAME_ID = %q, want %q", vars["LOADTEST_GAME_ID"], gameID)
	}
	if active := fake.activeKeys(); len(active) != 1 || vars["LOADTEST_API_KEY"] != active[0] {
		t.Errorf("LOADTEST_API_KEY = %q, active keys %v", vars["LOADTEST_API_KEY"], active)
	}
	if !isThrowaway(vars["LOADTEST_DEV_EMAIL"]) || vars["LOADTEST_DEV_PASSWORD"] != fake.developers[vars["LOADTEST_DEV_EMAIL"]] {
		t.Errorf("throwaway developer not written: %q", vars["LOADTEST_DEV_EMAIL"])
	}

	cfg := fake.published[gameID]
	clan := cfg["clan"].(map[string]any)
	chat := cfg["chat"].(map[string]any)
	if clan["enabled"] != true || clan["maxMembers"] != float64(20) || chat["clanChat"] != true || chat["slowModeSeconds"] != float64(0) {
		t.Errorf("published config: clan %v, chat %v", clan, chat)
	}
	if fake.versions[gameID] != 1 {
		t.Errorf("published versions = %d, want 1", fake.versions[gameID])
	}
	if strings.Contains(log, vars["LOADTEST_API_KEY"]) || strings.Contains(log, vars["LOADTEST_DEV_PASSWORD"]) {
		t.Errorf("the output shows a secret:\n%s", log)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("settings file mode = %o, want 600", mode)
		}
	}
}

func TestSecondRunReusesDeveloperAndGameAndKeepsOneKey(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "local.ps1")
	args := []string{"-platform", srv.URL, "-social", "http://s", "-realtime", "http://r", "-out", out}

	if log, err := runTool(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, log)
	}
	first := readSettings(t, out)

	// What loading the file does: the next run signs in as the same developer.
	t.Setenv("LOADTEST_DEV_EMAIL", first["LOADTEST_DEV_EMAIL"])
	t.Setenv("LOADTEST_DEV_PASSWORD", first["LOADTEST_DEV_PASSWORD"])
	if log, err := runTool(t, args...); err != nil {
		t.Fatalf("second run: %v\n%s", err, log)
	}
	second := readSettings(t, out)

	if len(fake.developers) != 1 || len(fake.games) != 1 {
		t.Errorf("developers %d, games %d: want both reused", len(fake.developers), len(fake.games))
	}
	if second["LOADTEST_GAME_ID"] != first["LOADTEST_GAME_ID"] {
		t.Errorf("game changed: %q -> %q", first["LOADTEST_GAME_ID"], second["LOADTEST_GAME_ID"])
	}
	if second["LOADTEST_DEV_EMAIL"] != first["LOADTEST_DEV_EMAIL"] || second["LOADTEST_DEV_PASSWORD"] == "" {
		t.Errorf("the throwaway developer's sign-in was not kept for a third run")
	}
	if active := fake.activeKeys(); len(active) != 1 || active[0] != second["LOADTEST_API_KEY"] || second["LOADTEST_API_KEY"] == first["LOADTEST_API_KEY"] {
		t.Errorf("active keys %v, file has %q: want only the new key", active, second["LOADTEST_API_KEY"])
	}
}

func TestRealDeveloperPasswordIsNeverWritten(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	fake.developers["lead@studio.test"] = "real-password-1"
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "server.ps1")
	t.Setenv("LOADTEST_DEV_PASSWORD", "real-password-1")

	if log, err := runTool(t, "-platform", srv.URL, "-social", "http://s", "-realtime", "http://r", "-email", "lead@studio.test", "-out", out); err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}

	data, _ := os.ReadFile(out)
	if strings.Contains(string(data), "real-password-1") || strings.Contains(string(data), "LOADTEST_DEV_") {
		t.Errorf("the settings file holds the developer's sign-in:\n%s", data)
	}
	if readSettings(t, out)["LOADTEST_API_KEY"] == "" {
		t.Error("no API key written")
	}
}

func TestPasswordFromStandardInput(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	fake.developers["lead@studio.test"] = "typed-password"
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	var out bytes.Buffer
	args := []string{"-platform", srv.URL, "-social", "http://s", "-realtime", "http://r", "-email", "lead@studio.test", "-out", filepath.Join(t.TempDir(), "s.ps1")}
	if err := run(args, strings.NewReader("typed-password\r\n"), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
}

func TestEmailVerificationOnExplainsWhatToDo(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	fake.verification = true
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "s.ps1")

	_, err := runTool(t, "-platform", srv.URL, "-social", "http://s", "-realtime", "http://r", "-out", out)

	if err == nil || !strings.Contains(err.Error(), "email verification on") || !strings.Contains(err.Error(), "-email") {
		t.Fatalf("err = %v, want an explanation that names -email", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("a settings file was written after a failed run")
	}
}

func TestTeardownRevokesEveryKeyAndDeletesTheFile(t *testing.T) {
	cleanEnv(t)
	fake := newFakePlatform()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "local.ps1")
	args := []string{"-platform", srv.URL, "-social", "http://s", "-realtime", "http://r", "-out", out}
	if log, err := runTool(t, args...); err != nil {
		t.Fatalf("setup: %v\n%s", err, log)
	}
	vars := readSettings(t, out)
	t.Setenv("LOADTEST_DEV_EMAIL", vars["LOADTEST_DEV_EMAIL"])
	t.Setenv("LOADTEST_DEV_PASSWORD", vars["LOADTEST_DEV_PASSWORD"])

	log, err := runTool(t, append(args, "-teardown")...)
	if err != nil {
		t.Fatalf("teardown: %v\n%s", err, log)
	}

	if active := fake.activeKeys(); len(active) != 0 {
		t.Errorf("active keys after teardown: %v", active)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("the settings file is still there")
	}
}

func TestTeardownNeedsTheDeveloper(t *testing.T) {
	cleanEnv(t)
	_, err := runTool(t, "-platform", "http://localhost:8081", "-out", filepath.Join(t.TempDir(), "s.ps1"), "-teardown")
	if err == nil || !strings.Contains(err.Error(), "-email") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusesASettingsFileInsideTheRepository(t *testing.T) {
	cleanEnv(t)
	_, err := runTool(t, "-platform", "http://localhost:8081", "-out", "settings.ps1")
	if err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestPasswordIsNeverAnArgument(t *testing.T) {
	cleanEnv(t)
	_, err := runTool(t, "-platform", "http://localhost:8081", "-out", filepath.Join(t.TempDir(), "s.ps1"), "hunter22")
	if err == nil || !strings.Contains(err.Error(), "LOADTEST_DEV_PASSWORD") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceURLs(t *testing.T) {
	for _, tc := range []struct{ platform, social, realtime string }{
		{"https://platform.130-61-158-79.sslip.io", "https://social.130-61-158-79.sslip.io", "https://realtime.130-61-158-79.sslip.io"},
		{"http://localhost:8081", "http://localhost:8082", "http://localhost:8083"},
	} {
		social, realtime, err := serviceURLs(tc.platform)
		if err != nil || social != tc.social || realtime != tc.realtime {
			t.Errorf("serviceURLs(%q) = %q, %q, %v", tc.platform, social, realtime, err)
		}
	}
	if _, _, err := serviceURLs("https://api.example.com"); err == nil {
		t.Error("an address it cannot read gave no error")
	}
}

func TestSettingsFileQuotesValues(t *testing.T) {
	now := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	vars := [][2]string{{"LOADTEST_API_KEY", "it's $x"}}

	ps := settingsFile("s.ps1", vars, now)
	if !strings.Contains(ps, "$env:LOADTEST_API_KEY = 'it''s $x'") {
		t.Errorf("PowerShell:\n%s", ps)
	}
	sh := settingsFile("s.sh", vars, now)
	if !strings.Contains(sh, `export LOADTEST_API_KEY='it'\''s $x'`) {
		t.Errorf("shell:\n%s", sh)
	}
}

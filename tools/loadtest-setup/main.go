// Command loadtest-setup prepares the test game that the load tests in loadtest/ play in, and
// writes the settings the k6 scripts read into a file outside the repository.
//
// One run does what a person would otherwise do by hand in Studio:
//
//  1. signs in as the load-test developer (or, on a local stack, registers a throwaway one);
//  2. finds the game named "Load test", or registers it;
//  3. saves and publishes its widget config, with clans and clan chat on, clans of up to 20 and
//     no slow mode, which is what the scenarios need;
//  4. creates an API key and revokes the game's older keys, so the test game has one working key;
//  5. writes LOADTEST_PLATFORM_URL, LOADTEST_SOCIAL_URL, LOADTEST_REALTIME_URL, LOADTEST_GAME_ID
//     and LOADTEST_API_KEY into a file in the home folder, to be loaded into the shell.
//
// Player tokens are not made here: they last an hour, so each scenario signs its players in when
// it starts (loadtest/lib/seed.js).
//
// Local stack (email verification off): a developer is registered with a random password. The
// file also holds that developer's address and password, so the next run signs in as the same
// developer and reuses the same game and its seeded players:
//
//	go run ./tools/loadtest-setup
//	. ~\.clan-loadtest\localhost-8081.ps1
//
// Server (email verification on, so nothing can be registered from a script): register a
// developer for load tests in Studio once, then
//
//	$env:LOADTEST_DEV_PASSWORD = Read-Host "Load-test developer password" -MaskInput
//	go run ./tools/loadtest-setup -platform https://platform.<host> -email <that developer>
//
// The password is read from LOADTEST_DEV_PASSWORD, or from the first line of standard input. It
// is never taken as a flag, so it stays out of the shell's history.
//
// When the tests are done, -teardown revokes every API key of the test game and deletes the file,
// so nothing that was written down still works.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultGameName = "Load test"
	// maxResponseBody caps what the tool reads from one HTTP answer.
	maxResponseBody = 1 << 20
	// runLimit is the longest a whole run may take.
	runLimit = 2 * time.Minute
)

// templateConfig is a complete, valid widget config with what the scenarios need switched on:
// clans (up to 20 members), clan chat and no slow mode. Only game_id is set per game.
//
//go:embed config.json
var templateConfig []byte

// options are the settings of one run, from flags and the environment.
type options struct {
	platformURL string
	socialURL   string
	realtimeURL string
	email       string
	password    string
	gameName    string
	outPath     string
	teardown    bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "loadtest-setup: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	opts, err := parseOptions(args, stdin)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, runLimit)
	defer cancel()

	s := &setup{opts: opts, api: &apiClient{http: &http.Client{Timeout: 20 * time.Second}, base: opts.platformURL}, out: stdout}
	if opts.teardown {
		return s.tearDown(ctx)
	}
	return s.prepare(ctx)
}

// parseOptions reads the flags, fills in what follows from them, and checks the output file's place.
func parseOptions(args []string, stdin io.Reader) (options, error) {
	opts := options{}
	fs := flag.NewFlagSet("loadtest-setup", flag.ContinueOnError)
	fs.StringVar(&opts.platformURL, "platform", envOr("LOADTEST_PLATFORM_URL", "http://localhost:8081"), "base URL of the platform service")
	fs.StringVar(&opts.socialURL, "social", "", "base URL of the social service (default: derived from -platform)")
	fs.StringVar(&opts.realtimeURL, "realtime", "", "base URL of the realtime service (default: derived from -platform)")
	fs.StringVar(&opts.email, "email", os.Getenv("LOADTEST_DEV_EMAIL"), "the load-test developer's email; empty: register a throwaway developer (local stack only)")
	fs.StringVar(&opts.gameName, "game", defaultGameName, "name of the test game")
	fs.StringVar(&opts.outPath, "out", "", "file to write the settings to (default: ~/.clan-loadtest/<platform host>.ps1 on Windows, .sh elsewhere)")
	fs.BoolVar(&opts.teardown, "teardown", false, "revoke every API key of the test game and delete the settings file")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q (the password is never a flag: set LOADTEST_DEV_PASSWORD)", fs.Arg(0))
	}

	opts.platformURL = strings.TrimRight(opts.platformURL, "/")
	social, realtime, err := serviceURLs(opts.platformURL)
	if opts.socialURL == "" || opts.realtimeURL == "" {
		if err != nil {
			return options{}, err
		}
	}
	if opts.socialURL == "" {
		opts.socialURL = social
	}
	if opts.realtimeURL == "" {
		opts.realtimeURL = realtime
	}
	opts.socialURL = strings.TrimRight(opts.socialURL, "/")
	opts.realtimeURL = strings.TrimRight(opts.realtimeURL, "/")

	if opts.outPath == "" {
		opts.outPath, err = defaultOutPath(opts.platformURL)
		if err != nil {
			return options{}, err
		}
	}
	if err := checkOutsideWorkingDir(opts.outPath); err != nil {
		return options{}, err
	}

	if opts.email != "" {
		opts.password = os.Getenv("LOADTEST_DEV_PASSWORD")
		if opts.password == "" {
			line, err := bufio.NewReader(stdin).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return options{}, fmt.Errorf("read the password from standard input: %w", err)
			}
			opts.password = strings.TrimRight(line, "\r\n")
		}
		if opts.password == "" {
			return options{}, errors.New("no password for -email: set LOADTEST_DEV_PASSWORD (Read-Host -MaskInput) or pipe it to standard input")
		}
	} else if opts.teardown {
		return options{}, errors.New("-teardown needs the developer: -email (or LOADTEST_DEV_EMAIL) and LOADTEST_DEV_PASSWORD; load the settings file first")
	}
	return opts, nil
}

// serviceURLs works out the Social and Realtime addresses from Platform's: platform.<host> gives
// social.<host> and realtime.<host>, and a local :8081 gives :8082 and :8083 (docker compose).
func serviceURLs(platformURL string) (social, realtime string, err error) {
	u, err := url.Parse(platformURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("-platform %q is not an absolute URL", platformURL)
	}
	host := u.Hostname()
	switch {
	case strings.HasPrefix(host, "platform."):
		rest := strings.TrimPrefix(u.Host, "platform.")
		return u.Scheme + "://social." + rest, u.Scheme + "://realtime." + rest, nil
	case u.Port() == "8081":
		return u.Scheme + "://" + host + ":8082", u.Scheme + "://" + host + ":8083", nil
	default:
		return "", "", fmt.Errorf("cannot tell the Social and Realtime addresses from %q: give -social and -realtime", platformURL)
	}
}

// defaultOutPath is ~/.clan-loadtest/<host>[-<port>].ps1 on Windows and .sh elsewhere.
func defaultOutPath(platformURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home folder for the settings file (give -out): %w", err)
	}
	u, _ := url.Parse(platformURL)
	name := u.Hostname()
	if u.Port() != "" {
		name += "-" + u.Port()
	}
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".ps1"
	}
	return filepath.Join(home, ".clan-loadtest", name+ext), nil
}

// checkOutsideWorkingDir refuses a settings file inside the folder the tool runs from, which is
// the repository: the file holds an API key and must never be committed.
func checkOutsideWorkingDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("-out %q: %w", path, err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(wd, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("-out %q is inside %s: the file holds an API key, so write it outside the repository", path, wd)
	}
	return nil
}

// setup runs the steps against Platform and prints one line per step.
type setup struct {
	opts options
	api  *apiClient
	out  io.Writer
}

func (s *setup) say(format string, args ...any) {
	fmt.Fprintf(s.out, format+"\n", args...)
}

func (s *setup) prepare(ctx context.Context) error {
	if _, err := s.api.call(ctx, http.MethodGet, "/ready", "", nil, nil, http.StatusOK); err != nil {
		return fmt.Errorf("platform at %s is not ready (is the stack up?): %w", s.opts.platformURL, err)
	}
	token, err := s.developer(ctx)
	if err != nil {
		return err
	}
	gameID, err := s.game(ctx, token)
	if err != nil {
		return err
	}
	version, err := s.publish(ctx, token, gameID)
	if err != nil {
		return err
	}
	s.say("widget config published (version %d): clans of up to 20, clan chat on, no slow mode", version)
	key, revoked, err := s.freshKey(ctx, token, gameID)
	if err != nil {
		return err
	}
	s.say("API key created; %d older key(s) revoked", revoked)
	if err := s.writeSettings(gameID, key); err != nil {
		return err
	}
	s.say("")
	s.say("Settings written to %s. Load them into this window with:", s.opts.outPath)
	s.say("  . '%s'", s.opts.outPath)
	s.say("then check them with: k6 run loadtest/smoke.js")
	return nil
}

// developer signs in, registering a throwaway developer first when no email was given.
func (s *setup) developer(ctx context.Context) (string, error) {
	if s.opts.email == "" {
		email, password, err := s.register(ctx)
		if err != nil {
			return "", err
		}
		s.opts.email, s.opts.password = email, password
	}
	var login struct {
		Token string `json:"token"`
	}
	creds := map[string]string{"email": s.opts.email, "password": s.opts.password}
	if _, err := s.api.call(ctx, http.MethodPost, "/api/v1/developers/login", "", creds, &login, http.StatusOK); err != nil {
		return "", fmt.Errorf("sign in as %s: %w", s.opts.email, err)
	}
	if login.Token == "" {
		return "", errors.New("sign in: the answer had no token")
	}
	s.say("signed in as %s", s.opts.email)
	return login.Token, nil
}

// register creates a throwaway developer with a random password. A Platform with email
// verification on answers 202 and creates nothing: then the developer has to come from Studio.
func (s *setup) register(ctx context.Context) (email, password string, err error) {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("make a password: %w", err)
	}
	password = hex.EncodeToString(secret)
	email = fmt.Sprintf("loadtest-%d%s", time.Now().UnixNano(), throwawayDomain)
	body := map[string]string{"email": email, "password": password, "name": "Load test"}
	status, err := s.api.call(ctx, http.MethodPost, "/api/v1/developers/register", "", body, nil, http.StatusCreated, http.StatusAccepted)
	if err != nil {
		return "", "", fmt.Errorf("register a load-test developer: %w", err)
	}
	if status == http.StatusAccepted {
		return "", "", errors.New("this Platform has email verification on, so a developer cannot be registered from a script. " +
			"Register a developer for load tests in Studio once, then run again with -email <its address> and LOADTEST_DEV_PASSWORD set")
	}
	s.say("registered a throwaway developer %s (its password is in the settings file)", email)
	return email, password, nil
}

type gameEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// findGame returns the developer's game with the test game's name, or "" when there is none.
func (s *setup) findGame(ctx context.Context, token string) (gameEntry, error) {
	var list struct {
		Games []gameEntry `json:"games"`
	}
	if _, err := s.api.call(ctx, http.MethodGet, "/api/v1/games", token, nil, &list, http.StatusOK); err != nil {
		return gameEntry{}, fmt.Errorf("list games: %w", err)
	}
	var found []gameEntry
	for _, g := range list.Games {
		if g.Name == s.opts.gameName {
			found = append(found, g)
		}
	}
	switch {
	case len(found) == 0:
		return gameEntry{}, nil
	case len(found) > 1:
		return gameEntry{}, fmt.Errorf("%d games are named %q: rename the extra ones, or pass -game with a unique name", len(found), s.opts.gameName)
	case found[0].Role != "" && found[0].Role != "owner":
		return gameEntry{}, fmt.Errorf("the game %q is only shared with %s as a member; API keys need its owner", s.opts.gameName, s.opts.email)
	}
	return found[0], nil
}

// game reuses the test game when it exists, so its seeded players are reused too, or registers it.
func (s *setup) game(ctx context.Context, token string) (string, error) {
	existing, err := s.findGame(ctx, token)
	if err != nil {
		return "", err
	}
	if existing.ID != "" {
		s.say("using the game %q (%s)", s.opts.gameName, existing.ID)
		return existing.ID, nil
	}
	var created struct {
		ID string `json:"id"`
	}
	body := map[string]string{"name": s.opts.gameName, "description": "Load tests only (tools/loadtest-setup). Not a real game."}
	if _, err := s.api.call(ctx, http.MethodPost, "/api/v1/games", token, body, &created, http.StatusCreated); err != nil {
		return "", fmt.Errorf("register the game: %w", err)
	}
	s.say("registered the game %q (%s)", s.opts.gameName, created.ID)
	return created.ID, nil
}

// publish saves the template config as the game's draft and publishes it.
func (s *setup) publish(ctx context.Context, token, gameID string) (int, error) {
	var draft map[string]any
	if err := json.Unmarshal(templateConfig, &draft); err != nil {
		return 0, fmt.Errorf("read config.json: %w", err)
	}
	draft["game_id"] = gameID
	base := "/api/v1/games/" + gameID + "/widget"
	if _, err := s.api.call(ctx, http.MethodPut, base+"/draft", token, map[string]any{"config": draft}, nil, http.StatusOK); err != nil {
		return 0, fmt.Errorf("save the widget draft: %w", err)
	}
	var version struct {
		Version int `json:"version"`
	}
	if _, err := s.api.call(ctx, http.MethodPost, base+"/publish", token, nil, &version, http.StatusCreated); err != nil {
		return 0, fmt.Errorf("publish the widget: %w", err)
	}
	return version.Version, nil
}

type apiKey struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	RevokedAt *string `json:"revokedAt"`
}

// activeKeys lists the IDs of the game's keys that are not revoked.
func (s *setup) activeKeys(ctx context.Context, token, gameID string) ([]string, error) {
	var list struct {
		APIKeys []apiKey `json:"apiKeys"`
	}
	if _, err := s.api.call(ctx, http.MethodGet, "/api/v1/games/"+gameID+"/api-keys", token, nil, &list, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	var ids []string
	for _, k := range list.APIKeys {
		if k.RevokedAt == nil {
			ids = append(ids, k.ID)
		}
	}
	return ids, nil
}

func (s *setup) revoke(ctx context.Context, token string, ids []string) error {
	for _, id := range ids {
		if _, err := s.api.call(ctx, http.MethodDelete, "/api/v1/api-keys/"+url.PathEscape(id), token, nil, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			return fmt.Errorf("revoke API key %s: %w", id, err)
		}
	}
	return nil
}

// freshKey creates a key and then revokes the game's other active keys. A key is shown only
// once, so an older one cannot be handed out again; keeping one working key keeps it tidy.
func (s *setup) freshKey(ctx context.Context, token, gameID string) (string, int, error) {
	before, err := s.activeKeys(ctx, token, gameID)
	if err != nil {
		return "", 0, err
	}
	var created apiKey
	if _, err := s.api.call(ctx, http.MethodPost, "/api/v1/games/"+gameID+"/api-keys", token, nil, &created, http.StatusCreated); err != nil {
		return "", 0, fmt.Errorf("create an API key: %w", err)
	}
	if created.Key == "" {
		return "", 0, errors.New("create an API key: the answer had no key")
	}
	if err := s.revoke(ctx, token, before); err != nil {
		return "", 0, err
	}
	return created.Key, len(before), nil
}

// tearDown revokes every key of the test game and deletes the settings file.
func (s *setup) tearDown(ctx context.Context) error {
	token, err := s.developer(ctx)
	if err != nil {
		return err
	}
	g, err := s.findGame(ctx, token)
	if err != nil {
		return err
	}
	if g.ID == "" {
		s.say("no game named %q: nothing to revoke", s.opts.gameName)
	} else {
		ids, err := s.activeKeys(ctx, token, g.ID)
		if err != nil {
			return err
		}
		if err := s.revoke(ctx, token, ids); err != nil {
			return err
		}
		s.say("revoked %d API key(s) of %q", len(ids), s.opts.gameName)
	}
	switch err := os.Remove(s.opts.outPath); {
	case err == nil:
		s.say("deleted %s", s.opts.outPath)
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("delete %s: %w", s.opts.outPath, err)
	}
	s.say("The game, its players, clans and messages stay: see loadtest/README.md, \"Test data\".")
	return nil
}

// writeSettings writes the file the shell loads: readable by its owner only.
func (s *setup) writeSettings(gameID, key string) error {
	vars := [][2]string{
		{"LOADTEST_PLATFORM_URL", s.opts.platformURL},
		{"LOADTEST_SOCIAL_URL", s.opts.socialURL},
		{"LOADTEST_REALTIME_URL", s.opts.realtimeURL},
		{"LOADTEST_GAME_ID", gameID},
		{"LOADTEST_API_KEY", key},
	}
	// A throwaway developer's sign-in goes in the file, so the next run reuses the same developer,
	// game and seeded players. A real developer's password never does.
	if isThrowaway(s.opts.email) {
		vars = append(vars, [2]string{"LOADTEST_DEV_EMAIL", s.opts.email}, [2]string{"LOADTEST_DEV_PASSWORD", s.opts.password})
	}
	text := settingsFile(s.opts.outPath, vars, time.Now())
	if err := os.MkdirAll(filepath.Dir(s.opts.outPath), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.opts.outPath), err)
	}
	if err := os.WriteFile(s.opts.outPath, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", s.opts.outPath, err)
	}
	// WriteFile keeps the mode of a file that already exists.
	if err := os.Chmod(s.opts.outPath, 0o600); err != nil {
		return fmt.Errorf("restrict %s: %w", s.opts.outPath, err)
	}
	return nil
}

// throwawayDomain is where the developers this tool registers live; nobody can receive mail there.
const throwawayDomain = "@example.com"

// isThrowaway tells a developer this tool registered (loadtest-<number>@example.com).
func isThrowaway(email string) bool {
	return strings.HasPrefix(email, "loadtest-") && strings.HasSuffix(email, throwawayDomain)
}

// settingsFile renders the variables as PowerShell (a .ps1 path) or POSIX shell assignments, each
// value in single quotes so nothing in it is expanded.
func settingsFile(path string, vars [][2]string, now time.Time) string {
	ps := strings.HasSuffix(strings.ToLower(path), ".ps1")
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by tools/loadtest-setup on %s. Load it with: . '%s'\n", now.Format("2006-01-02 15:04"), path)
	b.WriteString("# It holds the test game's API key. Keep it out of the repository; -teardown revokes the key and deletes it.\n")
	for _, v := range vars {
		if ps {
			fmt.Fprintf(&b, "$env:%s = '%s'\n", v[0], strings.ReplaceAll(v[1], "'", "''"))
		} else {
			fmt.Fprintf(&b, "export %s='%s'\n", v[0], strings.ReplaceAll(v[1], "'", `'\''`))
		}
	}
	return b.String()
}

// apiClient sends JSON requests to Platform.
type apiClient struct {
	http *http.Client
	base string
}

// call sends one request and decodes a JSON answer into out. It returns the status, and an error
// when the status is not one of want.
func (c *apiClient) call(ctx context.Context, method, path, token string, body, out any, want ...int) (int, error) {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return resp.StatusCode, err
	}
	for _, w := range want {
		if resp.StatusCode == w {
			if out != nil && len(data) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return resp.StatusCode, fmt.Errorf("%s %s: read the answer: %w", method, path, err)
				}
			}
			return resp.StatusCode, nil
		}
	}
	return resp.StatusCode, fmt.Errorf("%s %s answered %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

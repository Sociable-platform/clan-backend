// Package gameconfig keeps the published config of each game in memory, so the rules and the
// frames that depend on it never wait for Platform.
//
// Platform owns the config. Realtime asks it for the newest published version of a game over
// Platform's public endpoint (GET /api/v1/public/games/{gameID}/widget-config), the same one the
// player's browser uses, so no extra endpoint and no shared secret are needed.
package gameconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNotPublished means the game has no published config (or does not exist: Platform answers
	// the same for both).
	ErrNotPublished = errors.New("the game has no published config")
	// ErrUnavailable means Platform could not give the config: it is down, too slow, or answered
	// with something unexpected. Trying again later may work.
	ErrUnavailable = errors.New("platform service unavailable")
	// ErrInvalidGame means the game ID is not a UUID. Nothing is sent to Platform for it.
	ErrInvalidGame = errors.New("the game ID is not a UUID")
	// ErrSuspended means Platform refused the config with 403 game_suspended: the game, or its
	// developer, is switched off by an admin. It is an answer about the game, not trouble.
	ErrSuspended = errors.New("the game is suspended")
)

// codeGameSuspended is the error code Platform uses for a suspended game and for a suspended
// developer alike, so players never see whose contract is behind it.
const codeGameSuspended = "game_suspended"

// maxResponseBytes bounds what is read from Platform. A config is at most 64 KB.
const maxResponseBytes = 256 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Config is one published version of a game's config.
type Config struct {
	Version  int             // 1, 2, 3 ...: every publish or rollback makes a new, higher one
	Settings json.RawMessage // the config document exactly as Platform sent it
	Chat     ChatRules       // the message rules in Settings, read once when the version is stored
}

// Source is where the config comes from.
type Source interface {
	// Fetch returns the newest published config of the game. Its errors wrap ErrNotPublished,
	// ErrSuspended, ErrUnavailable or ErrInvalidGame.
	Fetch(ctx context.Context, gameID string) (Config, error)
}

// HTTPSource asks the Platform service.
type HTTPSource struct {
	base string
	http *http.Client
}

// NewHTTPSource returns a Source for the Platform service at baseURL (for example http://platform:8081).
// timeout limits one request, including reading the answer.
func NewHTTPSource(baseURL string, timeout time.Duration) (*HTTPSource, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("platform: %q is not a valid URL, use http://host:port", baseURL)
	}
	return &HTTPSource{
		base: u.String(),
		http: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Fetch implements Source.
func (s *HTTPSource) Fetch(ctx context.Context, gameID string) (Config, error) {
	// The ID goes into the URL path, so it must be exactly a UUID and nothing else.
	if !uuidPattern.MatchString(gameID) {
		return Config{}, ErrInvalidGame
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/api/v1/public/games/"+url.PathEscape(gameID)+"/widget-config", nil)
	if err != nil {
		return Config{}, fmt.Errorf("build platform request: %w", ErrUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	// Platform lets browsers keep the answer for a minute; this call must always see the newest version.
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := s.http.Do(req)
	if err != nil {
		return Config{}, fmt.Errorf("call platform: %w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Config{}, fmt.Errorf("read platform answer: %w", ErrUnavailable)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Only Platform's own "not published" answer counts; any other 404 is a wrong address.
		if errorCode(body) == "widget_not_published" {
			return Config{}, ErrNotPublished
		}
		return Config{}, fmt.Errorf("platform answered 404: %w", ErrUnavailable)
	case http.StatusForbidden:
		// Only Platform's own "suspended" answer counts. Any other 403 says nothing about the
		// game, so it is trouble like a 500: the last known status stays.
		if errorCode(body) == codeGameSuspended {
			return Config{}, ErrSuspended
		}
		return Config{}, fmt.Errorf("platform answered 403: %w", ErrUnavailable)
	default:
		return Config{}, fmt.Errorf("platform answered %d: %w", resp.StatusCode, ErrUnavailable)
	}

	var c struct {
		Version int             `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(body, &c); err != nil || c.Version < 1 || !bytes.HasPrefix(bytes.TrimSpace(c.Config), []byte("{")) {
		return Config{}, fmt.Errorf("platform answer is not a config: %w", ErrUnavailable)
	}
	return Config{Version: c.Version, Settings: c.Config}, nil
}

// errorCode reads error.code from Platform's standard error body; "" when it is not one.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Code
}

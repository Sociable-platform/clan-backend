// Package gameconfig gives Social the clan rules a game developer set in Studio: how many
// members a clan may have and the clan roles. They come from the game's latest published
// widget config, read from Platform's public endpoint
// (GET /api/v1/public/games/{gameID}/widget-config), the one Realtime and the SDK use too.
//
// The answer is kept in memory for a short time (TTL), so a busy game costs Platform at most
// one request per TTL. Platform being down never breaks a request: the last rules seen are
// used, or the defaults when there are none.
package gameconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultMaxMembers is the clan size before a game publishes a config (and while Platform
// cannot be asked and nothing is known yet).
const DefaultMaxMembers = 10

// The bounds of clan.maxMembers in config v1; a value outside them is a broken answer.
const (
	minMaxMembers = 2
	maxMaxMembers = 200
)

// maxResponseBytes caps what is read from Platform; a config is at most 64 KB.
const maxResponseBytes = 256 << 10

// Role is one entry of clan.roles, lowest power first.
type Role struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CanPromote bool   `json:"canPromote"`
	CanKick    bool   `json:"canKick"`
}

// Rules are the clan rules of one game.
type Rules struct {
	MaxMembers int
	// Roles is empty when the game has published no config yet (or it could not be read).
	Roles []Role
	// Published is false when these are the defaults, not a published config.
	Published bool
}

// Defaults are the rules of a game that has published no config.
func Defaults() Rules { return Rules{MaxMembers: DefaultMaxMembers} }

// Source gives the rules of a game. It never fails: when nothing better is known it gives
// Defaults().
type Source interface {
	Rules(ctx context.Context, gameID string) Rules
}

// Fixed is a Source that always gives Defaults(); Social uses it when no Platform URL is set.
type Fixed struct{}

// Rules returns Defaults().
func (Fixed) Rules(context.Context, string) Rules { return Defaults() }

// Client asks Platform for a game's published config and keeps the rules for ttl.
type Client struct {
	base string
	http *http.Client
	ttl  time.Duration
	log  *slog.Logger
	now  func() time.Time

	mu    sync.Mutex
	games map[string]cached
}

type cached struct {
	rules   Rules
	expires time.Time
}

// NewClient returns a Client for Platform at baseURL (such as http://platform:8081).
// timeout bounds each call to Platform; ttl is how long an answer is reused.
func NewClient(baseURL string, timeout, ttl time.Duration, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("platform: %q is not a valid URL, use http://host:port", baseURL)
	}
	return &Client{
		base: u.String(),
		http: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		ttl:   ttl,
		log:   log,
		now:   time.Now,
		games: map[string]cached{},
	}, nil
}

// errNotPublished is Platform's own answer for a game with no published config.
var errNotPublished = errors.New("the game has no published config")

// Rules returns the game's rules, from memory while they are fresh, else from Platform.
// If Platform cannot be asked, it keeps the last rules it had for the game (or the defaults)
// and asks again after ttl, so a Platform that is down is not asked on every request.
func (c *Client) Rules(ctx context.Context, gameID string) Rules {
	now := c.now()
	c.mu.Lock()
	old, known := c.games[gameID]
	c.mu.Unlock()
	if known && now.Before(old.expires) {
		return old.rules
	}

	rules, err := c.fetch(ctx, gameID)
	switch {
	case errors.Is(err, errNotPublished):
		rules = Defaults()
	case err != nil:
		rules = Defaults()
		if known {
			rules = old.rules
		}
		c.log.Warn("cannot read the game's clan rules from platform, using the last known ones",
			"game_id", gameID, "max_members", rules.MaxMembers, "error", err)
	}
	c.mu.Lock()
	c.games[gameID] = cached{rules: rules, expires: now.Add(c.ttl)}
	c.mu.Unlock()
	return rules
}

// fetch asks Platform once.
func (c *Client) fetch(ctx context.Context, gameID string) (Rules, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/public/games/"+url.PathEscape(gameID)+"/widget-config", nil)
	if err != nil {
		return Rules{}, fmt.Errorf("build platform request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache") // Platform lets browsers keep it a minute; we want the newest

	resp, err := c.http.Do(req)
	if err != nil {
		return Rules{}, fmt.Errorf("call platform: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Rules{}, fmt.Errorf("read platform answer: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Only Platform's own "not published" answer counts; any other 404 is a wrong address.
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Code == "widget_not_published" {
			return Rules{}, errNotPublished
		}
		return Rules{}, errors.New("platform answered 404")
	default:
		return Rules{}, fmt.Errorf("platform answered %d", resp.StatusCode)
	}

	var answer struct {
		Config struct {
			Clan struct {
				MaxMembers *int   `json:"maxMembers"`
				Roles      []Role `json:"roles"`
			} `json:"clan"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return Rules{}, fmt.Errorf("platform answer is not a config: %w", err)
	}
	clan := answer.Config.Clan
	if clan.MaxMembers == nil || *clan.MaxMembers < minMaxMembers || *clan.MaxMembers > maxMaxMembers {
		return Rules{}, errors.New("platform answer has no valid clan.maxMembers")
	}
	return Rules{MaxMembers: *clan.MaxMembers, Roles: clan.Roles, Published: true}, nil
}

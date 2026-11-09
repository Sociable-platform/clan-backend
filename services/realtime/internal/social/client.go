// Package social is realtime's client for the social service.
//
// A player token holds only the game's own player ID (for example "u_42"), but conversations,
// members and events use social's own player ID (a UUID). Realtime learns that ID once per
// connection by calling social's GET /api/v1/players/me with the player's own token, so no
// extra endpoint and no shared secret are needed.
package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnavailable means social could not tell who the player is: it is down, too slow, or
// answered with something unexpected. The player can simply try again later.
var ErrUnavailable = errors.New("social service unavailable")

// maxResponseBytes bounds what is read from social, so a misbehaving server cannot fill memory.
const maxResponseBytes = 64 << 10

// Player is a player as social knows them.
type Player struct {
	ID          string // social's own ID for the player (a UUID)
	DisplayName string
}

// Client talks to the social service.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a Client for the social service at baseURL (for example http://social:8082).
// timeout limits one request, including reading the answer.
func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("social: %q is not a valid URL, use http://host:port", baseURL)
	}
	return &Client{
		base: u.String(),
		http: &http.Client{
			Timeout: timeout,
			// The token must only ever go to the configured address, so redirects are not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Player returns the player the token belongs to. Social creates the player record on the
// first call. gameID is the game from the verified token; the answer must be for the same game,
// otherwise the call fails, so a mix-up between games can never reach the hub.
// Every failure wraps ErrUnavailable. The token is never put in an error.
func (c *Client) Player(ctx context.Context, token, gameID string) (Player, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/players/me", nil)
	if err != nil {
		return Player{}, fmt.Errorf("build social request: %w", ErrUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// err can mention the URL but never the header, so the token is not in it.
		return Player{}, fmt.Errorf("call social: %w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Player{}, fmt.Errorf("read social answer: %w", ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return Player{}, fmt.Errorf("social answered %d: %w", resp.StatusCode, ErrUnavailable)
	}

	var p struct {
		ID          string `json:"id"`
		GameID      string `json:"gameId"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.ID == "" {
		return Player{}, fmt.Errorf("social answer is not a player: %w", ErrUnavailable)
	}
	if !strings.EqualFold(p.GameID, gameID) {
		return Player{}, fmt.Errorf("social answered for another game: %w", ErrUnavailable)
	}
	return Player{ID: strings.ToLower(p.ID), DisplayName: p.DisplayName}, nil
}

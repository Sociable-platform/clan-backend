package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoInternalToken means Friendships was called on a Client that has no internal token. It is a
// mistake in the setup, not an outage, so it does not wrap ErrUnavailable.
var ErrNoInternalToken = errors.New("social internal token is not set")

// internalTokenHeader carries the shared secret of social's /internal endpoints.
const internalTokenHeader = "X-Internal-Token"

// maxPageBytes bounds one page of friendships. A page of 500 is about 200 KB, far more than a player answer.
const maxPageBytes = 4 << 20

// Friendship is two friends in one game, as social lists them. A has the smaller ID.
type Friendship struct {
	GameID string // the game's ID (a UUID)
	A, B   Player
}

// FriendshipPage is one page of Friendships.
type FriendshipPage struct {
	Friendships []Friendship
	NextCursor  string // pass it to the next call; empty on the last page
}

// WithInternalToken sets the secret Friendships sends to social (social's SOCIAL_INTERNAL_TOKEN).
// It returns c, so it can follow NewClient. The token only ever goes to the configured address.
func (c *Client) WithInternalToken(token string) *Client {
	c.internalToken = token
	return c
}

// Friendships returns one page of every friendship of every game, from social's internal sync
// endpoint. after is the NextCursor of the previous page, empty for the first one; limit is how many
// to ask for (social allows 1 to 500).
//
// A failure to get a good answer wraps ErrUnavailable. The token is never put in an error.
func (c *Client) Friendships(ctx context.Context, after string, limit int) (FriendshipPage, error) {
	if c.internalToken == "" {
		return FriendshipPage{}, ErrNoInternalToken
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/internal/v1/friendships?"+q.Encode(), nil)
	if err != nil {
		return FriendshipPage{}, fmt.Errorf("build social request: %w", ErrUnavailable)
	}
	req.Header.Set(internalTokenHeader, c.internalToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// err can mention the URL but never the header, so the token is not in it.
		return FriendshipPage{}, fmt.Errorf("call social: %w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return FriendshipPage{}, fmt.Errorf("read social answer: %w", ErrUnavailable)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusNotFound:
		// 401: the token differs from social's. 404: social has no token set (the route is off) or is too old.
		return FriendshipPage{}, fmt.Errorf("social answered %d, check that the internal tokens of social and realtime are the same: %w", resp.StatusCode, ErrUnavailable)
	default:
		return FriendshipPage{}, fmt.Errorf("social answered %d: %w", resp.StatusCode, ErrUnavailable)
	}

	var raw struct {
		Friendships []struct {
			GameID  string `json:"gameId"`
			PlayerA struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"playerA"`
			PlayerB struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"playerB"`
		} `json:"friendships"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Friendships == nil {
		return FriendshipPage{}, fmt.Errorf("social answer is not a page of friendships: %w", ErrUnavailable)
	}
	page := FriendshipPage{Friendships: make([]Friendship, 0, len(raw.Friendships)), NextCursor: raw.NextCursor}
	for _, f := range raw.Friendships {
		page.Friendships = append(page.Friendships, Friendship{
			GameID: strings.ToLower(f.GameID),
			A:      Player{ID: strings.ToLower(f.PlayerA.ID), DisplayName: f.PlayerA.DisplayName},
			B:      Player{ID: strings.ToLower(f.PlayerB.ID), DisplayName: f.PlayerB.DisplayName},
		})
	}
	return page, nil
}

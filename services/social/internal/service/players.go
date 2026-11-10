package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

// maxDisplayNameLen keeps names short enough for clan and member lists; longer names are cut.
const maxDisplayNameLen = 32

// Player is a player of a game, as social knows them.
type Player struct {
	ID          string    // social's own ID for the player
	GameID      string    // the game the player belongs to
	ExternalID  string    // the game's own player ID, e.g. "u_42"
	DisplayName string    // name shown to other players
	CreatedAt   time.Time // when social first saw the player
}

// EnsurePlayer returns the calling player's record. The first call for a (gameID, externalID)
// pair creates it; later calls return the same record with the display name updated.
// gameID and externalID come from the verified player token, never from the request.
func (s *Service) EnsurePlayer(ctx context.Context, gameID, externalID, displayName string) (Player, error) {
	row, err := s.store.UpsertPlayer(ctx, gameID, externalID, cleanDisplayName(displayName, externalID))
	if err != nil {
		return Player{}, fmt.Errorf("ensure player: %w", err)
	}
	return Player{
		ID:          row.ID,
		GameID:      row.GameID,
		ExternalID:  row.ExternalID,
		DisplayName: row.DisplayName,
		CreatedAt:   row.CreatedAt,
	}, nil
}

// cleanDisplayName trims spaces, falls back to the game's player ID when the token has no
// name (the column cannot be empty), and cuts the name to maxDisplayNameLen characters.
func cleanDisplayName(name, externalID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = externalID
	}
	if utf8.RuneCountInString(name) > maxDisplayNameLen {
		name = string([]rune(name)[:maxDisplayNameLen])
	}
	return name
}

// maxPlayerSearchLen is the longest search text accepted by SearchPlayers.
const maxPlayerSearchLen = 50

// playerSearchLimit is the most players one search returns, and how many it returns when the
// caller does not ask for fewer.
const playerSearchLimit = 100

// SearchPlayersInput is what a caller can ask of the player search. Everything is text, as it
// comes from the URL, and an empty value means "not given". SearchPlayers checks it.
type SearchPlayersInput struct {
	Query string // the name must contain it, ignoring upper/lower case; required, at most 50 characters
	Limit string // 1 to 100; default 100
}

// PlayerSearchResult is a player found by SearchPlayers, with how they relate to the caller.
type PlayerSearchResult struct {
	ID          string
	DisplayName string
	Relation    string // none, friend, request_sent or request_received
	RequestID   string // the pending request's ID for request_sent and request_received, otherwise ""
}

// SearchPlayers finds players of the caller's game by name, so the caller can add them as
// friends. The caller is never in the results, and neither is any other game's player. Each
// result says how the player relates to the caller, so the UI can show Add, Pending, Accept or
// Friend. The caller's player record is created first if needed.
//
// A search text is required, so the endpoint can never list a whole game.
//
// Wrong input is a 400: invalid_query (empty or over 50 characters) or invalid_limit.
func (s *Service) SearchPlayers(ctx context.Context, gameID, externalID, displayName string, in SearchPlayersInput) ([]PlayerSearchResult, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_query", "Search text (q) is required")
	}
	if utf8.RuneCountInString(q) > maxPlayerSearchLen {
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_query",
			fmt.Sprintf("Search text must be at most %d characters", maxPlayerSearchLen))
	}
	limit := playerSearchLimit
	if in.Limit != "" {
		// plain digits only: no sign, no decimals, no leading zero (same rule as the clan list)
		n, err := strconv.Atoi(in.Limit)
		if !limitText.MatchString(in.Limit) || err != nil || n > playerSearchLimit {
			return nil, httpx.NewError(http.StatusBadRequest, "invalid_limit",
				fmt.Sprintf("limit must be a whole number from 1 to %d", playerSearchLimit))
		}
		limit = n
	}

	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return nil, fmt.Errorf("search players: %w", err)
	}
	rows, err := s.store.SearchPlayers(ctx, gameID, me.ID, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search players: %w", err)
	}
	players := make([]PlayerSearchResult, 0, len(rows))
	for _, p := range rows {
		players = append(players, PlayerSearchResult(p))
	}
	return players, nil
}

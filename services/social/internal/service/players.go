package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

package repository

import (
	"context"
	"fmt"
	"time"
)

// Player is a row of the players table.
type Player struct {
	ID          string    // our own ID for the player (UUID)
	GameID      string    // the game the player belongs to
	ExternalID  string    // the game's own player ID, e.g. "u_42"
	DisplayName string    // name shown to other players
	CreatedAt   time.Time // when we first saw this player
}

// UpsertPlayer returns the player (gameID, externalID), creating the row the first time
// that pair appears. For an existing player it updates the display name, so a rename
// in the game shows up here on the next call.
//
// It is one SQL statement on purpose: if a game sends two requests at the same moment
// for a brand-new player, the unique key (game_id, external_id) and ON CONFLICT make sure
// only one row is created and both requests get it back.
func (r *Repository) UpsertPlayer(ctx context.Context, gameID, externalID, displayName string) (Player, error) {
	if r.pool == nil {
		return Player{}, ErrNoDatabase
	}
	p := Player{GameID: gameID, ExternalID: externalID}
	err := r.pool.QueryRow(ctx,
		`insert into players (game_id, external_id, display_name)
		 values ($1, $2, $3)
		 on conflict (game_id, external_id)
		 do update set display_name = excluded.display_name
		 returning id::text, display_name, created_at`,
		gameID, externalID, displayName,
	).Scan(&p.ID, &p.DisplayName, &p.CreatedAt)
	if err != nil {
		return Player{}, fmt.Errorf("upsert player: %w", err)
	}
	return p, nil
}

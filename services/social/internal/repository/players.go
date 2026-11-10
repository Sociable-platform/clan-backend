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

// Relations between the caller and a player in search results.
const (
	RelationNone            = "none"             // strangers: the caller can send a request
	RelationFriend          = "friend"           // already friends
	RelationRequestSent     = "request_sent"     // the caller asked; waiting for the other player
	RelationRequestReceived = "request_received" // the other player asked; the caller can accept
)

// PlayerSearchResult is a player found by SearchPlayers, with how they relate to the caller.
type PlayerSearchResult struct {
	ID          string
	DisplayName string
	Relation    string // one of the Relation constants
	RequestID   string // the pending request's ID for request_sent and request_received, otherwise ""
}

// SearchPlayers returns up to limit players of gameID whose display name contains query,
// ignoring upper/lower case, sorted by name (ignoring case) and then ID. The caller (playerID)
// is never in the list, and neither is any other game's player. strpos looks for the exact text,
// so % and _ in the search are normal characters.
//
// Each result says whether the two are friends, have a pending request (and which way), or
// neither. A pair has at most one request and is never friends and pending at the same time.
func (r *Repository) SearchPlayers(ctx context.Context, gameID, playerID, query string, limit int) ([]PlayerSearchResult, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.pool.Query(ctx,
		`select p.id::text, p.display_name,
		        case when f.game_id is not null then 'friend'
		             when fr.sender_id = $2::uuid then 'request_sent'
		             when fr.receiver_id = $2::uuid then 'request_received'
		             else 'none' end,
		        coalesce(fr.id::text, '')
		 from players p
		 left join friendships f
		        on f.game_id = p.game_id
		       and f.player_a_id = least(p.id, $2::uuid) and f.player_b_id = greatest(p.id, $2::uuid)
		 left join friend_requests fr
		        on fr.game_id = p.game_id
		       and least(fr.sender_id, fr.receiver_id) = least(p.id, $2::uuid)
		       and greatest(fr.sender_id, fr.receiver_id) = greatest(p.id, $2::uuid)
		 where p.game_id = $1 and p.id <> $2::uuid
		   and strpos(lower(p.display_name), lower($3)) > 0
		 order by lower(p.display_name), p.id
		 limit $4`,
		gameID, playerID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search players: %w", err)
	}
	defer rows.Close()

	players := make([]PlayerSearchResult, 0)
	for rows.Next() {
		var p PlayerSearchResult
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Relation, &p.RequestID); err != nil {
			return nil, fmt.Errorf("search players: %w", err)
		}
		players = append(players, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search players: %w", err)
	}
	return players, nil
}

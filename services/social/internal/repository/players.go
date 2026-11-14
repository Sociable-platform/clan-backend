package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Player is a row of the players table.
type Player struct {
	ID          string    // our own ID for the player (UUID)
	GameID      string    // the game the player belongs to
	ExternalID  string    // the game's own player ID, e.g. "u_42"
	DisplayName string    // name shown to other players
	CreatedAt   time.Time // when we first saw this player
	// Renamed is set by UpsertPlayer when that call changed the name of an existing player.
	Renamed bool
}

// UpsertPlayer returns the player (gameID, externalID), creating the row the first time
// that pair appears. For an existing player it updates the display name, so a rename
// in the game shows up here on the next call. Renamed is true only for the call that
// changed the name of an existing player: not for the first insert, and not when the
// name was already the same.
//
// Most calls change nothing, so it first only reads the row. Only a new player or a new
// name runs the upsert, which is one SQL statement on purpose:
//   - two requests for a brand-new player at the same moment: the unique key
//     (game_id, external_id) and ON CONFLICT make sure only one row is created;
//   - two requests with the same new name at the same moment: "where the name differs"
//     lets only the first one update the row, so only one call reports Renamed.
func (r *Repository) UpsertPlayer(ctx context.Context, gameID, externalID, displayName string) (Player, error) {
	if r.pool == nil {
		return Player{}, ErrNoDatabase
	}
	p, err := r.playerByExternalID(ctx, gameID, externalID)
	switch {
	case err == nil && p.DisplayName == displayName:
		return p, nil // the usual case: nothing to write
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return Player{}, fmt.Errorf("upsert player: %w", err)
	}

	p = Player{GameID: gameID, ExternalID: externalID}
	var inserted bool
	err = r.db(ctx).QueryRow(ctx,
		`insert into players (game_id, external_id, display_name)
		 values ($1, $2, $3)
		 on conflict (game_id, external_id)
		 do update set display_name = excluded.display_name
		   where players.display_name <> excluded.display_name
		 returning id::text, display_name, created_at, xmax = 0`, // xmax = 0: the row was inserted, not updated
		gameID, externalID, displayName,
	).Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another request saved this very name a moment ago; it reports the rename, not us.
		p, err = r.playerByExternalID(ctx, gameID, externalID)
		if err != nil {
			return Player{}, fmt.Errorf("upsert player: %w", err)
		}
		return p, nil
	}
	if err != nil {
		return Player{}, fmt.Errorf("upsert player: %w", err)
	}
	p.Renamed = !inserted
	return p, nil
}

// playerByExternalID reads the player (gameID, externalID); pgx.ErrNoRows if there is none.
func (r *Repository) playerByExternalID(ctx context.Context, gameID, externalID string) (Player, error) {
	p := Player{GameID: gameID, ExternalID: externalID}
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, display_name, created_at from players where game_id = $1 and external_id = $2`,
		gameID, externalID,
	).Scan(&p.ID, &p.DisplayName, &p.CreatedAt)
	return p, err
}

// Relations between the caller and a player in search results.
const (
	RelationNone            = "none"             // strangers: the caller can send a request
	RelationFriend          = "friend"           // already friends
	RelationRequestSent     = "request_sent"     // the caller asked; waiting for the other player
	RelationRequestReceived = "request_received" // the other player asked; the caller can accept
	RelationBlocked         = "blocked"          // the caller blocked this player; they can unblock
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
// Each result says whether the caller blocked the player, the two are friends, have a pending
// request (and which way), or neither. A pair has at most one request and is never friends and
// pending at the same time; a block removes both. A player who blocked the caller is left out.
func (r *Repository) SearchPlayers(ctx context.Context, gameID, playerID, query string, limit int) ([]PlayerSearchResult, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select p.id::text, p.display_name,
		        case when mine.blocker_id is not null then 'blocked'
		             when f.game_id is not null then 'friend'
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
		 left join blocks mine
		        on mine.game_id = p.game_id and mine.blocker_id = $2::uuid and mine.blocked_id = p.id
		 where p.game_id = $1 and p.id <> $2::uuid
		   and strpos(lower(p.display_name), lower($3)) > 0
		   -- a player who blocked the caller is not shown to them: the block stays hidden
		   and not exists (select 1 from blocks theirs
		                   where theirs.game_id = p.game_id and theirs.blocker_id = p.id and theirs.blocked_id = $2::uuid)
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

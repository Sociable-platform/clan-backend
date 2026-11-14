package repository

import (
	"context"
	"fmt"
	"time"
)

// FriendshipKey is the primary key of a friendship. Friendships are listed in this order, so
// a key is also the place to continue a listing from.
type FriendshipKey struct {
	GameID  string
	PlayerA string // the smaller player ID of the pair
	PlayerB string
}

// zeroUUID sorts before every real ID, so a listing "after" it starts at the first friendship.
const zeroUUID = "00000000-0000-0000-0000-000000000000"

// SyncFriendship is one friendship with both players' names, for another service that rebuilds
// its own copy (the internal sync endpoint).
type SyncFriendship struct {
	GameID    string
	PlayerA   FriendPlayer
	PlayerB   FriendPlayer
	CreatedAt time.Time
}

// Key returns the friendship's primary key.
func (f SyncFriendship) Key() FriendshipKey {
	return FriendshipKey{GameID: f.GameID, PlayerA: f.PlayerA.ID, PlayerB: f.PlayerB.ID}
}

// ListFriendshipsAfter returns up to limit friendships of every game, in primary key order,
// that come after the key after. A zero key starts at the beginning. after must hold valid UUIDs
// (the service checks).
//
// It is the one repository function that is not limited to one game: it feeds a service-to-
// service endpoint, never a player.
func (r *Repository) ListFriendshipsAfter(ctx context.Context, after FriendshipKey, limit int) ([]SyncFriendship, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	if after == (FriendshipKey{}) {
		after = FriendshipKey{GameID: zeroUUID, PlayerA: zeroUUID, PlayerB: zeroUUID}
	}
	rows, err := r.db(ctx).Query(ctx,
		`select f.game_id::text, f.player_a_id::text, pa.display_name, f.player_b_id::text, pb.display_name, f.created_at
		 from friendships f
		 join players pa on pa.id = f.player_a_id and pa.game_id = f.game_id
		 join players pb on pb.id = f.player_b_id and pb.game_id = f.game_id
		 where (f.game_id, f.player_a_id, f.player_b_id) > ($1::uuid, $2::uuid, $3::uuid)
		 order by f.game_id, f.player_a_id, f.player_b_id
		 limit $4`,
		after.GameID, after.PlayerA, after.PlayerB, limit)
	if err != nil {
		return nil, fmt.Errorf("list friendships after %s/%s/%s: %w", after.GameID, after.PlayerA, after.PlayerB, err)
	}
	defer rows.Close()

	out := make([]SyncFriendship, 0, limit)
	for rows.Next() {
		var f SyncFriendship
		if err := rows.Scan(&f.GameID, &f.PlayerA.ID, &f.PlayerA.DisplayName, &f.PlayerB.ID, &f.PlayerB.DisplayName, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("list friendships: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list friendships: %w", err)
	}
	return out, nil
}

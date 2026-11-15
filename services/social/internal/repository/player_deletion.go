package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BlockPair is one block: Blocker blocked Blocked.
type BlockPair struct {
	Blocker FriendPlayer
	Blocked FriendPlayer
}

// PlayerDeletion is what DeletePlayer removed together with the player, read in the same
// transaction, so the events tell exactly what was saved.
type PlayerDeletion struct {
	// Player is the deleted player with the name they had before. ID is empty when the game had
	// no live record of the player: then only a deleted record was written.
	Player      FriendPlayer
	Clan        *LeaveResult    // the clan the player left; nil when they were in none
	Friendships []Friendship    // the friendships that ended, oldest first
	Requests    []FriendRequest // the pending requests the player sent or received, oldest first
	Blocks      []BlockPair     // the blocks by or of the player, oldest first
}

// DeletePlayer deletes the live player (gameID, externalID) of a game that deleted the account.
// The row stays, renamed to deletedName and with deleted_at set, so the player's tokens issued
// before now can be refused (DeletedAfter) and Realtime's chat history keeps a player to point at.
// Everything else goes, in one transaction:
//
//   - the clan membership, exactly as LeaveClan does it (the owner hands over to the member who
//     joined first; the last member's clan is deleted);
//   - every friendship, pending friend request and block the player is in.
//
// The player row is locked first ("for update"). Friend requests and blocks read the other player
// with "for key share" (friendPlayer), so one that runs at the same moment waits and then finds
// no player, instead of being saved for a deleted one.
//
// When there is no live record (a player social never saw, or one deleted before), a deleted
// record dated now is written, so tokens issued before this call are refused all the same. The
// result then has an empty Player.ID and nothing else.
//
// Once deleted, the pair is free again: the next token for the same externalID, issued after
// now, gets a new, empty player (UpsertPlayer).
func (r *Repository) DeletePlayer(ctx context.Context, gameID, externalID, deletedName string) (PlayerDeletion, error) {
	if r.pool == nil {
		return PlayerDeletion{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ctx = context.WithValue(ctx, txKey{}, tx) // so LeaveClan below runs inside this transaction

	var res PlayerDeletion
	err = tx.QueryRow(ctx,
		`select id::text, display_name from players
		 where game_id = $1 and external_id = $2 and deleted_at is null
		 for update`,
		gameID, externalID).Scan(&res.Player.ID, &res.Player.DisplayName)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx,
			`insert into players (game_id, external_id, display_name, deleted_at) values ($1, $2, $3, now())`,
			gameID, externalID, deletedName)
		if err != nil {
			return PlayerDeletion{}, fmt.Errorf("delete player: write deleted record: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
		}
		return PlayerDeletion{}, nil
	case err != nil:
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}

	if res.Clan, err = r.leaveOnDeletion(ctx, tx, gameID, externalID, res.Player.ID); err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	if res.Friendships, err = endFriendships(ctx, tx, gameID, res.Player.ID); err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	if res.Requests, err = dropFriendRequests(ctx, tx, gameID, res.Player.ID); err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	if res.Blocks, err = dropBlocks(ctx, tx, gameID, res.Player.ID); err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}

	_, err = tx.Exec(ctx,
		`update players set display_name = $2, deleted_at = now() where id = $1`,
		res.Player.ID, deletedName)
	if err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PlayerDeletion{}, fmt.Errorf("delete player: %w", err)
	}
	return res, nil
}

// leaveOnDeletion takes the player out of their clan with LeaveClan; nil when they are in none.
// A kick or the clan's end that happened just before (ErrNotInClan, ErrClanNotFound) leaves
// nothing to do.
func (r *Repository) leaveOnDeletion(ctx context.Context, tx pgx.Tx, gameID, externalID, playerID string) (*LeaveResult, error) {
	var clanID string
	err := tx.QueryRow(ctx,
		`select clan_id::text from clan_members where game_id = $1 and player_id = $2`,
		gameID, playerID).Scan(&clanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find clan: %w", err)
	}
	left, err := r.LeaveClan(ctx, gameID, clanID, externalID)
	switch {
	case errors.Is(err, ErrNotInClan), errors.Is(err, ErrClanNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("leave clan: %w", err)
	}
	return &left, nil
}

// endFriendships deletes every friendship of playerID and returns them with both names.
func endFriendships(ctx context.Context, tx pgx.Tx, gameID, playerID string) ([]Friendship, error) {
	rows, err := tx.Query(ctx,
		`with ended as (
		     delete from friendships
		     where game_id = $1 and (player_a_id = $2 or player_b_id = $2)
		     returning player_a_id, player_b_id, created_at)
		 select a.id::text, a.display_name, b.id::text, b.display_name, e.created_at
		 from ended e
		 join players a on a.id = e.player_a_id
		 join players b on b.id = e.player_b_id
		 order by e.created_at, a.id, b.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("end friendships: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Friendship, error) {
		var f Friendship
		err := row.Scan(&f.PlayerA.ID, &f.PlayerA.DisplayName, &f.PlayerB.ID, &f.PlayerB.DisplayName, &f.CreatedAt)
		return f, err
	})
	if err != nil {
		return nil, fmt.Errorf("end friendships: %w", err)
	}
	return out, nil
}

// dropFriendRequests deletes every pending request sent or received by playerID.
func dropFriendRequests(ctx context.Context, tx pgx.Tx, gameID, playerID string) ([]FriendRequest, error) {
	rows, err := tx.Query(ctx,
		`with dropped as (
		     delete from friend_requests
		     where game_id = $1 and (sender_id = $2 or receiver_id = $2)
		     returning id, sender_id, receiver_id, created_at)
		 select d.id::text, s.id::text, s.display_name, rc.id::text, rc.display_name, d.created_at
		 from dropped d
		 join players s  on s.id = d.sender_id
		 join players rc on rc.id = d.receiver_id
		 order by d.created_at, d.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("drop friend requests: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FriendRequest, error) {
		var req FriendRequest
		err := row.Scan(&req.ID, &req.Sender.ID, &req.Sender.DisplayName,
			&req.Receiver.ID, &req.Receiver.DisplayName, &req.CreatedAt)
		return req, err
	})
	if err != nil {
		return nil, fmt.Errorf("drop friend requests: %w", err)
	}
	return out, nil
}

// dropBlocks deletes every block by or of playerID.
func dropBlocks(ctx context.Context, tx pgx.Tx, gameID, playerID string) ([]BlockPair, error) {
	rows, err := tx.Query(ctx,
		`with dropped as (
		     delete from blocks
		     where game_id = $1 and (blocker_id = $2 or blocked_id = $2)
		     returning blocker_id, blocked_id, created_at)
		 select br.id::text, br.display_name, bd.id::text, bd.display_name
		 from dropped d
		 join players br on br.id = d.blocker_id
		 join players bd on bd.id = d.blocked_id
		 order by d.created_at, br.id, bd.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("drop blocks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BlockPair, error) {
		var b BlockPair
		err := row.Scan(&b.Blocker.ID, &b.Blocker.DisplayName, &b.Blocked.ID, &b.Blocked.DisplayName)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("drop blocks: %w", err)
	}
	return out, nil
}

// DeletedAfter reports whether the player (gameID, externalID) was deleted after t. The auth
// gate asks it with a token's issue time: true means the token was issued before a deletion of
// its player, so it must be refused even though it has not expired.
func (r *Repository) DeletedAfter(ctx context.Context, gameID, externalID string, t time.Time) (bool, error) {
	if r.pool == nil {
		return false, ErrNoDatabase
	}
	var deleted bool
	err := r.db(ctx).QueryRow(ctx,
		`select exists (select 1 from players
		                where game_id = $1 and external_id = $2
		                  and deleted_at is not null and deleted_at > $3)`,
		gameID, externalID, t).Scan(&deleted)
	if err != nil {
		return false, fmt.Errorf("deleted after: %w", err)
	}
	return deleted, nil
}

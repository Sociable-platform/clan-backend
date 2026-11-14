package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBlockSelf means a player tried to block (or unblock) themselves.
var ErrBlockSelf = errors.New("cannot block yourself")

// BlockResult is what Block did.
type BlockResult struct {
	Blocker FriendPlayer
	Blocked FriendPlayer
	// Created is false when the block already existed: nothing changed, so no event.
	Created bool
	// RemovedFriendship is the pair's friendship that the block ended, if they were friends.
	RemovedFriendship *Friendship
	// RemovedRequest is the pair's pending friend request (either way) that the block deleted.
	// A pair has at most one.
	RemovedRequest *FriendRequest
}

// Block makes blockerID block blockedID in gameID. In one transaction, under the same pair lock
// as every friend change, it saves the block and removes what a block does not allow: the pair's
// friendship and their pending friend request, in either direction. It returns what it removed,
// so the service can tell the other services (friend.removed, friend.request_cancelled or
// _declined). Blocking someone already blocked changes nothing (Created false).
//
// Errors: ErrBlockSelf, ErrPlayerNotFound (either player missing, in another game, or an ID that
// is not a UUID).
func (r *Repository) Block(ctx context.Context, gameID, blockerID, blockedID string) (BlockResult, error) {
	if r.pool == nil {
		return BlockResult{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return BlockResult{}, fmt.Errorf("block: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	blocker, blocked, err := blockPair(ctx, tx, gameID, blockerID, blockedID)
	if err != nil {
		return BlockResult{}, err
	}
	res := BlockResult{Blocker: blocker, Blocked: blocked}

	// 1. The block. ON CONFLICT: a second block of the same player is a no-op.
	tag, err := tx.Exec(ctx,
		`insert into blocks (game_id, blocker_id, blocked_id) values ($1, $2, $3) on conflict do nothing`,
		gameID, blocker.ID, blocked.ID)
	if err != nil {
		return BlockResult{}, fmt.Errorf("block: %w", err)
	}
	res.Created = tag.RowsAffected() == 1

	// 2. No friendship survives a block.
	f := Friendship{}
	err = tx.QueryRow(ctx,
		`delete from friendships
		 where game_id = $1 and player_a_id = least($2::uuid, $3::uuid) and player_b_id = greatest($2::uuid, $3::uuid)
		 returning created_at`,
		gameID, blocker.ID, blocked.ID).Scan(&f.CreatedAt)
	switch {
	case err == nil:
		f.PlayerA, f.PlayerB = blocker, blocked
		if f.PlayerB.ID < f.PlayerA.ID {
			f.PlayerA, f.PlayerB = f.PlayerB, f.PlayerA
		}
		res.RemovedFriendship = &f
	case !errors.Is(err, pgx.ErrNoRows):
		return BlockResult{}, fmt.Errorf("block: remove friendship: %w", err)
	}

	// 3. Nor a pending request, whoever sent it.
	req := FriendRequest{}
	var senderID string
	err = tx.QueryRow(ctx,
		`delete from friend_requests
		 where game_id = $1
		   and least(sender_id, receiver_id) = least($2::uuid, $3::uuid)
		   and greatest(sender_id, receiver_id) = greatest($2::uuid, $3::uuid)
		 returning id::text, sender_id::text, created_at`,
		gameID, blocker.ID, blocked.ID).Scan(&req.ID, &senderID, &req.CreatedAt)
	switch {
	case err == nil:
		req.Sender, req.Receiver = blocker, blocked
		if senderID == blocked.ID {
			req.Sender, req.Receiver = blocked, blocker
		}
		res.RemovedRequest = &req
	case !errors.Is(err, pgx.ErrNoRows):
		return BlockResult{}, fmt.Errorf("block: remove friend request: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return BlockResult{}, fmt.Errorf("block: %w", err)
	}
	return res, nil
}

// UnblockResult is what Unblock did.
type UnblockResult struct {
	Blocker FriendPlayer
	Blocked FriendPlayer
	// Removed is false when there was no block: nothing changed, so no event.
	Removed bool
}

// Unblock removes blockerID's block of blockedID. Unblocking someone who is not blocked changes
// nothing (Removed false, no error). The friendship a block ended stays ended.
//
// Errors: ErrBlockSelf, ErrPlayerNotFound.
func (r *Repository) Unblock(ctx context.Context, gameID, blockerID, blockedID string) (UnblockResult, error) {
	if r.pool == nil {
		return UnblockResult{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return UnblockResult{}, fmt.Errorf("unblock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	blocker, blocked, err := blockPair(ctx, tx, gameID, blockerID, blockedID)
	if err != nil {
		return UnblockResult{}, err
	}
	tag, err := tx.Exec(ctx,
		`delete from blocks where game_id = $1 and blocker_id = $2 and blocked_id = $3`,
		gameID, blocker.ID, blocked.ID)
	if err != nil {
		return UnblockResult{}, fmt.Errorf("unblock: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return UnblockResult{}, fmt.Errorf("unblock: %w", err)
	}
	return UnblockResult{Blocker: blocker, Blocked: blocked, Removed: tag.RowsAffected() == 1}, nil
}

// blockPair reads both players (with their names, in the database's own ID spelling) and
// takes the pair lock, so a block never runs at the same time as a friend change of the pair.
func blockPair(ctx context.Context, tx pgx.Tx, gameID, blockerID, blockedID string) (FriendPlayer, FriendPlayer, error) {
	blocker, err := friendPlayer(ctx, tx, gameID, blockerID)
	if err != nil {
		return FriendPlayer{}, FriendPlayer{}, blockError(err)
	}
	blocked, err := friendPlayer(ctx, tx, gameID, blockedID)
	if err != nil {
		return FriendPlayer{}, FriendPlayer{}, blockError(err)
	}
	if blocker.ID == blocked.ID {
		return FriendPlayer{}, FriendPlayer{}, ErrBlockSelf
	}
	if _, err := tx.Exec(ctx, lockPairSQL, gameID, blocker.ID, blocked.ID); err != nil {
		return FriendPlayer{}, FriendPlayer{}, fmt.Errorf("lock pair: %w", err)
	}
	return blocker, blocked, nil
}

// blockError turns "no such player" and "not a UUID" into ErrPlayerNotFound.
func blockError(err error) error {
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02") {
		return ErrPlayerNotFound
	}
	return fmt.Errorf("find player: %w", err)
}

// BlockedPlayer is one player someone blocked.
type BlockedPlayer struct {
	FriendPlayer
	Since time.Time
}

// ListBlocks returns the players playerID blocked in gameID, newest first. Only the blocker
// sees this list: a blocked player is never told.
func (r *Repository) ListBlocks(ctx context.Context, gameID, playerID string) ([]BlockedPlayer, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select p.id::text, p.display_name, b.created_at
		 from blocks b
		 join players p on p.id = b.blocked_id and p.game_id = b.game_id
		 where b.game_id = $1 and b.blocker_id = $2
		 order by b.created_at desc, p.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("list blocks: %w", err)
	}
	defer rows.Close()
	out := []BlockedPlayer{}
	for rows.Next() {
		var b BlockedPlayer
		if err := rows.Scan(&b.ID, &b.DisplayName, &b.Since); err != nil {
			return nil, fmt.Errorf("list blocks: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// Event types published by the blocks feature. Realtime keeps its own copy of the blocks, so it
// can stop direct messages between the two players.
const (
	EventPlayerBlocked   = "player.blocked"   // {blocker_id, blocked_id}
	EventPlayerUnblocked = "player.unblocked" // {blocker_id, blocked_id}
)

// BlockedPlayer is a player the calling player blocked, and since when.
type BlockedPlayer struct {
	FriendPlayer
	Since time.Time
}

// BlockPlayer makes the calling player block playerID, a player of the same game. Blocking is
// one-way: the other player can still block, or not block, the caller.
//
// A block also ends what it does not allow, in the same transaction: the two players'
// friendship and their pending friend request, in either direction. After the commit it
// publishes player.blocked, plus one event for what the block ended:
//   - friend.removed, with removed_by the caller, when they were friends;
//   - friend.request_cancelled, when the caller had sent the request;
//   - friend.request_declined, when the other player had sent it.
//
// Blocking a player who is already blocked changes nothing and publishes nothing.
//
// Errors the client can act on:
//   - 400 invalid_player_id: playerID is empty
//   - 400 cannot_block_self: playerID is the caller
//   - 404 player_not_found: no such player in the caller's game (a player of another game,
//     or an ID that is not a UUID, counts as not found)
func (s *Service) BlockPlayer(ctx context.Context, gameID, externalID, displayName, playerID string) error {
	playerID, err := blockTarget(playerID)
	if err != nil {
		return err
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return fmt.Errorf("block player: %w", err)
	}
	res, err := s.store.Block(ctx, gameID, me.ID, playerID)
	if err != nil {
		return blockErr("block player", err)
	}

	if res.Created {
		_ = s.events.Publish(ctx, events.New(EventPlayerBlocked, gameID, blockData(res.Blocker, res.Blocked)))
	}
	if f := res.RemovedFriendship; f != nil {
		_ = s.events.Publish(ctx, events.New(EventFriendRemoved, gameID, map[string]any{
			"players":    []map[string]any{playerData(f.PlayerA), playerData(f.PlayerB)},
			"removed_by": res.Blocker.ID,
		}))
	}
	if req := res.RemovedRequest; req != nil {
		typ := EventFriendRequestDeclined // the blocked player had asked the caller
		if req.Sender.ID == res.Blocker.ID {
			typ = EventFriendRequestCancelled // the caller had asked the blocked player
		}
		_ = s.events.Publish(ctx, events.New(typ, gameID, requestData(*req)))
	}
	return nil
}

// UnblockPlayer removes the calling player's block of playerID and publishes player.unblocked.
// It does not bring back a friendship or request the block ended. Unblocking a player who is
// not blocked changes nothing and publishes nothing.
//
// Errors: the same as BlockPlayer (400 invalid_player_id, 400 cannot_block_self,
// 404 player_not_found).
func (s *Service) UnblockPlayer(ctx context.Context, gameID, externalID, displayName, playerID string) error {
	playerID, err := blockTarget(playerID)
	if err != nil {
		return err
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return fmt.Errorf("unblock player: %w", err)
	}
	res, err := s.store.Unblock(ctx, gameID, me.ID, playerID)
	if err != nil {
		return blockErr("unblock player", err)
	}
	if res.Removed {
		_ = s.events.Publish(ctx, events.New(EventPlayerUnblocked, gameID, blockData(res.Blocker, res.Blocked)))
	}
	return nil
}

// ListBlocks returns the players the calling player blocked, newest block first. The list is
// empty, never nil, when there are none.
func (s *Service) ListBlocks(ctx context.Context, gameID, externalID, displayName string) ([]BlockedPlayer, error) {
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return nil, fmt.Errorf("list blocks: %w", err)
	}
	rows, err := s.store.ListBlocks(ctx, gameID, me.ID)
	if err != nil {
		return nil, fmt.Errorf("list blocks: %w", err)
	}
	out := make([]BlockedPlayer, 0, len(rows))
	for _, b := range rows {
		out = append(out, BlockedPlayer{FriendPlayer: FriendPlayer(b.FriendPlayer), Since: b.Since})
	}
	return out, nil
}

// blockTarget checks the player ID of a block or unblock and returns it in lower case.
func blockTarget(playerID string) (string, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_player_id", "playerId is required")
	}
	if !uuidPattern.MatchString(playerID) {
		return "", errPlayerNotFound()
	}
	return strings.ToLower(playerID), nil
}

// blockErr turns a repository error of Block or Unblock into the client's error.
func blockErr(op string, err error) error {
	switch {
	case errors.Is(err, repository.ErrBlockSelf):
		return httpx.NewError(http.StatusBadRequest, "cannot_block_self", "You cannot block yourself")
	case errors.Is(err, repository.ErrPlayerNotFound):
		return errPlayerNotFound()
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// blockData is the data of player.blocked and player.unblocked (docs/event-messages.md).
func blockData(blocker, blocked repository.FriendPlayer) map[string]any {
	return map[string]any{"blocker_id": blocker.ID, "blocked_id": blocked.ID}
}

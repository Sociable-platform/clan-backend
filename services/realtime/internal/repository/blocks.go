package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// ConversationInfo is what the message rules need to know about a conversation.
type ConversationInfo struct {
	Kind     string   // "dm" or "clan"
	Members  []string // the IDs of all members, the asking player included
	ReadOnly bool     // a DM whose two players are not friends any more: nobody can send (TEAM-283)
}

// ConversationOf returns the kind and the members of a conversation, for a player who must be one of them.
// It returns ErrNotMember when the player is not a member, the conversation does not exist, or it belongs to
// another game: the same answer for all, so nobody can find out which conversations exist.
func (r *Repository) ConversationOf(ctx context.Context, gameID, conversationID, playerID string) (ConversationInfo, error) {
	if r.pool == nil {
		return ConversationInfo{}, ErrNoDatabase
	}
	var info ConversationInfo
	err := r.db(ctx).QueryRow(ctx, `
		SELECT c.kind, c.read_only_since IS NOT NULL, array_agg(cm.player_id::text ORDER BY cm.player_id)
		FROM conversations c
		JOIN conversation_members cm ON cm.conversation_id = c.id
		WHERE c.game_id = $1 AND c.id = $2
		GROUP BY c.kind, c.read_only_since`, gameID, conversationID).Scan(&info.Kind, &info.ReadOnly, &info.Members)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationInfo{}, ErrNotMember
	}
	if err != nil {
		return ConversationInfo{}, fmt.Errorf("load conversation: %w", err)
	}
	if !slices.Contains(info.Members, playerID) {
		return ConversationInfo{}, ErrNotMember
	}
	return info, nil
}

// AddBlock records that blockerID blocked blockedID in the game. Doing it again changes nothing.
func (r *Repository) AddBlock(ctx context.Context, gameID, blockerID, blockedID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	_, err := r.db(ctx).Exec(ctx, `
		INSERT INTO blocks (game_id, blocker_id, blocked_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, gameID, blockerID, blockedID)
	if err != nil {
		return fmt.Errorf("add block: %w", err)
	}
	return nil
}

// RemoveBlock deletes the block of blockedID by blockerID. Doing it again, or for a block that was never
// there, changes nothing.
func (r *Repository) RemoveBlock(ctx context.Context, gameID, blockerID, blockedID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	_, err := r.db(ctx).Exec(ctx, `
		DELETE FROM blocks WHERE game_id = $1 AND blocker_id = $2 AND blocked_id = $3`, gameID, blockerID, blockedID)
	if err != nil {
		return fmt.Errorf("remove block: %w", err)
	}
	return nil
}

// IsBlocked reports whether one of the two players blocked the other in the game. The order of a and b does not matter.
func (r *Repository) IsBlocked(ctx context.Context, gameID, a, b string) (bool, error) {
	if r.pool == nil {
		return false, ErrNoDatabase
	}
	var blocked bool
	err := r.db(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM blocks
			WHERE game_id = $1
			  AND ((blocker_id = $2 AND blocked_id = $3) OR (blocker_id = $3 AND blocked_id = $2)))`,
		gameID, a, b).Scan(&blocked)
	if err != nil {
		return false, fmt.Errorf("check block: %w", err)
	}
	return blocked, nil
}

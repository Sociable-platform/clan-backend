package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotMember means the sender is not a member of the conversation in that game. The same
// error is returned when the conversation does not exist or belongs to another game, so callers
// cannot tell the cases apart.
var ErrNotMember = errors.New("not a member of the conversation")

// NewMessage is a message to store.
type NewMessage struct {
	ID         string // a UUIDv7 made by the caller
	SenderName string // the sender's name now; history keeps it
	Body       string
	CreatedAt  time.Time
}

// InsertMessage stores a message from senderID in the conversation and returns the IDs of all
// members of the conversation (the sender included), in one transaction.
//
// Membership and insert are one statement, so a sender who was removed a moment ago cannot slip
// a message in between a check and the insert. Every table access filters by gameID.
func (r *Repository) InsertMessage(ctx context.Context, gameID, conversationID, senderID string, m NewMessage) (members []string, err error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin insert message: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing after Commit

	tag, err := tx.Exec(ctx, `
		INSERT INTO messages (id, conversation_id, game_id, sender_id, sender_name, body, created_at)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (
			SELECT 1 FROM conversation_members
			WHERE game_id = $3 AND conversation_id = $2 AND player_id = $4)`,
		m.ID, conversationID, gameID, senderID, m.SenderName, m.Body, m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotMember
	}

	rows, err := tx.Query(ctx, `
		SELECT player_id FROM conversation_members
		WHERE game_id = $1 AND conversation_id = $2`, gameID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	members, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit insert message: %w", err)
	}
	return members, nil
}

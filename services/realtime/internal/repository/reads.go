package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoSuchMessage means a message ID is not a message of the conversation (in that game).
var ErrNoSuchMessage = errors.New("not a message of the conversation")

// PlayerConversation is one conversation of a player, as the conversation list shows it.
type PlayerConversation struct {
	ID          string
	Kind        string  // "dm" or "clan"
	ClanID      *string // set for "clan", nil for "dm"
	Members     []Member
	LastMessage *Message // the newest message; nil while nobody has written
	Unread      int      // messages of other players after the player's read position, at most 99
}

// ListConversations returns every conversation playerID is a member of in the game, most recently
// active first (the newest message, or the creation of a conversation nobody wrote in yet).
// It never returns a conversation the player is not a member of, or one of another game.
func (r *Repository) ListConversations(ctx context.Context, gameID, playerID string) ([]PlayerConversation, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	// One query for the conversations with their last message and the player's own unread count, and
	// one for the members of those conversations. Both filter by game_id.
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.kind, c.clan_id,
		       lm.id, lm.sender_id, lm.sender_name, lm.body, lm.created_at,
		       (SELECT count(*) FROM (
		            SELECT 1 FROM messages m
		            WHERE m.game_id = c.game_id
		              AND m.conversation_id = c.id
		              AND m.sender_id <> me.player_id
		              AND (me.last_read_message_id IS NULL OR m.id > me.last_read_message_id)
		            LIMIT $3) unread)
		FROM conversation_members me
		JOIN conversations c ON c.id = me.conversation_id AND c.game_id = me.game_id
		LEFT JOIN LATERAL (
		    SELECT id, sender_id, sender_name, body, created_at
		    FROM messages
		    WHERE game_id = c.game_id AND conversation_id = c.id
		    ORDER BY id DESC
		    LIMIT 1) lm ON true
		WHERE me.game_id = $1 AND me.player_id = $2
		ORDER BY COALESCE(lm.created_at, c.created_at) DESC, c.id DESC`, gameID, playerID, maxUnread)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var out []PlayerConversation
	var ids []string
	for rows.Next() {
		var c PlayerConversation
		var (
			msgID, msgSender, msgName, msgBody *string
			msgAt                              *time.Time
		)
		if err := rows.Scan(&c.ID, &c.Kind, &c.ClanID, &msgID, &msgSender, &msgName, &msgBody, &msgAt, &c.Unread); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		if msgID != nil {
			c.LastMessage = &Message{ID: *msgID, SenderID: *msgSender, SenderName: *msgName, Body: *msgBody, CreatedAt: *msgAt}
		}
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read conversations: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}

	members, err := r.pool.Query(ctx, `
		SELECT conversation_id, player_id, display_name
		FROM conversation_members
		WHERE game_id = $1 AND conversation_id = ANY($2::uuid[])
		ORDER BY joined_at, player_id`, gameID, ids)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer members.Close()
	byConversation := map[string][]Member{}
	for members.Next() {
		var conversationID string
		var m Member
		if err := members.Scan(&conversationID, &m.PlayerID, &m.Name); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		byConversation[conversationID] = append(byConversation[conversationID], m)
	}
	if err := members.Err(); err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	for i := range out {
		out[i].Members = byConversation[out[i].ID]
	}
	return out, nil
}

// ListMessages returns one page of a conversation's history, newest first: at most limit messages
// older than the message with ID before (all of them when before is empty), and whether older ones
// exist. It returns ErrNotMember when playerID is not a member of the conversation in that game,
// which is also the answer for an unknown conversation or one of another game.
//
// The membership check and the page are read in one transaction, so they see the same data.
func (r *Repository) ListMessages(ctx context.Context, gameID, playerID, conversationID, before string, limit int) (msgs []Message, hasMore bool, err error) {
	if r.pool == nil {
		return nil, false, ErrNoDatabase
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, false, fmt.Errorf("begin list messages: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var member bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM conversation_members
			WHERE game_id = $1 AND conversation_id = $2 AND player_id = $3)`,
		gameID, conversationID, playerID).Scan(&member)
	if err != nil {
		return nil, false, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return nil, false, ErrNotMember
	}

	var beforeID *string // nil: no upper bound
	if before != "" {
		beforeID = &before
	}
	// One more than asked for tells whether older messages exist. Message IDs are UUIDv7, so
	// ordering by ID is ordering by time (and uses the (conversation_id, id DESC) index).
	rows, err := tx.Query(ctx, `
		SELECT id, sender_id, sender_name, body, created_at
		FROM messages
		WHERE game_id = $1 AND conversation_id = $2 AND ($3::uuid IS NULL OR id < $3::uuid)
		ORDER BY id DESC
		LIMIT $4`, gameID, conversationID, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	msgs = make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SenderID, &m.SenderName, &m.Body, &m.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read messages: %w", err)
	}
	if len(msgs) > limit {
		msgs, hasMore = msgs[:limit], true
	}
	return msgs, hasMore, nil
}

// MarkRead records that playerID has read conversationID up to messageID, so the unread count
// drops. The read position only moves forward: an older messageID than the stored one changes
// nothing, and running it again changes nothing.
//
// It returns ErrNotMember when playerID is not a member of the conversation in that game (also for
// an unknown conversation or one of another game), and ErrNoSuchMessage when messageID is not a
// message of that conversation. Both the check and the update are one statement.
func (r *Repository) MarkRead(ctx context.Context, gameID, playerID, conversationID, messageID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	var member, found bool
	// The UPDATE in the CTE runs even though only the two EXISTS are read. Under concurrent calls
	// the row lock makes the second UPDATE re-check "older than the new message" on the newest value.
	err := r.pool.QueryRow(ctx, `
		WITH me AS (
			SELECT 1 FROM conversation_members
			WHERE game_id = $1 AND conversation_id = $2 AND player_id = $3
		), msg AS (
			SELECT id FROM messages
			WHERE game_id = $1 AND conversation_id = $2 AND id = $4::uuid
		), upd AS (
			UPDATE conversation_members SET last_read_message_id = (SELECT id FROM msg)
			WHERE game_id = $1 AND conversation_id = $2 AND player_id = $3
			  AND EXISTS (SELECT 1 FROM msg)
			  AND (last_read_message_id IS NULL OR last_read_message_id < (SELECT id FROM msg))
		)
		SELECT EXISTS (SELECT 1 FROM me), EXISTS (SELECT 1 FROM msg)`,
		gameID, conversationID, playerID, messageID).Scan(&member, &found)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	if !member {
		return ErrNotMember
	}
	if !found {
		return ErrNoSuchMessage
	}
	return nil
}

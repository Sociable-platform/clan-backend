package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Member is a player in a conversation, with the name Realtime copied from the event.
type Member struct {
	PlayerID string
	Name     string
}

// Message is a row of the messages table.
type Message struct {
	ID         string
	Kind       string // "text", or a member line: "member_joined", "member_left", "member_kicked"
	SenderID   string // for a member line, the player who joined or left
	SenderName string
	Body       string // empty for a member line
	CreatedAt  time.Time
}

// DM is a direct-message conversation after UpsertDM or MakeDMReadOnly.
type DM struct {
	ConversationID string
	LastMessage    *Message       // the newest message; nil while nobody has written
	Unread         map[string]int // player ID -> unread messages (at most 99), for both members
	ReadOnly       bool           // the two are not friends any more: nobody can send (TEAM-283)
}

// maxUnread is the highest unread count anybody sees ("99+" in the SDK).
const maxUnread = 99

// UpsertDM makes sure the DM conversation with this dmKey exists in the game and that both
// players are members, with the given names. It can run any number of times for the same
// pair: the second run changes nothing (except a renamed player's name).
//
// A conversation that already exists is reused with its messages, so players who become
// friends again get their history back. Their read position is gone with their membership:
// the messages of the other player count as unread again. A read-only DM (see MakeDMReadOnly)
// opens again: the players are friends now, and they kept their membership and read position.
func (r *Repository) UpsertDM(ctx context.Context, gameID, dmKey string, a, b Member) (DM, error) {
	if r.pool == nil {
		return DM{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return DM{}, fmt.Errorf("begin upsert dm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing after Commit

	// The no-op update makes RETURNING give back the id of the row that was already there.
	var dm DM
	err = tx.QueryRow(ctx, `
		INSERT INTO conversations (game_id, kind, dm_key)
		VALUES ($1, 'dm', $2)
		ON CONFLICT (game_id, dm_key) DO UPDATE SET dm_key = EXCLUDED.dm_key, read_only_since = NULL
		RETURNING id`, gameID, dmKey).Scan(&dm.ConversationID)
	if err != nil {
		return DM{}, fmt.Errorf("upsert dm conversation: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO conversation_members (conversation_id, game_id, player_id, display_name)
		SELECT $1, $2, m.player_id, m.display_name
		FROM unnest($3::uuid[], $4::text[]) AS m(player_id, display_name)
		ON CONFLICT (conversation_id, player_id) DO UPDATE SET display_name = EXCLUDED.display_name`,
		dm.ConversationID, gameID,
		[]string{a.PlayerID, b.PlayerID}, []string{a.Name, b.Name})
	if err != nil {
		return DM{}, fmt.Errorf("upsert dm members: %w", err)
	}

	dm.LastMessage, err = lastMessage(ctx, tx, gameID, dm.ConversationID)
	if err != nil {
		return DM{}, err
	}
	dm.Unread, err = unreadCounts(ctx, tx, gameID, dm.ConversationID)
	if err != nil {
		return DM{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DM{}, fmt.Errorf("commit upsert dm: %w", err)
	}
	return dm, nil
}

// MakeDMReadOnly keeps the DM conversation with this dmKey and its members, and marks it read-only:
// the two players stopped being friends, by a removal or a block (TEAM-283). Both keep the history;
// nobody can send, and it does not count as a friendship (FriendsOf leaves it out). UpsertDM opens
// it again when they become friends again.
//
// changed is true when this call made it read-only; then dm has its last message and both members'
// unread counts. It is false when the game has no such conversation or it already was read-only
// (a redelivered friend.removed), and there is nothing to tell anybody.
func (r *Repository) MakeDMReadOnly(ctx context.Context, gameID, dmKey string) (dm DM, changed bool, err error) {
	if r.pool == nil {
		return DM{}, false, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return DM{}, false, fmt.Errorf("begin read-only dm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing after Commit

	err = tx.QueryRow(ctx, `
		UPDATE conversations SET read_only_since = now()
		WHERE game_id = $1 AND dm_key = $2 AND kind = 'dm' AND read_only_since IS NULL
		RETURNING id`, gameID, dmKey).Scan(&dm.ConversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DM{}, false, nil
	}
	if err != nil {
		return DM{}, false, fmt.Errorf("read-only dm: %w", err)
	}
	dm.ReadOnly = true
	if dm.LastMessage, err = lastMessage(ctx, tx, gameID, dm.ConversationID); err != nil {
		return DM{}, false, err
	}
	if dm.Unread, err = unreadCounts(ctx, tx, gameID, dm.ConversationID); err != nil {
		return DM{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DM{}, false, fmt.Errorf("commit read-only dm: %w", err)
	}
	return dm, true, nil
}

// RemoveDMMembers deletes both members of the DM conversation with this dmKey. The conversation
// and its messages stay, so the history is there if the players become friends again. An ended
// friendship no longer does this (MakeDMReadOnly keeps the DM); tests use it to leave a DM.
// It returns the conversation's ID, or found = false when the game has no such conversation.
// Running it again after it worked deletes nothing and returns the same ID.
func (r *Repository) RemoveDMMembers(ctx context.Context, gameID, dmKey string) (conversationID string, found bool, err error) {
	if r.pool == nil {
		return "", false, ErrNoDatabase
	}
	// One statement: the DELETE in the CTE runs even though only the SELECT is read.
	err = r.db(ctx).QueryRow(ctx, `
		WITH c AS (
			SELECT id FROM conversations WHERE game_id = $1 AND dm_key = $2
		), removed AS (
			DELETE FROM conversation_members
			WHERE game_id = $1 AND conversation_id IN (SELECT id FROM c)
		)
		SELECT id FROM c`, gameID, dmKey).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("remove dm members: %w", err)
	}
	return conversationID, true, nil
}

// FriendsOf returns the IDs of the players who share a DM conversation with the player in the game,
// ordered by ID (a pair has one DM, so nobody is listed twice). A read-only DM (the two stopped
// being friends) is not a friendship, so its other member is not listed.
func (r *Repository) FriendsOf(ctx context.Context, gameID, playerID string) ([]string, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx, `
		SELECT other.player_id
		FROM conversation_members me
		JOIN conversations c ON c.id = me.conversation_id AND c.game_id = me.game_id AND c.kind = 'dm'
		                    AND c.read_only_since IS NULL
		JOIN conversation_members other
		  ON other.conversation_id = me.conversation_id AND other.game_id = me.game_id AND other.player_id <> me.player_id
		WHERE me.game_id = $1 AND me.player_id = $2
		ORDER BY other.player_id`, gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	defer rows.Close()
	friends := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan friend: %w", err)
		}
		friends = append(friends, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read friends: %w", err)
	}
	return friends, nil
}

// RenamePlayer sets the display name stored with the player's memberships in this game. Other
// games' memberships of the same player ID are not touched. Running it again changes nothing.
func (r *Repository) RenamePlayer(ctx context.Context, gameID, playerID, name string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	_, err := r.db(ctx).Exec(ctx, `
		UPDATE conversation_members SET display_name = $3
		WHERE game_id = $1 AND player_id = $2 AND display_name <> $3`, gameID, playerID, name)
	if err != nil {
		return fmt.Errorf("rename player: %w", err)
	}
	return nil
}

// queryer is what pgx.Tx and the pool have in common for the read helpers.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func lastMessage(ctx context.Context, q queryer, gameID, conversationID string) (*Message, error) {
	var m Message
	err := q.QueryRow(ctx, `
		SELECT id, kind, sender_id, sender_name, body, created_at
		FROM messages
		WHERE game_id = $1 AND conversation_id = $2
		ORDER BY id DESC
		LIMIT 1`, gameID, conversationID).Scan(&m.ID, &m.Kind, &m.SenderID, &m.SenderName, &m.Body, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load last message: %w", err)
	}
	return &m, nil
}

// unreadCounts returns, for every member, how many messages from other players come after the
// member's last read message. Counting stops at maxUnread. Member lines (a join or a leave) are not
// counted: only kind "text".
func unreadCounts(ctx context.Context, q queryer, gameID, conversationID string) (map[string]int, error) {
	rows, err := q.Query(ctx, `
		SELECT cm.player_id,
		       (SELECT count(*) FROM (
		            SELECT 1 FROM messages m
		            WHERE m.game_id = cm.game_id
		              AND m.conversation_id = cm.conversation_id
		              AND m.kind = 'text'
		              AND m.sender_id <> cm.player_id
		              AND (cm.last_read_message_id IS NULL OR m.id > cm.last_read_message_id)
		            LIMIT $3) unread)
		FROM conversation_members cm
		WHERE cm.game_id = $1 AND cm.conversation_id = $2`, gameID, conversationID, maxUnread)
	if err != nil {
		return nil, fmt.Errorf("count unread: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan unread: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unread: %w", err)
	}
	return out, nil
}

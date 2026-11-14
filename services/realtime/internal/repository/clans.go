package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ClanRoom is the chat conversation of one clan after JoinClanRoom.
type ClanRoom struct {
	ConversationID string
	Members        []Member       // every member, the one who just joined included, in the order they joined
	LastMessage    *Message       // the newest message; nil while nobody has written
	Unread         map[string]int // player ID -> unread messages (at most 99), for every member
}

// JoinClanRoom makes sure the clan has its conversation in the game and that the player is a member
// of it, with the given name. clan.created (the owner) and clan.member_joined (everybody else) both
// use it, so a joiner whose clan.created was never seen still gets the room.
//
// It can run any number of times for the same player: the second run changes nothing (except a
// renamed player's name). The messages of the room stay as long as the clan does, so a player who
// left and joins again gets the whole history back, also what was written while they were away;
// their read position went with their membership, so the messages of the others count as unread.
func (r *Repository) JoinClanRoom(ctx context.Context, gameID, clanID string, m Member) (ClanRoom, error) {
	if r.pool == nil {
		return ClanRoom{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return ClanRoom{}, fmt.Errorf("begin join clan room: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing after Commit

	// The no-op update makes RETURNING give back the id of the row that was already there.
	var room ClanRoom
	err = tx.QueryRow(ctx, `
		INSERT INTO conversations (game_id, kind, clan_id)
		VALUES ($1, 'clan', $2)
		ON CONFLICT (game_id, clan_id) DO UPDATE SET clan_id = EXCLUDED.clan_id
		RETURNING id`, gameID, clanID).Scan(&room.ConversationID)
	if err != nil {
		return ClanRoom{}, fmt.Errorf("upsert clan conversation: %w", err)
	}

	// A member who is there already keeps their joined_at and read position.
	_, err = tx.Exec(ctx, `
		INSERT INTO conversation_members (conversation_id, game_id, player_id, display_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (conversation_id, player_id) DO UPDATE SET display_name = EXCLUDED.display_name`,
		room.ConversationID, gameID, m.PlayerID, m.Name)
	if err != nil {
		return ClanRoom{}, fmt.Errorf("upsert clan member: %w", err)
	}

	if room.Members, err = members(ctx, tx, gameID, room.ConversationID); err != nil {
		return ClanRoom{}, err
	}
	if room.LastMessage, err = lastMessage(ctx, tx, gameID, room.ConversationID); err != nil {
		return ClanRoom{}, err
	}
	if room.Unread, err = unreadCounts(ctx, tx, gameID, room.ConversationID); err != nil {
		return ClanRoom{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClanRoom{}, fmt.Errorf("commit join clan room: %w", err)
	}
	return room, nil
}

// LeaveClanRoom deletes the player's membership of the clan's conversation. The conversation and
// its messages stay for the members who are still there (and for the player, should they join
// again). It returns the conversation's ID, or found = false when the game has no conversation
// for the clan. Running it again, or for a player who was not a member, changes nothing and still
// reports the conversation.
func (r *Repository) LeaveClanRoom(ctx context.Context, gameID, clanID, playerID string) (conversationID string, found bool, err error) {
	if r.pool == nil {
		return "", false, ErrNoDatabase
	}
	err = r.db(ctx).QueryRow(ctx, `
		WITH room AS (
		    SELECT id FROM conversations
		    WHERE game_id = $1 AND kind = 'clan' AND clan_id = $2),
		gone AS (
		    DELETE FROM conversation_members cm
		    USING room
		    WHERE cm.game_id = $1 AND cm.conversation_id = room.id AND cm.player_id = $3)
		SELECT id FROM room`, gameID, clanID, playerID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("leave clan room: %w", err)
	}
	return conversationID, true, nil
}

// DeleteClanRoom deletes the clan's conversation with its members and messages, and returns the
// conversation's ID and the IDs of the members it had. found is false when the game has no
// conversation for the clan, which is also what a second run reports.
func (r *Repository) DeleteClanRoom(ctx context.Context, gameID, clanID string) (conversationID string, memberIDs []string, found bool, err error) {
	if r.pool == nil {
		return "", nil, false, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return "", nil, false, fmt.Errorf("begin delete clan room: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing after Commit

	// The lock makes a member who joins at the same moment wait until the room is gone, so the
	// list below is everybody who has to be told.
	err = tx.QueryRow(ctx, `
		SELECT id FROM conversations
		WHERE game_id = $1 AND kind = 'clan' AND clan_id = $2
		FOR UPDATE`, gameID, clanID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("load clan conversation: %w", err)
	}
	all, err := members(ctx, tx, gameID, conversationID)
	if err != nil {
		return "", nil, false, err
	}
	// Members and messages go with the conversation (ON DELETE CASCADE).
	if _, err := tx.Exec(ctx, `DELETE FROM conversations WHERE game_id = $1 AND id = $2`, gameID, conversationID); err != nil {
		return "", nil, false, fmt.Errorf("delete clan conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, false, fmt.Errorf("commit delete clan room: %w", err)
	}
	memberIDs = make([]string, 0, len(all))
	for _, m := range all {
		memberIDs = append(memberIDs, m.PlayerID)
	}
	return conversationID, memberIDs, true, nil
}

// members returns the members of a conversation in the order they joined.
func members(ctx context.Context, q queryer, gameID, conversationID string) ([]Member, error) {
	rows, err := q.Query(ctx, `
		SELECT player_id, display_name
		FROM conversation_members
		WHERE game_id = $1 AND conversation_id = $2
		ORDER BY joined_at, player_id`, gameID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PlayerID, &m.Name); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	return out, nil
}

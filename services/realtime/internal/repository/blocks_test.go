package repository

import (
	"context"
	"errors"
	"testing"
)

func TestConversationOfGivesKindAndMembersToAMemberOnly(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newUUID(t), newUUID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}

	info, err := r.ConversationOf(ctx, game, dm.ConversationID, memberA.PlayerID)
	if err != nil || info.Kind != "dm" || len(info.Members) != 2 || info.Members[0] != memberA.PlayerID || info.Members[1] != memberB.PlayerID {
		t.Fatalf("info = %+v, %v, want a dm with both members", info, err)
	}

	stranger := newUUID(t)
	for name, c := range map[string]struct{ game, conv, player string }{
		"not a member":             {game, dm.ConversationID, stranger},
		"another game":             {otherGame, dm.ConversationID, memberA.PlayerID},
		"conversation is unknown":  {game, newUUID(t), memberA.PlayerID},
		"removed from the friends": {game, dm.ConversationID, memberA.PlayerID},
	} {
		if name == "removed from the friends" {
			if _, _, err := r.RemoveDMMembers(ctx, game, testKey); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.ConversationOf(ctx, c.game, c.conv, c.player); !errors.Is(err, ErrNotMember) {
			t.Errorf("%s: err = %v, want ErrNotMember", name, err)
		}
	}
}

func TestConversationOfAClanRoom(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	var id string
	if err := r.pool.QueryRow(ctx, "insert into conversations (game_id, kind, clan_id) values ($1, 'clan', $2) returning id", game, newUUID(t)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Member{memberA, memberB} {
		if _, err := r.pool.Exec(ctx, "insert into conversation_members (conversation_id, game_id, player_id, display_name) values ($1, $2, $3, $4)", id, game, m.PlayerID, m.Name); err != nil {
			t.Fatal(err)
		}
	}
	info, err := r.ConversationOf(ctx, game, id, memberB.PlayerID)
	if err != nil || info.Kind != "clan" || len(info.Members) != 2 {
		t.Fatalf("info = %+v, %v, want a clan room with two members", info, err)
	}
}

func TestBlocksInEitherDirectionAndPerGame(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newUUID(t), newUUID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	a, b := memberA.PlayerID, memberB.PlayerID

	if blocked, err := r.IsBlocked(ctx, game, a, b); err != nil || blocked {
		t.Fatalf("blocked = %v, %v before any block", blocked, err)
	}
	for i := 0; i < 2; i++ { // twice: the second one changes nothing
		if err := r.AddBlock(ctx, game, a, b); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		if blocked, err := r.IsBlocked(ctx, game, pair[0], pair[1]); err != nil || !blocked {
			t.Errorf("IsBlocked(%s, %s) = %v, %v, want true in both directions", pair[0][:4], pair[1][:4], blocked, err)
		}
	}
	if blocked, _ := r.IsBlocked(ctx, otherGame, a, b); blocked {
		t.Error("a block in one game applies in another")
	}
	third := newUUID(t)
	if blocked, _ := r.IsBlocked(ctx, game, a, third); blocked {
		t.Error("a block of B also blocks A and a third player")
	}

	if err := r.RemoveBlock(ctx, game, b, a); err != nil { // B did not block A: nothing to remove
		t.Fatal(err)
	}
	if blocked, _ := r.IsBlocked(ctx, game, a, b); !blocked {
		t.Error("removing a block that B never made removed the block of A")
	}
	for i := 0; i < 2; i++ {
		if err := r.RemoveBlock(ctx, game, a, b); err != nil {
			t.Fatal(err)
		}
	}
	if blocked, _ := r.IsBlocked(ctx, game, a, b); blocked {
		t.Error("still blocked after the unblock")
	}
	_, _ = r.pool.Exec(ctx, "delete from blocks where game_id = any($1)", []string{game, otherGame})
}

func TestBlocksWithoutDatabase(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	if _, err := r.ConversationOf(ctx, "g", "c", "p"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("ConversationOf: %v", err)
	}
	if err := r.AddBlock(ctx, "g", "a", "b"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("AddBlock: %v", err)
	}
	if err := r.RemoveBlock(ctx, "g", "a", "b"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("RemoveBlock: %v", err)
	}
	if _, err := r.IsBlocked(ctx, "g", "a", "b"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("IsBlocked: %v", err)
	}
}

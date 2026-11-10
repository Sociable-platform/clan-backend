package repository

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
)

// These tests talk to a real Postgres with the realtime migrations applied. They run only when
// REALTIME_TEST_DATABASE_URL is set (see README "Integration tests"), otherwise they are skipped.
func testRepo(t *testing.T) (*Repository, func(gameIDs ...string)) {
	t.Helper()
	url := os.Getenv("REALTIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("REALTIME_TEST_DATABASE_URL is not set; skipping database test")
	}
	pool, err := pg.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(gameIDs ...string) {
		for _, g := range gameIDs {
			// Members and messages go with their conversation.
			_, _ = pool.Exec(context.Background(), "delete from conversations where game_id = $1", g)
		}
		pool.Close()
	}
	return New(pool), cleanup
}

// newUUID makes a random UUID so test runs never collide with each other or real data.
func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const testKey = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

var (
	memberA = Member{PlayerID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Ali"}
	memberB = Member{PlayerID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "Rashad"}
)

func (r *Repository) countMembers(t *testing.T, gameID, conversationID string) int {
	t.Helper()
	var n int
	err := r.pool.QueryRow(context.Background(),
		"select count(*) from conversation_members where game_id = $1 and conversation_id = $2", gameID, conversationID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *Repository) addMessage(t *testing.T, gameID, conversationID, id, senderID, senderName, body string) {
	t.Helper()
	_, err := r.pool.Exec(context.Background(), `
		insert into messages (id, conversation_id, game_id, sender_id, sender_name, body)
		values ($1, $2, $3, $4, $5, $6)`, id, conversationID, gameID, senderID, senderName, body)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpsertDMTwiceChangesNothing(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	first, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConversationID == "" || first.LastMessage != nil || first.Unread[memberA.PlayerID] != 0 || len(first.Unread) != 2 {
		t.Fatalf("first = %+v, want a new empty conversation with two members", first)
	}

	// The same event again, with the two players in the other order.
	again, err := r.UpsertDM(ctx, game, testKey, memberB, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if again.ConversationID != first.ConversationID {
		t.Errorf("second ID = %s, first = %s: a redelivered event made a second conversation", again.ConversationID, first.ConversationID)
	}
	if n := r.countMembers(t, game, first.ConversationID); n != 2 {
		t.Errorf("members = %d, want 2", n)
	}
}

func TestUpsertDMKeepsTheLatestNames(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	renamed := Member{PlayerID: memberA.PlayerID, Name: "Ali the Great"}
	if _, err := r.UpsertDM(ctx, game, testKey, renamed, memberB); err != nil {
		t.Fatal(err)
	}
	var name string
	err = r.pool.QueryRow(ctx, "select display_name from conversation_members where conversation_id = $1 and player_id = $2",
		dm.ConversationID, memberA.PlayerID).Scan(&name)
	if err != nil || name != "Ali the Great" {
		t.Errorf("name = %q, %v, want the new name", name, err)
	}
}

func TestUpsertDMConcurrentRedeliveryMakesOneConversation(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)

	var wg sync.WaitGroup
	ids := make([]string, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dm, err := r.UpsertDM(context.Background(), game, testKey, memberA, memberB)
			ids[i], errs[i] = dm.ConversationID, err
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("call %d got conversation %s, call 0 got %s", i, ids[i], ids[0])
		}
	}
	if n := r.countMembers(t, game, ids[0]); n != 2 {
		t.Errorf("members = %d, want 2", n)
	}
}

func TestGamesAreSeparate(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()

	// The same two player IDs in two games: each game gets its own conversation and nothing leaks.
	dmA, err := r.UpsertDM(ctx, gameA, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	dmB, err := r.UpsertDM(ctx, gameB, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if dmA.ConversationID == dmB.ConversationID {
		t.Fatal("two games share one conversation")
	}

	// Removing in game A leaves game B alone.
	if id, found, err := r.RemoveDMMembers(ctx, gameA, testKey); err != nil || !found || id != dmA.ConversationID {
		t.Fatalf("remove in A = %q, %v, %v", id, found, err)
	}
	if n := r.countMembers(t, gameB, dmB.ConversationID); n != 2 {
		t.Errorf("game B members = %d, want 2: a removal in game A reached game B", n)
	}
	// A game that has no such conversation finds nothing, even though another game does.
	if id, found, err := r.RemoveDMMembers(ctx, newUUID(t), testKey); err != nil || found || id != "" {
		t.Errorf("remove in an unknown game = %q, %v, %v, want not found", id, found, err)
	}
}

func TestRemoveDMMembersKeepsMessagesAndIsRepeatable(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	r.addMessage(t, game, dm.ConversationID, "00000000-0000-7000-8000-000000000001", memberB.PlayerID, "Rashad", "gg")

	for i := 0; i < 2; i++ { // the second run is a redelivered friend.removed
		id, found, err := r.RemoveDMMembers(ctx, game, testKey)
		if err != nil || !found || id != dm.ConversationID {
			t.Fatalf("run %d = %q, %v, %v, want the conversation ID", i, id, found, err)
		}
		if n := r.countMembers(t, game, dm.ConversationID); n != 0 {
			t.Fatalf("run %d: members = %d, want 0", i, n)
		}
	}
	var messages int
	if err := r.pool.QueryRow(ctx, "select count(*) from messages where conversation_id = $1", dm.ConversationID).Scan(&messages); err != nil || messages != 1 {
		t.Errorf("messages = %d, %v, want the history kept", messages, err)
	}

	// Friends again: same conversation, the history is there, the old message is unread.
	back, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if back.ConversationID != dm.ConversationID {
		t.Errorf("new conversation %s, want the old one %s", back.ConversationID, dm.ConversationID)
	}
	if back.LastMessage == nil || back.LastMessage.Body != "gg" || back.LastMessage.SenderName != "Rashad" {
		t.Errorf("last message = %+v, want the old one", back.LastMessage)
	}
	if back.Unread[memberA.PlayerID] != 1 || back.Unread[memberB.PlayerID] != 0 {
		t.Errorf("unread = %v, want Ali 1 (Rashad wrote it), Rashad 0", back.Unread)
	}
}

func TestUpsertDMReportsLastMessageAndUnread(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	r.addMessage(t, game, dm.ConversationID, "00000000-0000-7000-8000-000000000001", memberA.PlayerID, "Ali", "one")
	r.addMessage(t, game, dm.ConversationID, "00000000-0000-7000-8000-000000000002", memberB.PlayerID, "Rashad", "two")
	r.addMessage(t, game, dm.ConversationID, "00000000-0000-7000-8000-000000000003", memberB.PlayerID, "Rashad", "three")

	// Ali has read up to message 2; only message 3 is unread. Rashad has read nothing, so Ali's message 1 counts.
	_, err = r.pool.Exec(ctx, "update conversation_members set last_read_message_id = $1 where conversation_id = $2 and player_id = $3",
		"00000000-0000-7000-8000-000000000002", dm.ConversationID, memberA.PlayerID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMessage == nil || got.LastMessage.Body != "three" {
		t.Errorf("last message = %+v, want \"three\"", got.LastMessage)
	}
	if got.Unread[memberA.PlayerID] != 1 || got.Unread[memberB.PlayerID] != 1 {
		t.Errorf("unread = %v, want 1 and 1", got.Unread)
	}
	// The read position survives a repeated event: it is only cleared when the membership is deleted.
}

func TestUnreadStopsAt99(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.pool.Exec(ctx, `
		insert into messages (id, conversation_id, game_id, sender_id, sender_name, body)
		select ('00000000-0000-7000-8000-' || lpad(i::text, 12, '0'))::uuid, $1, $2, $3, 'Rashad', 'spam'
		from generate_series(1, 150) as i`, dm.ConversationID, game, memberB.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unread[memberA.PlayerID] != 99 {
		t.Errorf("unread = %d, want 99 (the cap)", got.Unread[memberA.PlayerID])
	}
}

func (r *Repository) memberName(t *testing.T, gameID, conversationID, playerID string) string {
	t.Helper()
	var name string
	err := r.pool.QueryRow(context.Background(),
		"select display_name from conversation_members where game_id = $1 and conversation_id = $2 and player_id = $3",
		gameID, conversationID, playerID).Scan(&name)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRenamePlayerChangesOnlyThatPlayerInThatGame(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()

	// A has two conversations in game A (with B and with C); the same A is also in game B.
	memberC := Member{PlayerID: newUUID(t), Name: "Carol"}
	keyC := memberA.PlayerID + ":" + memberC.PlayerID
	withB, err := r.UpsertDM(ctx, gameA, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	withC, err := r.UpsertDM(ctx, gameA, keyC, memberA, memberC)
	if err != nil {
		t.Fatal(err)
	}
	otherGame, err := r.UpsertDM(ctx, gameB, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	r.addMessage(t, gameA, withB.ConversationID, "00000000-0000-7000-8000-000000000001", memberA.PlayerID, "Ali", "hi")

	for i := 0; i < 2; i++ { // the second run is a redelivered player.renamed
		if err := r.RenamePlayer(ctx, gameA, memberA.PlayerID, "Alice Brown"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	for _, c := range []string{withB.ConversationID, withC.ConversationID} {
		if got := r.memberName(t, gameA, c, memberA.PlayerID); got != "Alice Brown" {
			t.Errorf("name in game A conversation %s = %q, want the new name", c, got)
		}
	}
	if got := r.memberName(t, gameA, withB.ConversationID, memberB.PlayerID); got != "Rashad" {
		t.Errorf("the other member's name = %q, want it unchanged", got)
	}
	if got := r.memberName(t, gameB, otherGame.ConversationID, memberA.PlayerID); got != "Ali" {
		t.Errorf("name in game B = %q, want it unchanged: a rename in game A reached game B", got)
	}
	var sender string
	if err := r.pool.QueryRow(ctx, "select sender_name from messages where conversation_id = $1", withB.ConversationID).Scan(&sender); err != nil || sender != "Ali" {
		t.Errorf("sender_name = %q, %v, want the name the sender had when writing", sender, err)
	}

	// An unknown player or game is not an error: there is just nothing to change.
	if err := r.RenamePlayer(ctx, newUUID(t), memberA.PlayerID, "x"); err != nil {
		t.Errorf("unknown game: %v", err)
	}
	if err := r.RenamePlayer(ctx, gameA, newUUID(t), "x"); err != nil {
		t.Errorf("unknown player: %v", err)
	}
}

func TestConversationsWithoutDatabase(t *testing.T) {
	r := New(nil)
	if _, err := r.UpsertDM(context.Background(), "g", testKey, memberA, memberB); err == nil {
		t.Error("UpsertDM without a database succeeded")
	}
	if _, _, err := r.RemoveDMMembers(context.Background(), "g", testKey); err == nil {
		t.Error("RemoveDMMembers without a database succeeded")
	}
	if err := r.RenamePlayer(context.Background(), "g", memberA.PlayerID, "x"); err == nil {
		t.Error("RenamePlayer without a database succeeded")
	}
}

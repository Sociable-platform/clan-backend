package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mid makes message ID number n: UUIDs that sort in the order of n, like UUIDv7.
func mid(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }

// addMessageAt stores a message with a given time and returns its ID.
func (r *Repository) addMessageAt(t *testing.T, gameID, conversationID string, n int, senderID, senderName, body string, at time.Time) string {
	t.Helper()
	_, err := r.pool.Exec(context.Background(), `
		insert into messages (id, conversation_id, game_id, sender_id, sender_name, body, created_at)
		values ($1, $2, $3, $4, $5, $6, $7)`, mid(n), conversationID, gameID, senderID, senderName, body, at)
	if err != nil {
		t.Fatal(err)
	}
	return mid(n)
}

func (r *Repository) lastRead(t *testing.T, gameID, conversationID, playerID string) string {
	t.Helper()
	var id *string
	err := r.pool.QueryRow(context.Background(),
		"select last_read_message_id::text from conversation_members where game_id = $1 and conversation_id = $2 and player_id = $3",
		gameID, conversationID, playerID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		return ""
	}
	return *id
}

func ids(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListConversationsShowsMembersLastMessageAndUnread(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	memberC := Member{PlayerID: newUUID(t), Name: "Carol"}
	now := time.Now().UTC().Truncate(time.Microsecond)

	ab, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := r.UpsertDM(ctx, game, memberA.PlayerID+":"+memberC.PlayerID, memberA, memberC)
	if err != nil {
		t.Fatal(err)
	}
	// In AB: two messages from B, then one from A. In AC: one from Carol, the newest of all.
	r.addMessageAt(t, game, ab.ConversationID, 1, memberB.PlayerID, "Rashad", "one", now.Add(-4*time.Minute))
	r.addMessageAt(t, game, ab.ConversationID, 2, memberB.PlayerID, "Rashad", "two", now.Add(-3*time.Minute))
	r.addMessageAt(t, game, ab.ConversationID, 3, memberA.PlayerID, "Ali", "three", now.Add(-2*time.Minute))
	r.addMessageAt(t, game, ac.ConversationID, 4, memberC.PlayerID, "Carol", "four", now.Add(-1*time.Minute))

	list, err := r.ListConversations(ctx, game, memberA.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != ac.ConversationID || list[1].ID != ab.ConversationID {
		t.Fatalf("conversations = %+v, want [AC, AB]: the one with the newest message first", list)
	}
	gotAC, gotAB := list[0], list[1]
	if gotAC.Kind != "dm" || gotAC.ClanID != nil || gotAB.Kind != "dm" {
		t.Errorf("kind/clan = %q %v, want a dm without a clan", gotAC.Kind, gotAC.ClanID)
	}
	if gotAB.LastMessage == nil || gotAB.LastMessage.ID != mid(3) || gotAB.LastMessage.Body != "three" ||
		gotAB.LastMessage.SenderID != memberA.PlayerID || gotAB.LastMessage.SenderName != "Ali" || !gotAB.LastMessage.CreatedAt.Equal(now.Add(-2*time.Minute)) {
		t.Errorf("AB last message = %+v, want message 3 by Ali", gotAB.LastMessage)
	}
	if gotAB.Unread != 2 || gotAC.Unread != 1 {
		t.Errorf("unread AB = %d, AC = %d, want 2 and 1: only messages of the other player count", gotAB.Unread, gotAC.Unread)
	}
	if len(gotAB.Members) != 2 || gotAB.Members[0].PlayerID == gotAB.Members[1].PlayerID {
		t.Errorf("AB members = %+v, want both players once", gotAB.Members)
	}
	names := map[string]string{}
	for _, m := range gotAB.Members {
		names[m.PlayerID] = m.Name
	}
	if names[memberA.PlayerID] != "Ali" || names[memberB.PlayerID] != "Rashad" {
		t.Errorf("AB member names = %v", names)
	}

	// Rashad sees only his own conversation, with his own unread count (Ali's message).
	forB, err := r.ListConversations(ctx, game, memberB.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forB) != 1 || forB[0].ID != ab.ConversationID || forB[0].Unread != 1 {
		t.Errorf("Rashad's conversations = %+v, want only AB with 1 unread", forB)
	}
}

func TestListConversationsUnreadDropsWithTheReadPosition(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 3; i++ {
		r.addMessageAt(t, game, dm.ConversationID, i, memberB.PlayerID, "Rashad", "hi", now.Add(time.Duration(i)*time.Second))
	}
	unread := func() int {
		list, err := r.ListConversations(ctx, game, memberA.PlayerID)
		if err != nil || len(list) != 1 {
			t.Fatalf("list = %+v, %v", list, err)
		}
		return list[0].Unread
	}
	if got := unread(); got != 3 {
		t.Fatalf("unread = %d, want 3", got)
	}
	if err := r.MarkRead(ctx, game, memberA.PlayerID, dm.ConversationID, mid(2)); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 1 {
		t.Errorf("unread after reading up to 2 = %d, want 1", got)
	}
	if err := r.MarkRead(ctx, game, memberA.PlayerID, dm.ConversationID, mid(3)); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 0 {
		t.Errorf("unread after reading up to 3 = %d, want 0", got)
	}
	r.addMessageAt(t, game, dm.ConversationID, 4, memberB.PlayerID, "Rashad", "again", now.Add(10*time.Second))
	if got := unread(); got != 1 {
		t.Errorf("unread after a new message = %d, want 1", got)
	}
}

func TestListConversationsCapsUnreadAt99(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 105; i++ {
		r.addMessageAt(t, game, dm.ConversationID, i, memberB.PlayerID, "Rashad", "spam", now.Add(time.Duration(i)*time.Millisecond))
	}
	list, err := r.ListConversations(ctx, game, memberA.PlayerID)
	if err != nil || len(list) != 1 || list[0].Unread != 99 {
		t.Errorf("= %+v, %v, want 99 unread (the cap)", list, err)
	}
}

func TestListConversationsOrderingWithAnEmptyConversation(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	memberC := Member{PlayerID: newUUID(t), Name: "Carol"}
	ab, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	// The last message of AB is an hour old; AC is new and has no messages: it comes first, with no last message.
	r.addMessageAt(t, game, ab.ConversationID, 1, memberB.PlayerID, "Rashad", "old", time.Now().UTC().Add(-time.Hour))
	ac, err := r.UpsertDM(ctx, game, memberA.PlayerID+":"+memberC.PlayerID, memberA, memberC)
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.ListConversations(ctx, game, memberA.PlayerID)
	if err != nil || len(list) != 2 || list[0].ID != ac.ConversationID || list[0].LastMessage != nil || list[0].Unread != 0 {
		t.Errorf("= %+v, %v, want AC first, empty, 0 unread", list, err)
	}
}

func TestListConversationsIsolation(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()
	inA, err := r.UpsertDM(ctx, gameA, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	inB, err := r.UpsertDM(ctx, gameB, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	r.addMessageAt(t, gameA, inA.ConversationID, 1, memberB.PlayerID, "Rashad", "game A", time.Now().UTC())

	// The same player ID in two games sees only the conversations of the game asked for.
	for _, c := range []struct{ game, want string }{{gameA, inA.ConversationID}, {gameB, inB.ConversationID}} {
		list, err := r.ListConversations(ctx, c.game, memberA.PlayerID)
		if err != nil || len(list) != 1 || list[0].ID != c.want {
			t.Fatalf("game %s: %+v, %v, want only %s", c.game, list, err, c.want)
		}
		if (c.game == gameB) && (list[0].LastMessage != nil || list[0].Unread != 0) {
			t.Errorf("game B sees game A's messages: %+v", list[0])
		}
	}
	// A stranger, and a game without conversations, get nothing.
	if list, err := r.ListConversations(ctx, gameA, newUUID(t)); err != nil || len(list) != 0 {
		t.Errorf("stranger = %+v, %v, want nothing", list, err)
	}
	if list, err := r.ListConversations(ctx, newUUID(t), memberA.PlayerID); err != nil || len(list) != 0 {
		t.Errorf("unknown game = %+v, %v, want nothing", list, err)
	}
	// After the friendship ended the conversation is gone from the list (its messages stay stored).
	if _, _, err := r.RemoveDMMembers(ctx, gameA, testKey); err != nil {
		t.Fatal(err)
	}
	if list, err := r.ListConversations(ctx, gameA, memberA.PlayerID); err != nil || len(list) != 0 {
		t.Errorf("after the friendship ended = %+v, %v, want nothing", list, err)
	}
}

func TestListMessagesPagesNewestFirst(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 1; i <= 5; i++ {
		sender, name := memberA, "Ali"
		if i%2 == 0 {
			sender, name = memberB, "Rashad"
		}
		r.addMessageAt(t, game, dm.ConversationID, i, sender.PlayerID, name, fmt.Sprintf("message %d", i), now.Add(time.Duration(i)*time.Second))
	}

	page := func(before string, limit int) ([]string, bool) {
		t.Helper()
		msgs, more, err := r.ListMessages(ctx, game, memberA.PlayerID, dm.ConversationID, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		return ids(msgs), more
	}
	steps := []struct {
		name   string
		before string
		limit  int
		want   []string
		more   bool
	}{
		{"newest page", "", 2, []string{mid(5), mid(4)}, true},
		{"next page", mid(4), 2, []string{mid(3), mid(2)}, true},
		{"last page, one left", mid(2), 2, []string{mid(1)}, false},
		{"page that ends exactly at the oldest", mid(3), 2, []string{mid(2), mid(1)}, false},
		{"everything in one page", "", 5, []string{mid(5), mid(4), mid(3), mid(2), mid(1)}, false},
		{"limit larger than the history", "", 50, []string{mid(5), mid(4), mid(3), mid(2), mid(1)}, false},
		{"before the oldest", mid(1), 10, []string{}, false},
		{"before a newer message than any", mid(99), 2, []string{mid(5), mid(4)}, true},
		{"limit one", "", 1, []string{mid(5)}, true},
	}
	for _, s := range steps {
		got, more := page(s.before, s.limit)
		if !equal(got, s.want) || more != s.more {
			t.Errorf("%s: got %v more=%v, want %v more=%v", s.name, got, more, s.want, s.more)
		}
	}

	// A message has everything the SDK shows.
	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, dm.ConversationID, "", 1)
	if err != nil || len(msgs) != 1 {
		t.Fatal(msgs, err)
	}
	if m := msgs[0]; m.SenderID != memberA.PlayerID || m.SenderName != "Ali" || m.Body != "message 5" || !m.CreatedAt.Equal(now.Add(5*time.Second)) {
		t.Errorf("message = %+v", m)
	}
}

func TestListMessagesOnlyForMembersOfThatGame(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, gameA, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.UpsertDM(ctx, gameA, memberA.PlayerID+":"+newUUID(t), memberA, Member{PlayerID: newUUID(t), Name: "Carol"})
	if err != nil {
		t.Fatal(err)
	}
	r.addMessageAt(t, gameA, dm.ConversationID, 1, memberB.PlayerID, "Rashad", "secret", time.Now().UTC())
	r.addMessageAt(t, gameA, other.ConversationID, 2, memberA.PlayerID, "Ali", "other chat", time.Now().UTC())

	notMember := []struct {
		name       string
		game, conv string
		player     string
	}{
		{"a stranger", gameA, dm.ConversationID, newUUID(t)},
		{"the same players in another game", gameB, dm.ConversationID, memberA.PlayerID},
		{"an unknown conversation", gameA, newUUID(t), memberA.PlayerID},
	}
	for _, c := range notMember {
		msgs, more, err := r.ListMessages(ctx, c.game, c.player, c.conv, "", 50)
		if !errors.Is(err, ErrNotMember) || msgs != nil || more {
			t.Errorf("%s: = %v, %v, %v, want ErrNotMember and nothing", c.name, msgs, more, err)
		}
	}

	// A member sees only that conversation's messages, never another conversation's.
	msgs, _, err := r.ListMessages(ctx, gameA, memberA.PlayerID, dm.ConversationID, "", 50)
	if err != nil || !equal(ids(msgs), []string{mid(1)}) {
		t.Errorf("messages = %v, %v, want only message 1", ids(msgs), err)
	}

	// After the friendship ended nobody reads it (the messages stay stored).
	if _, _, err := r.RemoveDMMembers(ctx, gameA, testKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ListMessages(ctx, gameA, memberA.PlayerID, dm.ConversationID, "", 50); !errors.Is(err, ErrNotMember) {
		t.Errorf("after the friendship ended: err = %v, want ErrNotMember", err)
	}
}

func TestListMessagesKeepsMessagesOfAPlayerWhoLeft(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	r.addMessageAt(t, game, dm.ConversationID, 1, memberB.PlayerID, "Rashad", "bye", time.Now().UTC())
	// Rashad's membership goes (for example he left a clan); his message stays in the history.
	if _, err := r.pool.Exec(ctx, "delete from conversation_members where conversation_id = $1 and player_id = $2", dm.ConversationID, memberB.PlayerID); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, dm.ConversationID, "", 50)
	if err != nil || len(msgs) != 1 || msgs[0].SenderName != "Rashad" {
		t.Errorf("= %+v, %v, want Rashad's message still there", msgs, err)
	}
}

func TestListMessagesKeepsTextExactlyAsStored(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	body := `'; drop table messages; -- <script>alert(1)</script> "quotes" 😀 日本語`
	r.addMessageAt(t, game, dm.ConversationID, 1, memberB.PlayerID, "Rashad", body, time.Now().UTC())
	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, dm.ConversationID, "", 50)
	if err != nil || len(msgs) != 1 || msgs[0].Body != body {
		t.Errorf("= %+v, %v, want the text unchanged", msgs, err)
	}
}

func TestMarkReadOnlyMovesForwardAndIsRepeatable(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 5; i++ {
		r.addMessageAt(t, game, dm.ConversationID, i, memberB.PlayerID, "Rashad", "hi", now.Add(time.Duration(i)*time.Second))
	}
	a := memberA.PlayerID
	steps := []struct {
		markID string
		want   string
	}{
		{mid(3), mid(3)},
		{mid(3), mid(3)}, // repeated
		{mid(1), mid(3)}, // older: nothing changes
		{mid(2), mid(3)},
		{mid(5), mid(5)},
		{mid(4), mid(5)},
	}
	if got := r.lastRead(t, game, dm.ConversationID, a); got != "" {
		t.Fatalf("before any read: %q, want none", got)
	}
	for i, s := range steps {
		if err := r.MarkRead(ctx, game, a, dm.ConversationID, s.markID); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := r.lastRead(t, game, dm.ConversationID, a); got != s.want {
			t.Errorf("step %d (mark %s): position = %s, want %s", i, s.markID, got, s.want)
		}
	}
	// Rashad's own position is not Ali's.
	if got := r.lastRead(t, game, dm.ConversationID, memberB.PlayerID); got != "" {
		t.Errorf("Rashad's position = %q, want it untouched", got)
	}
}

func TestMarkReadRefusals(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB := newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()
	memberC := Member{PlayerID: newUUID(t), Name: "Carol"}
	ab, err := r.UpsertDM(ctx, gameA, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := r.UpsertDM(ctx, gameA, memberA.PlayerID+":"+memberC.PlayerID, memberA, memberC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpsertDM(ctx, gameB, testKey, memberA, memberB); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.addMessageAt(t, gameA, ab.ConversationID, 1, memberB.PlayerID, "Rashad", "in AB", now)
	r.addMessageAt(t, gameA, ac.ConversationID, 2, memberC.PlayerID, "Carol", "in AC", now.Add(time.Second))

	tests := []struct {
		name        string
		game, conv  string
		player, msg string
		want        error
	}{
		{"a stranger", gameA, ab.ConversationID, newUUID(t), mid(1), ErrNotMember},
		{"the same players in another game", gameB, ab.ConversationID, memberA.PlayerID, mid(1), ErrNotMember},
		{"an unknown conversation", gameA, newUUID(t), memberA.PlayerID, mid(1), ErrNotMember},
		{"a message of another conversation", gameA, ab.ConversationID, memberA.PlayerID, mid(2), ErrNoSuchMessage},
		{"an unknown message", gameA, ab.ConversationID, memberA.PlayerID, mid(99), ErrNoSuchMessage},
		{"not a member and no such message: not a member wins", gameA, ab.ConversationID, memberC.PlayerID, mid(99), ErrNotMember},
	}
	for _, tt := range tests {
		if err := r.MarkRead(ctx, tt.game, tt.player, tt.conv, tt.msg); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	// Nothing was changed by any of them.
	if got := r.lastRead(t, gameA, ab.ConversationID, memberA.PlayerID); got != "" {
		t.Errorf("position = %q after only refused calls, want none", got)
	}
	if got := r.lastRead(t, gameA, ac.ConversationID, memberC.PlayerID); got != "" {
		t.Errorf("Carol's position = %q, want none", got)
	}
}

// Two tabs (or two events) can mark different messages at the same moment. Whatever the order they
// are applied in, the position ends at the newest one and never goes back.
func TestMarkReadConcurrentCallsEndAtTheNewestMessage(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	const n = 30
	now := time.Now().UTC()
	for i := 1; i <= n; i++ {
		r.addMessageAt(t, game, dm.ConversationID, i, memberB.PlayerID, "Rashad", "hi", now.Add(time.Duration(i)*time.Second))
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- r.MarkRead(ctx, game, memberA.PlayerID, dm.ConversationID, mid(i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := r.lastRead(t, game, dm.ConversationID, memberA.PlayerID); got != mid(n) {
		t.Errorf("position = %s, want %s (the newest)", got, mid(n))
	}
}

func TestReadsWithoutDatabase(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	if _, err := r.ListConversations(ctx, "g", "p"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("ListConversations: %v", err)
	}
	if _, _, err := r.ListMessages(ctx, "g", "p", "c", "", 10); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("ListMessages: %v", err)
	}
	if err := r.MarkRead(ctx, "g", "p", "c", "m"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("MarkRead: %v", err)
	}
}

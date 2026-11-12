package repository

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

const (
	msgID1 = "00000000-0000-7000-8000-000000000001"
	msgID2 = "00000000-0000-7000-8000-000000000002"
)

func TestInsertMessageStoresAndListsAllMembers(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 11, 10, 8, 0, 0, 123_000_000, time.UTC)
	members, err := r.InsertMessage(ctx, game, dm.ConversationID, memberA.PlayerID, NewMessage{ID: msgID1, SenderName: "Ali", Body: "gg", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(members)
	if len(members) != 2 || members[0] != memberA.PlayerID || members[1] != memberB.PlayerID {
		t.Errorf("members = %v, want both players", members)
	}

	var m Message
	err = r.pool.QueryRow(ctx, "select id, sender_id, sender_name, body, created_at from messages where conversation_id = $1", dm.ConversationID).
		Scan(&m.ID, &m.SenderID, &m.SenderName, &m.Body, &m.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != msgID1 || m.SenderID != memberA.PlayerID || m.SenderName != "Ali" || m.Body != "gg" || !m.CreatedAt.Equal(at) {
		t.Errorf("stored = %+v", m)
	}

	// The new message is Rashad's unread and the last message.
	again, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if again.LastMessage == nil || again.LastMessage.ID != msgID1 || again.Unread[memberB.PlayerID] != 1 || again.Unread[memberA.PlayerID] != 0 {
		t.Errorf("after the message: last = %+v, unread = %v", again.LastMessage, again.Unread)
	}
}

func TestInsertMessageRefusesNonMembersAndStoresNothing(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newUUID(t), newUUID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	otherDM, err := r.UpsertDM(ctx, otherGame, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	stranger := newUUID(t)

	tests := []struct {
		name       string
		game, conv string
		sender     string
	}{
		{"a player who is not in it", game, dm.ConversationID, stranger},
		{"a conversation that does not exist", game, newUUID(t), memberA.PlayerID},
		// Alice IS a member of otherDM, but in the other game: the game of the token decides.
		{"a conversation of another game", game, otherDM.ConversationID, memberA.PlayerID},
		{"the right conversation in the wrong game", otherGame, dm.ConversationID, memberA.PlayerID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members, err := r.InsertMessage(ctx, tt.game, tt.conv, tt.sender, NewMessage{ID: newUUID(t), SenderName: "X", Body: "hi", CreatedAt: time.Now()})
			if !errors.Is(err, ErrNotMember) || members != nil {
				t.Errorf("= (%v, %v), want ErrNotMember and no members", members, err)
			}
		})
	}
	var n int
	if err := r.pool.QueryRow(ctx, "select count(*) from messages where game_id = any($1)", []string{game, otherGame}).Scan(&n); err != nil || n != 0 {
		t.Errorf("messages stored = %d, %v, want 0", n, err)
	}
}

func TestInsertMessageAfterLeavingIsRefused(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RemoveDMMembers(ctx, game, testKey); err != nil {
		t.Fatal(err)
	}
	_, err = r.InsertMessage(ctx, game, dm.ConversationID, memberA.PlayerID, NewMessage{ID: msgID1, SenderName: "Ali", Body: "hi", CreatedAt: time.Now()})
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("error = %v, want ErrNotMember after the friendship ended", err)
	}
}

func TestInsertMessageKeepsTextExactly(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, _ := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	body := `'; drop table messages; -- <script>alert(1)</script> 😀 ə "quoted" \ line1` + "\nline2"
	if _, err := r.InsertMessage(ctx, game, dm.ConversationID, memberA.PlayerID, NewMessage{ID: msgID1, SenderName: "O'Brien", Body: body, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var got, name string
	if err := r.pool.QueryRow(ctx, "select body, sender_name from messages where id = $1", msgID1).Scan(&got, &name); err != nil || got != body || name != "O'Brien" {
		t.Errorf("stored = %q, %q, %v, want the text unchanged", got, name, err)
	}
}

func TestInsertMessageConcurrentSendsAllLand(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	dm, _ := r.UpsertDM(ctx, game, testKey, memberA, memberB)

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.InsertMessage(ctx, game, dm.ConversationID, memberA.PlayerID, NewMessage{ID: newUUID(t), SenderName: "Ali", Body: "x", CreatedAt: time.Now()})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	var n int
	if err := r.pool.QueryRow(ctx, "select count(*) from messages where conversation_id = $1", dm.ConversationID).Scan(&n); err != nil || n != 20 {
		t.Errorf("stored = %d, %v, want 20", n, err)
	}
}

func TestInsertMessageWithoutDatabase(t *testing.T) {
	if _, err := New(nil).InsertMessage(context.Background(), "g", "c", "s", NewMessage{}); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("error = %v, want ErrNoDatabase", err)
	}
}

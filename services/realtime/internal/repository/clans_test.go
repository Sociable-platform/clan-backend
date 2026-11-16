package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var memberC = Member{PlayerID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Name: "Emma"}

func memberIDs(ms []Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.PlayerID
	}
	return out
}

func TestJoinClanRoomMakesOneRoomAndIsRepeatable(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	owner, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if owner.ConversationID == "" || owner.LastMessage != nil || !equal(memberIDs(owner.Members), []string{memberA.PlayerID}) || !owner.Joined {
		t.Fatalf("owner = %+v, want a new empty room with only the owner, who just joined", owner)
	}

	// The same event again, now with a renamed owner.
	again, err := r.JoinClanRoom(ctx, game, clan, Member{PlayerID: memberA.PlayerID, Name: "Ali B"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ConversationID != owner.ConversationID || len(again.Members) != 1 || again.Members[0].Name != "Ali B" || again.Joined {
		t.Errorf("again = %+v, want the same room, one member, the new name, and Joined false (a member already)", again)
	}

	joined, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if joined.ConversationID != owner.ConversationID || !joined.Joined {
		t.Errorf("the joiner got room %s (joined %v), the owner %s; want the same room, newly joined", joined.ConversationID, joined.Joined, owner.ConversationID)
	}
	// The order is the order of joining, not of the IDs.
	if got := memberIDs(joined.Members); !equal(got, []string{memberA.PlayerID, memberB.PlayerID}) {
		t.Errorf("members = %v, want the owner first, then the joiner", got)
	}
	if len(joined.Unread) != 2 || joined.Unread[memberB.PlayerID] != 0 {
		t.Errorf("unread = %v, want 0 for both", joined.Unread)
	}

	// The room is in the conversation list of both, as a clan room with the clan's ID.
	for _, p := range []string{memberA.PlayerID, memberB.PlayerID} {
		list, err := r.ListConversations(ctx, game, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Kind != "clan" || list[0].ClanID == nil || *list[0].ClanID != clan || len(list[0].Members) != 2 {
			t.Errorf("list of %s = %+v, want the one clan room with two members", p, list)
		}
	}
}

// A player who joins a clan whose room has messages gets them as history, not as unread: their read
// position starts at the newest message. Messages written after they joined count as usual.
func TestJoinClanRoomStartsANewMemberWithNothingUnread(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	room, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	conv := room.ConversationID
	r.addMessage(t, game, conv, mid(1), memberA.PlayerID, "Ali", "welcome")
	r.addMessage(t, game, conv, mid(2), memberA.PlayerID, "Ali", "anyone?")

	joined, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if n := joined.Unread[memberB.PlayerID]; n != 0 {
		t.Errorf("unread for the new member = %d, want 0", n)
	}
	list, err := r.ListConversations(ctx, game, memberB.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Unread != 0 || list[0].LastMessage == nil || list[0].LastMessage.ID != mid(2) {
		t.Fatalf("list = %+v, want the room with its last message and nothing unread", list)
	}

	// The same join event again does not move the read position.
	r.addMessage(t, game, conv, mid(3), memberA.PlayerID, "Ali", "hi Rashad")
	again, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if n := again.Unread[memberB.PlayerID]; n != 1 {
		t.Errorf("unread after a message and a repeated join = %d, want 1", n)
	}

	msgs, _, err := r.ListMessages(ctx, game, memberB.PlayerID, conv, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(msgs); !equal(got, []string{mid(3), mid(2), mid(1)}) {
		t.Errorf("history = %v, want all three messages, newest first", got)
	}
}

// A player who leaves and joins again gets the whole history: what was written before and while
// they were away. Like any new member, they start with nothing unread.
func TestJoinClanRoomGivesARejoinerTheWholeHistory(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	room, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClanRoom(ctx, game, clan, memberB); err != nil {
		t.Fatal(err)
	}
	conv := room.ConversationID
	r.addMessage(t, game, conv, mid(1), memberA.PlayerID, "Ali", "before")
	r.addMessage(t, game, conv, mid(2), memberB.PlayerID, "Rashad", "mine")
	if err := r.MarkRead(ctx, game, memberB.PlayerID, conv, mid(2)); err != nil {
		t.Fatal(err)
	}

	if _, found, err := r.LeaveClanRoom(ctx, game, clan, memberB.PlayerID); err != nil || !found {
		t.Fatalf("leave: found=%v err=%v", found, err)
	}
	r.addMessage(t, game, conv, mid(3), memberA.PlayerID, "Ali", "while away")
	r.addMessage(t, game, conv, mid(4), memberA.PlayerID, "Ali", "still away")

	back, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if back.ConversationID != conv {
		t.Fatalf("rejoined room %s, want the same room %s", back.ConversationID, conv)
	}
	if back.LastMessage == nil || back.LastMessage.ID != mid(4) {
		t.Errorf("last message = %+v, want the one written while away", back.LastMessage)
	}
	if n := back.Unread[memberB.PlayerID]; n != 0 {
		t.Errorf("unread for the rejoiner = %d, want 0", n)
	}
	// The joiner is the last one now.
	if got := memberIDs(back.Members); !equal(got, []string{memberA.PlayerID, memberB.PlayerID}) {
		t.Errorf("members = %v", got)
	}

	msgs, _, err := r.ListMessages(ctx, game, memberB.PlayerID, conv, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(msgs); !equal(got, []string{mid(4), mid(3), mid(2), mid(1)}) {
		t.Errorf("history = %v, want all four messages, newest first", got)
	}
}

func TestLeaveClanRoomKeepsTheRoomForTheOthers(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	room, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinClanRoom(ctx, game, clan, memberB); err != nil {
		t.Fatal(err)
	}
	r.addMessage(t, game, room.ConversationID, mid(1), memberB.PlayerID, "Rashad", "bye")

	for i := range 2 { // the second run is a redelivered event
		leave, found, err := r.LeaveClanRoom(ctx, game, clan, memberB.PlayerID)
		if err != nil || !found || leave.ConversationID != room.ConversationID {
			t.Fatalf("leave %d: %+v found=%v err=%v, want the room", i+1, leave, found, err)
		}
		// Only the first run removed a member, and it reports the name they had in the room.
		if want := (ClanLeave{ConversationID: room.ConversationID, Left: i == 0, Name: map[bool]string{true: "Rashad"}[i == 0]}); leave != want {
			t.Errorf("leave %d = %+v, want %+v", i+1, leave, want)
		}
	}
	if n := r.countMembers(t, game, room.ConversationID); n != 1 {
		t.Errorf("members = %d, want the owner only", n)
	}
	if _, err := r.ConversationOf(ctx, game, room.ConversationID, memberB.PlayerID); !errors.Is(err, ErrNotMember) {
		t.Errorf("ConversationOf for the one who left: %v, want ErrNotMember", err)
	}
	if _, err := r.InsertMessage(ctx, game, room.ConversationID, memberB.PlayerID, NewMessage{ID: mid(2), SenderName: "Rashad", Body: "x"}); !errors.Is(err, ErrNotMember) {
		t.Errorf("message from the one who left: %v, want ErrNotMember", err)
	}
	// The owner still sees the message of the one who left.
	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, room.ConversationID, "", 50)
	if err != nil || !equal(ids(msgs), []string{mid(1)}) {
		t.Errorf("history = %v (%v), want the message kept", ids(msgs), err)
	}

	if _, found, err := r.LeaveClanRoom(ctx, game, newUUID(t), memberA.PlayerID); err != nil || found {
		t.Errorf("leave a clan with no room: found=%v err=%v, want not found", found, err)
	}
}

func TestDeleteClanRoomRemovesEverythingAndTellsWhoWasIn(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan, other := newUUID(t), newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	room, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Member{memberC, memberA} {
		if _, err := r.JoinClanRoom(ctx, game, clan, m); err != nil {
			t.Fatal(err)
		}
	}
	r.addMessage(t, game, room.ConversationID, mid(1), memberB.PlayerID, "Rashad", "hi")
	kept, err := r.JoinClanRoom(ctx, game, other, memberA) // another clan of the game
	if err != nil {
		t.Fatal(err)
	}

	id, members, found, err := r.DeleteClanRoom(ctx, game, clan)
	if err != nil || !found || id != room.ConversationID {
		t.Fatalf("delete: id=%s found=%v err=%v, want the room", id, found, err)
	}
	if want := []string{memberB.PlayerID, memberC.PlayerID, memberA.PlayerID}; !equal(members, want) {
		t.Errorf("members = %v, want %v (in the order they joined)", members, want)
	}

	var rows int
	if err := r.pool.QueryRow(ctx, `select (select count(*) from conversations where id = $1)
		+ (select count(*) from conversation_members where conversation_id = $1)
		+ (select count(*) from messages where conversation_id = $1)`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d rows of the room are left, want none", rows)
	}

	if _, members, found, err := r.DeleteClanRoom(ctx, game, clan); err != nil || found || len(members) != 0 {
		t.Errorf("second delete: found=%v members=%v err=%v, want nothing", found, members, err)
	}
	if n := r.countMembers(t, game, kept.ConversationID); n != 1 {
		t.Errorf("the other clan's room has %d members, want it untouched", n)
	}
}

func TestClanRoomsAreSeparatePerGame(t *testing.T) {
	r, cleanup := testRepo(t)
	gameA, gameB, clan := newUUID(t), newUUID(t), newUUID(t)
	defer cleanup(gameA, gameB)
	ctx := context.Background()

	// The same clan ID in two games (it cannot happen with Social's IDs, but nothing may leak if it did).
	a, err := r.JoinClanRoom(ctx, gameA, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.JoinClanRoom(ctx, gameB, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if a.ConversationID == b.ConversationID {
		t.Fatal("two games share one clan room")
	}
	if _, _, err := r.LeaveClanRoom(ctx, gameA, clan, memberA.PlayerID); err != nil {
		t.Fatal(err)
	}
	if n := r.countMembers(t, gameB, b.ConversationID); n != 1 {
		t.Errorf("leaving in game A changed game B: %d members", n)
	}
	if _, members, _, err := r.DeleteClanRoom(ctx, gameA, clan); err != nil || len(members) != 0 {
		t.Errorf("delete in game A: members=%v err=%v, want an empty room", members, err)
	}
	if n := r.countMembers(t, gameB, b.ConversationID); n != 1 {
		t.Errorf("deleting in game A changed game B: %d members", n)
	}
}

// A DM is never taken for a clan room.
func TestClanRoomFunctionsLeaveDMsAlone(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newUUID(t)
	defer cleanup(game)
	ctx := context.Background()

	dm, err := r.UpsertDM(ctx, game, testKey, memberA, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LeaveClanRoom(ctx, game, dm.ConversationID, memberA.PlayerID); err != nil || found {
		t.Errorf("leave with a DM's ID as the clan: found=%v err=%v", found, err)
	}
	if _, _, found, err := r.DeleteClanRoom(ctx, game, dm.ConversationID); err != nil || found {
		t.Errorf("delete with a DM's ID as the clan: found=%v err=%v", found, err)
	}
	if n := r.countMembers(t, game, dm.ConversationID); n != 2 {
		t.Errorf("DM members = %d, want 2", n)
	}
}

func TestJoinClanRoomConcurrentJoinsMakeOneRoom(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)

	players := make([]Member, 8)
	for i := range players {
		players[i] = Member{PlayerID: newUUID(t), Name: "P"}
	}
	var wg sync.WaitGroup
	rooms := make([]string, len(players))
	errs := make([]error, len(players))
	for i, p := range players {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, err := r.JoinClanRoom(context.Background(), game, clan, p)
			rooms[i], errs[i] = room.ConversationID, err
		}()
	}
	wg.Wait()
	for i := range rooms {
		if errs[i] != nil {
			t.Fatalf("join %d: %v", i, errs[i])
		}
		if rooms[i] != rooms[0] {
			t.Fatalf("join %d got room %s, join 0 got %s", i, rooms[i], rooms[0])
		}
	}
	if n := r.countMembers(t, game, rooms[0]); n != len(players) {
		t.Errorf("members = %d, want %d", n, len(players))
	}
}

func TestClanRoomsWithoutDatabase(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	if _, err := r.JoinClanRoom(ctx, "g", "c", memberA); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("JoinClanRoom: %v", err)
	}
	if _, _, err := r.LeaveClanRoom(ctx, "g", "c", "p"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("LeaveClanRoom: %v", err)
	}
	if _, err := r.InsertMemberMessage(ctx, "g", "c", MemberMessage{}); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("InsertMemberMessage: %v", err)
	}
	if _, _, _, err := r.DeleteClanRoom(ctx, "g", "c"); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("DeleteClanRoom: %v", err)
	}
}

// Member lines (a join, a leave, a kick) are stored with their kind and an empty body, come back with
// it in the history and as the last message, and are never unread. A player who left can be the
// subject of one, and the members returned are the ones still there.
func TestMemberMessagesAreStoredButNeverUnread(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

	room, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	conv := room.ConversationID
	if _, err := r.JoinClanRoom(ctx, game, clan, memberB); err != nil {
		t.Fatal(err)
	}
	members, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(1), Kind: "member_joined", PlayerID: memberB.PlayerID, PlayerName: "Rashad", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(members, []string{memberA.PlayerID, memberB.PlayerID}) {
		t.Errorf("members after the join line = %v, want both, in the order they joined", members)
	}
	r.addMessage(t, game, conv, mid(2), memberA.PlayerID, "Ali", "welcome")

	if _, _, err := r.LeaveClanRoom(ctx, game, clan, memberB.PlayerID); err != nil {
		t.Fatal(err)
	}
	// Rashad is not a member any more and is still the subject of the line.
	members, err = r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(3), Kind: "member_kicked", PlayerID: memberB.PlayerID, PlayerName: "Rashad", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(members, []string{memberA.PlayerID}) {
		t.Errorf("members after the kick line = %v, want only the owner", members)
	}

	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, conv, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Kind+" "+m.SenderName+" "+m.Body)
	}
	if want := []string{"member_kicked Rashad ", "text Ali welcome", "member_joined Rashad "}; !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}

	// Ali has read nothing: the join and the kick lines are not unread, and Ali's own message never is.
	list, err := r.ListConversations(ctx, game, memberA.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Unread != 0 || list[0].LastMessage == nil || list[0].LastMessage.Kind != "member_kicked" {
		t.Errorf("list = %+v, want the room with the kick line last and nothing unread", list)
	}
	again, err := r.JoinClanRoom(ctx, game, clan, memberB)
	if err != nil {
		t.Fatal(err)
	}
	if again.Unread[memberA.PlayerID] != 0 || again.LastMessage == nil || again.LastMessage.Kind != "member_kicked" {
		t.Errorf("room = %+v, want no unread for Ali and the kick line last", again)
	}

	// Only the known kinds are stored.
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(4), Kind: "shout", PlayerID: memberA.PlayerID, PlayerName: "Ali", CreatedAt: at}); err == nil {
		t.Error("a message of an unknown kind was stored")
	}
	// A message a player sends is text.
	if _, err := r.InsertMessage(ctx, game, conv, memberA.PlayerID, NewMessage{ID: mid(5), SenderName: "Ali", Body: "hi", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	last, err := lastMessage(ctx, r.pool, game, conv)
	if err != nil || last == nil || last.ID != mid(5) || last.Kind != "text" {
		t.Errorf("last message = %+v (%v), want the sent one, as text", last, err)
	}
}

// A member_kicked line keeps who removed the player: in the history, as the last message of the
// conversation list and of a room, and nowhere else. The table refuses kicked_by on any other message.
func TestMemberKickedLineKeepsWhoRemovedThePlayer(t *testing.T) {
	r, cleanup := testRepo(t)
	game, clan := newUUID(t), newUUID(t)
	defer cleanup(game)
	ctx := context.Background()
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

	room, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	conv := room.ConversationID
	if _, err := r.JoinClanRoom(ctx, game, clan, memberB); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LeaveClanRoom(ctx, game, clan, memberB.PlayerID); err != nil {
		t.Fatal(err)
	}

	byAli := &Member{PlayerID: memberA.PlayerID, Name: "Ali"}
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(1), Kind: "member_kicked", PlayerID: memberB.PlayerID, PlayerName: "Rashad", CreatedAt: at, KickedBy: byAli}); err != nil {
		t.Fatal(err)
	}
	// An event that did not name the kicker: the line has none.
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(2), Kind: "member_kicked", PlayerID: memberC.PlayerID, PlayerName: "Emma", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(3), Kind: "member_left", PlayerID: memberC.PlayerID, PlayerName: "Emma", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}

	msgs, _, err := r.ListMessages(ctx, game, memberA.PlayerID, conv, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("history has %d messages, want 3", len(msgs))
	}
	// Newest first: member_left, kicked without a kicker, kicked by Ali.
	if msgs[0].KickedBy != nil || msgs[1].KickedBy != nil {
		t.Errorf("kickedBy = %+v, %+v; want none on a leave and on a kick the event did not explain", msgs[0].KickedBy, msgs[1].KickedBy)
	}
	if k := msgs[2].KickedBy; k == nil || *k != *byAli || msgs[2].Kind != "member_kicked" {
		t.Errorf("the kick = %+v kickedBy %+v, want member_kicked by Ali", msgs[2], k)
	}

	// The last message of the conversation list and of the room carries it too.
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(4), Kind: "member_kicked", PlayerID: memberB.PlayerID, PlayerName: "Rashad", CreatedAt: at, KickedBy: byAli}); err != nil {
		t.Fatal(err)
	}
	list, err := r.ListConversations(ctx, game, memberA.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].LastMessage == nil || list[0].LastMessage.KickedBy == nil || *list[0].LastMessage.KickedBy != *byAli {
		t.Errorf("list = %+v, want the kick as the last message, by Ali", list)
	}
	again, err := r.JoinClanRoom(ctx, game, clan, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if again.LastMessage == nil || again.LastMessage.KickedBy == nil || *again.LastMessage.KickedBy != *byAli {
		t.Errorf("room last message = %+v, want the kick, by Ali", again.LastMessage)
	}

	// The table keeps it honest: only a member_kicked has a kicker, and id and name come together.
	if _, err := r.InsertMemberMessage(ctx, game, conv, MemberMessage{ID: mid(5), Kind: "member_left", PlayerID: memberB.PlayerID, PlayerName: "Rashad", CreatedAt: at, KickedBy: byAli}); err == nil {
		t.Error("a member_left with a kicker was stored")
	}
	if _, err := r.pool.Exec(ctx, `insert into messages (id, conversation_id, game_id, kind, sender_id, sender_name, body, kicked_by_id)
		values ($1, $2, $3, 'member_kicked', $4, 'Rashad', '', $5)`, mid(6), conv, game, memberB.PlayerID, memberA.PlayerID); err == nil {
		t.Error("a kicker id without a name was stored")
	}
	if _, err := r.pool.Exec(ctx, `insert into messages (id, conversation_id, game_id, kind, sender_id, sender_name, body, kicked_by_id, kicked_by_name)
		values ($1, $2, $3, 'text', $4, 'Ali', 'hi', $4, 'Ali')`, mid(7), conv, game, memberA.PlayerID); err == nil {
		t.Error("a text message with a kicker was stored")
	}
}

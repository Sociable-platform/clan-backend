package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

const (
	wolves = "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091" // a clan
	emma   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func (m *memStore) JoinClanRoom(_ context.Context, gameID, clanID string, mem repository.Member) (repository.ClanRoom, error) {
	m.clanCalls = append(m.clanCalls, "join "+gameID+"|"+clanID+"|"+mem.PlayerID+"|"+mem.Name)
	return m.room, m.clanErr
}

func (m *memStore) LeaveClanRoom(_ context.Context, gameID, clanID, playerID string) (repository.ClanLeave, bool, error) {
	m.clanCalls = append(m.clanCalls, "leave "+gameID+"|"+clanID+"|"+playerID)
	return repository.ClanLeave{ConversationID: m.roomID, Left: m.roomLeft, Name: m.roomLeftName}, m.roomFound, m.clanErr
}

func (m *memStore) InsertMemberMessage(_ context.Context, gameID, conversationID string, msg repository.MemberMessage) ([]string, error) {
	if m.lineErr != nil {
		return nil, m.lineErr
	}
	m.lines = append(m.lines, msg)
	m.lineConvs = append(m.lineConvs, gameID+"|"+conversationID)
	return m.lineMembers, nil
}

func (m *memStore) DeleteClanRoom(_ context.Context, gameID, clanID string) (string, []string, bool, error) {
	m.clanCalls = append(m.clanCalls, "delete "+gameID+"|"+clanID)
	return m.roomID, m.roomMembers, m.roomFound, m.clanErr
}

// frameJSON is what the dispatcher would put inside a frame's data.
func frameJSON(t *testing.T, p Push) string {
	t.Helper()
	b, err := json.Marshal(p.Data)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClanCreatedGivesTheOwnerTheRoom(t *testing.T) {
	store := &memStore{room: repository.ClanRoom{
		ConversationID: "c-9",
		Members:        []repository.Member{{PlayerID: ali, Name: "Ali"}},
		Unread:         map[string]int{ali: 0},
	}}
	svc := New(store, WithPresence(onlineSet{ali: true}))

	// IDs in upper case are stored in lower case, like every other ID of an event.
	pushes, err := svc.ClanCreated(context.Background(), game, strings.ToUpper(wolves), Player{ID: ali, Name: " Ali "})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"join " + game + "|" + wolves + "|" + ali + "|Ali"}; !reflect.DeepEqual(store.clanCalls, want) {
		t.Errorf("store calls = %v, want %v (the name must be trimmed)", store.clanCalls, want)
	}
	if len(pushes) != 1 {
		t.Fatalf("pushes = %d, want 1 (conversation.added for the owner)", len(pushes))
	}
	p := pushes[0]
	if p.GameID != game || p.Type != "conversation.added" || !reflect.DeepEqual(p.PlayerIDs, []string{ali}) {
		t.Errorf("push = %s to %v in %s, want conversation.added to the owner", p.Type, p.PlayerIDs, p.GameID)
	}
	want := `{"conversation":{"id":"c-9","kind":"clan","clanId":"` + wolves + `","members":[{"id":"` + ali +
		`","name":"Ali","online":true}],"lastMessage":null,"unread":0}}`
	if got := frameJSON(t, p); got != want {
		t.Errorf("data = %s\nwant   %s", got, want)
	}
}

func TestClanMemberJoinedGivesTheRoomWithItsHistoryAndTellsTheOthers(t *testing.T) {
	last := time.Date(2026, 11, 10, 13, 30, 41, 207e6, time.FixedZone("x", 4*3600))
	store := &memStore{room: repository.ClanRoom{
		ConversationID: "c-9",
		Members: []repository.Member{
			{PlayerID: ali, Name: "Ali"}, {PlayerID: emma, Name: "Emma"}, {PlayerID: rashad, Name: "Rashad"},
		},
		LastMessage: &repository.Message{ID: "m-7", Kind: "text", SenderID: ali, SenderName: "Ali", Body: "practice at 8", CreatedAt: last},
		Unread:      map[string]int{ali: 0, emma: 3, rashad: 5},
	}}
	svc := New(store, WithPresence(onlineSet{ali: true, rashad: true}))

	pushes, err := svc.ClanMemberJoined(context.Background(), game, wolves, Player{ID: rashad, Name: "Rashad"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"join " + game + "|" + wolves + "|" + rashad + "|Rashad"}; !reflect.DeepEqual(store.clanCalls, want) {
		t.Errorf("store calls = %v, want %v", store.clanCalls, want)
	}
	// The player was a member already (Joined false, a repeated event): no member line.
	if len(store.lines) != 0 {
		t.Errorf("member lines = %+v, want none for a player who already was a member", store.lines)
	}
	if len(pushes) != 2 {
		t.Fatalf("pushes = %d, want conversation.added for the joiner and clan_joined for the others", len(pushes))
	}

	added := pushes[0]
	if added.Type != "conversation.added" || !reflect.DeepEqual(added.PlayerIDs, []string{rashad}) {
		t.Errorf("first push = %s to %v, want conversation.added to the joiner", added.Type, added.PlayerIDs)
	}
	// The joiner's own unread count (5), every member with a presence snapshot, the last message in UTC.
	want := `{"conversation":{"id":"c-9","kind":"clan","clanId":"` + wolves + `","members":[` +
		`{"id":"` + ali + `","name":"Ali","online":true},` +
		`{"id":"` + emma + `","name":"Emma","online":false},` +
		`{"id":"` + rashad + `","name":"Rashad","online":true}],` +
		`"lastMessage":{"id":"m-7","kind":"text","senderId":"` + ali + `","senderName":"Ali","body":"practice at 8","createdAt":"2026-11-10T09:30:41.207Z"},` +
		`"unread":5}}`
	if got := frameJSON(t, added); got != want {
		t.Errorf("conversation.added data = %s\nwant                      %s", got, want)
	}

	joined := pushes[1]
	if joined.Type != "notification" || !reflect.DeepEqual(joined.PlayerIDs, []string{ali, emma}) {
		t.Errorf("second push = %s to %v, want a notification to the other members only", joined.Type, joined.PlayerIDs)
	}
	want = `{"kind":"clan_joined","payload":{"clanId":"` + wolves + `","player":{"id":"` + rashad + `","name":"Rashad"}}}`
	if got := frameJSON(t, joined); got != want {
		t.Errorf("clan_joined data = %s\nwant              %s", got, want)
	}
}

func TestClanMemberJoinedAloneTellsNobodyElse(t *testing.T) {
	// The room was empty, for example because clan.created was never seen.
	store := &memStore{room: repository.ClanRoom{ConversationID: "c-9", Members: []repository.Member{{PlayerID: rashad, Name: "Rashad"}}}}
	pushes, err := New(store).ClanMemberJoined(context.Background(), game, wolves, Player{ID: rashad, Name: "Rashad"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 1 || pushes[0].Type != "conversation.added" {
		t.Errorf("pushes = %+v, want only conversation.added", pushes)
	}
}

const lineID = "0193a6c0-1111-7222-8333-944455556666" // the ID fixedIDs gives every new message

func TestClanMemberJoinedWritesOneJoinedLine(t *testing.T) {
	store := &memStore{
		room: repository.ClanRoom{
			ConversationID: "c-9",
			Members:        []repository.Member{{PlayerID: ali, Name: "Ali"}, {PlayerID: rashad, Name: "Rashad"}},
			LastMessage:    &repository.Message{ID: "m-7", Kind: "text", SenderID: ali, SenderName: "Ali", Body: "practice at 8", CreatedAt: testNow},
			Unread:         map[string]int{ali: 0, rashad: 0},
			Joined:         true,
		},
		lineMembers: []string{ali, rashad},
	}
	svc := New(store, WithIDs(fixedIDs{id: lineID, at: testNow}))

	pushes, err := svc.ClanMemberJoined(context.Background(), game, wolves, Player{ID: rashad, Name: "Rashad"})
	if err != nil {
		t.Fatal(err)
	}
	wantLine := repository.MemberMessage{ID: lineID, Kind: "member_joined", PlayerID: rashad, PlayerName: "Rashad", CreatedAt: testNow}
	if !reflect.DeepEqual(store.lines, []repository.MemberMessage{wantLine}) || !reflect.DeepEqual(store.lineConvs, []string{game + "|c-9"}) {
		t.Errorf("member lines = %+v in %v, want one member_joined line for Rashad in c-9", store.lines, store.lineConvs)
	}

	line := `{"id":"` + lineID + `","kind":"member_joined","senderId":"` + rashad + `","senderName":"Rashad","body":"","createdAt":"2026-11-10T08:00:00Z"}`
	var got []string
	for _, p := range pushes {
		got = append(got, p.Type+" "+strings.Join(p.PlayerIDs, ","))
	}
	want := []string{"conversation.added " + rashad, "notification " + ali, "message.new " + ali + "," + rashad}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pushes = %v\nwant     %v", got, want)
	}
	// The joiner's room already ends with the line, and it is not unread.
	if added := frameJSON(t, pushes[0]); !strings.Contains(added, `"lastMessage":`+line+`,"unread":0}`) {
		t.Errorf("conversation.added = %s\nwant the joined line as the last message and nothing unread", added)
	}
	if got, want := frameJSON(t, pushes[2]), `{"conversationId":"c-9","message":`+line+`}`; got != want {
		t.Errorf("message.new data = %s\nwant             %s", got, want)
	}
}

func TestClanMemberLeft(t *testing.T) {
	removed := `{"conversationId":"c-9"}`
	kickedNote := `{"kind":"clan_kicked","payload":{"clanId":"` + wolves + `"}}`
	line := func(kind string) string {
		return `{"conversationId":"c-9","message":{"id":"` + lineID + `","kind":"` + kind + `","senderId":"` + rashad +
			`","senderName":"Rashad","body":"","createdAt":"2026-11-10T08:00:00Z"}}`
	}
	tests := []struct {
		name      string
		found     bool
		left      bool     // the player was a member
		stay      []string // the members after the leave
		kicked    bool
		want      []string // "type to data"
		wantLines []string // kinds of the stored member lines
	}{
		{"left", true, true, []string{ali, emma}, false, []string{
			"conversation.removed " + rashad + " " + removed,
			"message.new " + ali + "," + emma + " " + line("member_left"),
		}, []string{"member_left"}},
		{"kicked", true, true, []string{ali}, true, []string{
			"conversation.removed " + rashad + " " + removed,
			"notification " + rashad + " " + kickedNote,
			"message.new " + ali + " " + line("member_kicked"),
		}, []string{"member_kicked"}},
		{"left again: not a member any more, no second line", true, false, []string{ali}, false, []string{
			"conversation.removed " + rashad + " " + removed,
		}, nil},
		{"left with nobody staying: the line is stored, nobody to tell", true, true, nil, false, []string{
			"conversation.removed " + rashad + " " + removed,
		}, []string{"member_left"}},
		{"left a clan realtime has no room for", false, false, nil, false, nil, nil},
		{"kicked from a clan realtime has no room for still hears it", false, false, nil, true, []string{
			"notification " + rashad + " " + kickedNote,
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{roomID: "c-9", roomFound: tt.found, roomLeft: tt.left, roomLeftName: "Rashad", lineMembers: tt.stay}
			pushes, err := New(store, WithIDs(fixedIDs{id: lineID, at: testNow})).ClanMemberLeft(context.Background(), game, wolves, rashad, tt.kicked)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"leave " + game + "|" + wolves + "|" + rashad}; !reflect.DeepEqual(store.clanCalls, want) {
				t.Errorf("store calls = %v, want %v", store.clanCalls, want)
			}
			var kinds []string
			for i, l := range store.lines {
				// The name is the one the player had in the room.
				if l.PlayerID != rashad || l.PlayerName != "Rashad" || store.lineConvs[i] != game+"|c-9" {
					t.Errorf("member line = %+v in %s, want Rashad's in c-9", l, store.lineConvs[i])
				}
				kinds = append(kinds, l.Kind)
			}
			if !reflect.DeepEqual(kinds, tt.wantLines) {
				t.Errorf("member lines = %v, want %v", kinds, tt.wantLines)
			}
			var got []string
			for _, p := range pushes {
				if p.GameID != game {
					t.Errorf("%s goes to game %s, want %s", p.Type, p.GameID, game)
				}
				got = append(got, p.Type+" "+strings.Join(p.PlayerIDs, ",")+" "+frameJSON(t, p))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("frames = %v\nwant     %v", got, tt.want)
			}
		})
	}
}

// A member line that cannot be stored fails the event: the consumer tries it again, and nothing is sent.
func TestClanMemberLinesPassStoreErrorsOn(t *testing.T) {
	errDB := errors.New("database down")
	calls := map[string]func(*Service) ([]Push, error){
		"joined": func(s *Service) ([]Push, error) {
			return s.ClanMemberJoined(context.Background(), game, wolves, Player{ID: rashad, Name: "Rashad"})
		},
		"left": func(s *Service) ([]Push, error) {
			return s.ClanMemberLeft(context.Background(), game, wolves, rashad, false)
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			store := &memStore{
				room:   repository.ClanRoom{ConversationID: "c-9", Joined: true},
				roomID: "c-9", roomFound: true, roomLeft: true, roomLeftName: "Rashad",
				lineErr: errDB,
			}
			pushes, err := call(New(store))
			if !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) {
				t.Errorf("error = %v, want the database error, not ErrInvalidEvent", err)
			}
			if len(pushes) != 0 {
				t.Errorf("pushes = %v, want none", pushes)
			}
		})
	}
}

func TestClanDisbandedTellsEveryMember(t *testing.T) {
	store := &memStore{roomID: "c-9", roomFound: true, roomMembers: []string{ali, emma}}
	pushes, err := New(store).ClanDisbanded(context.Background(), game, wolves)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"delete " + game + "|" + wolves}; !reflect.DeepEqual(store.clanCalls, want) {
		t.Errorf("store calls = %v, want %v", store.clanCalls, want)
	}
	var got []string
	for _, p := range pushes {
		if p.GameID != game || !reflect.DeepEqual(p.PlayerIDs, []string{ali, emma}) {
			t.Errorf("%s goes to %v in %s, want every member", p.Type, p.PlayerIDs, p.GameID)
		}
		got = append(got, p.Type+" "+frameJSON(t, p))
	}
	want := []string{
		`conversation.removed {"conversationId":"c-9"}`,
		`notification {"kind":"clan_disbanded","payload":{"clanId":"` + wolves + `"}}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %v\nwant     %v", got, want)
	}
}

func TestClanDisbandedWithoutARoomSendsNothing(t *testing.T) {
	for _, store := range []*memStore{
		{roomFound: false},               // never seen, or a second run
		{roomID: "c-9", roomFound: true}, // a room nobody was left in
	} {
		pushes, err := New(store).ClanDisbanded(context.Background(), game, wolves)
		if err != nil || len(pushes) != 0 {
			t.Errorf("found=%v: pushes = %v, err = %v; want nothing", store.roomFound, pushes, err)
		}
	}
}

func TestClanEventsRefuseBadData(t *testing.T) {
	ok := Player{ID: ali, Name: "Ali"}
	tests := []struct {
		name string
		call func(*Service) ([]Push, error)
	}{
		{"created: game is not a UUID", func(s *Service) ([]Push, error) { return s.ClanCreated(context.Background(), "g-1", wolves, ok) }},
		{"created: clan is not a UUID", func(s *Service) ([]Push, error) { return s.ClanCreated(context.Background(), game, "wolves", ok) }},
		{"created: owner has no name", func(s *Service) ([]Push, error) {
			return s.ClanCreated(context.Background(), game, wolves, Player{ID: ali, Name: "  "})
		}},
		{"joined: player is not a UUID", func(s *Service) ([]Push, error) {
			return s.ClanMemberJoined(context.Background(), game, wolves, Player{ID: "u-1", Name: "Ali"})
		}},
		{"joined: clan missing", func(s *Service) ([]Push, error) { return s.ClanMemberJoined(context.Background(), game, "", ok) }},
		{"left: player is not a UUID", func(s *Service) ([]Push, error) {
			return s.ClanMemberLeft(context.Background(), game, wolves, "", false)
		}},
		{"left: clan is not a UUID", func(s *Service) ([]Push, error) {
			return s.ClanMemberLeft(context.Background(), game, "x", ali, true)
		}},
		{"disbanded: clan is not a UUID", func(s *Service) ([]Push, error) { return s.ClanDisbanded(context.Background(), game, "x") }},
		{"disbanded: game is not a UUID", func(s *Service) ([]Push, error) { return s.ClanDisbanded(context.Background(), "", wolves) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			pushes, err := tt.call(New(store))
			if !errors.Is(err, ErrInvalidEvent) {
				t.Errorf("error = %v, want ErrInvalidEvent", err)
			}
			if len(pushes) != 0 || len(store.clanCalls) != 0 {
				t.Errorf("pushes = %v, store calls = %v; want nothing done", pushes, store.clanCalls)
			}
		})
	}
}

// A database failure is not an invalid event: the consumer must try the event again, and send nothing.
func TestClanEventsPassStoreErrorsOn(t *testing.T) {
	errDB := errors.New("database down")
	p := Player{ID: ali, Name: "Ali"}
	calls := map[string]func(*Service) ([]Push, error){
		"created": func(s *Service) ([]Push, error) { return s.ClanCreated(context.Background(), game, wolves, p) },
		"joined":  func(s *Service) ([]Push, error) { return s.ClanMemberJoined(context.Background(), game, wolves, p) },
		"left": func(s *Service) ([]Push, error) {
			return s.ClanMemberLeft(context.Background(), game, wolves, ali, true)
		},
		"disbanded": func(s *Service) ([]Push, error) { return s.ClanDisbanded(context.Background(), game, wolves) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			store := &memStore{clanErr: errDB, roomFound: true, roomID: "c-9", roomMembers: []string{ali}}
			pushes, err := call(New(store))
			if !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) {
				t.Errorf("error = %v, want the database error, not ErrInvalidEvent", err)
			}
			if len(pushes) != 0 {
				t.Errorf("pushes = %v, want none when nothing was stored", pushes)
			}
		})
	}
}

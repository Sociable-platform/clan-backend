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
	game   = "11111111-1111-4111-8111-111111111111"
	ali    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // sorts before rashad
	rashad = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// memStore records what the service asks of the repository.
type memStore struct {
	upserts []upsertCall
	removes []string // dm keys
	dm      repository.DM
	removed struct {
		id    string
		found bool
	}
	err error
}

type upsertCall struct {
	gameID, key string
	a, b        repository.Member
}

func (m *memStore) Ping(context.Context) error { return nil }

func (m *memStore) UpsertDM(_ context.Context, gameID, key string, a, b repository.Member) (repository.DM, error) {
	m.upserts = append(m.upserts, upsertCall{gameID, key, a, b})
	return m.dm, m.err
}

func (m *memStore) RemoveDMMembers(_ context.Context, gameID, key string) (string, bool, error) {
	m.removes = append(m.removes, gameID+"|"+key)
	return m.removed.id, m.removed.found, m.err
}

type onlineSet map[string]bool

func (o onlineSet) Online(gameID, playerID string) bool { return gameID == game && o[playerID] }

func TestFriendAcceptedBuildsConversationAndNotifications(t *testing.T) {
	last := time.Date(2026, 11, 9, 18, 20, 0, 0, time.FixedZone("x", 4*3600))
	store := &memStore{dm: repository.DM{
		ConversationID: "c-1",
		LastMessage:    &repository.Message{ID: "m-1", SenderID: rashad, SenderName: "Rashad", Body: "gg", CreatedAt: last},
		Unread:         map[string]int{ali: 1, rashad: 0},
	}}
	svc := New(store, WithPresence(onlineSet{ali: true}))

	pushes, err := svc.FriendAccepted(context.Background(), game,
		Player{ID: ali, Name: " Ali "}, Player{ID: rashad, Name: "Rashad"})
	if err != nil {
		t.Fatal(err)
	}

	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.gameID != game || got.key != ali+":"+rashad || got.a.Name != "Ali" || got.b.PlayerID != rashad {
		t.Errorf("upsert = %+v (the name must be trimmed, the key must be <smaller>:<larger>)", got)
	}

	// Four frames: conversation.added for each, then friend_accepted for each.
	var types, targets []string
	for _, p := range pushes {
		if p.GameID != game {
			t.Errorf("push for game %q, want %q", p.GameID, game)
		}
		types = append(types, p.Type)
		targets = append(targets, p.PlayerIDs[0])
	}
	if want := []string{"conversation.added", "conversation.added", "notification", "notification"}; !reflect.DeepEqual(types, want) {
		t.Fatalf("frame types = %v, want %v", types, want)
	}
	if want := []string{ali, rashad, ali, rashad}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("frame targets = %v, want %v", targets, want)
	}

	// The conversation object each player gets has their own unread count.
	wantAli := `{"conversation":{"id":"c-1","kind":"dm","clanId":null,"members":[` +
		`{"id":"` + ali + `","name":"Ali","online":true},{"id":"` + rashad + `","name":"Rashad","online":false}],` +
		`"lastMessage":{"id":"m-1","senderId":"` + rashad + `","senderName":"Rashad","body":"gg","createdAt":"2026-11-09T14:20:00Z"},"unread":1}}`
	if s := mustJSON(t, pushes[0].Data); s != wantAli {
		t.Errorf("conversation.added for Ali =\n%s\nwant\n%s", s, wantAli)
	}
	if s := mustJSON(t, pushes[1].Data); !strings.Contains(s, `"unread":0`) {
		t.Errorf("conversation.added for Rashad = %s, want unread 0", s)
	}

	// Each player is told about the OTHER one.
	if s, want := mustJSON(t, pushes[2].Data), `{"kind":"friend_accepted","payload":{"player":{"id":"`+rashad+`","name":"Rashad"}}}`; s != want {
		t.Errorf("notification for Ali = %s, want %s", s, want)
	}
	if s, want := mustJSON(t, pushes[3].Data), `{"kind":"friend_accepted","payload":{"player":{"id":"`+ali+`","name":"Ali"}}}`; s != want {
		t.Errorf("notification for Rashad = %s, want %s", s, want)
	}
}

func TestFriendAcceptedGivesTheSameKeyInEitherOrderAndAnyCase(t *testing.T) {
	store := &memStore{}
	svc := New(store)
	upper := "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	for _, order := range [][2]Player{
		{{ID: ali, Name: "Ali"}, {ID: rashad, Name: "Rashad"}},
		{{ID: rashad, Name: "Rashad"}, {ID: ali, Name: "Ali"}},
		{{ID: ali, Name: "Ali"}, {ID: upper, Name: "Rashad"}},
	} {
		if _, err := svc.FriendAccepted(context.Background(), game, order[0], order[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range store.upserts {
		if c.key != ali+":"+rashad {
			t.Errorf("call %d key = %q, want %q", i, c.key, ali+":"+rashad)
		}
	}
}

func TestFriendAcceptedWithoutPresenceEveryoneIsOffline(t *testing.T) {
	store := &memStore{dm: repository.DM{ConversationID: "c-1"}}
	pushes, err := New(store).FriendAccepted(context.Background(), game, Player{ali, "Ali"}, Player{rashad, "Rashad"})
	if err != nil {
		t.Fatal(err)
	}
	if s := mustJSON(t, pushes[0].Data); strings.Contains(s, `"online":true`) || !strings.Contains(s, `"lastMessage":null`) {
		t.Errorf("conversation = %s, want everyone offline and no last message", s)
	}
}

func TestEventsRejectBadInputWithoutTouchingTheDatabase(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Service) error
	}{
		{"accepted: game is not a uuid", func(s *Service) error {
			_, err := s.FriendAccepted(context.Background(), "game-1", Player{ali, "Ali"}, Player{rashad, "R"})
			return err
		}},
		{"accepted: player id is not a uuid", func(s *Service) error {
			_, err := s.FriendAccepted(context.Background(), game, Player{"u_42", "Ali"}, Player{rashad, "R"})
			return err
		}},
		{"accepted: uuid with a bad character", func(s *Service) error {
			_, err := s.FriendAccepted(context.Background(), game, Player{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaag", "Ali"}, Player{rashad, "R"})
			return err
		}},
		{"accepted: name is blank", func(s *Service) error {
			_, err := s.FriendAccepted(context.Background(), game, Player{ali, "  "}, Player{rashad, "R"})
			return err
		}},
		{"accepted: same player twice", func(s *Service) error {
			_, err := s.FriendAccepted(context.Background(), game, Player{ali, "Ali"}, Player{ali, "Ali"})
			return err
		}},
		{"removed: same player twice", func(s *Service) error {
			_, err := s.FriendRemoved(context.Background(), game, ali, ali)
			return err
		}},
		{"removed: id is empty", func(s *Service) error {
			_, err := s.FriendRemoved(context.Background(), game, ali, "")
			return err
		}},
		{"request: receiver is not a uuid", func(s *Service) error {
			_, err := s.FriendRequestSent(context.Background(), game, "r-1", Player{ali, "Ali"}, "rashad")
			return err
		}},
		{"request: to oneself", func(s *Service) error {
			_, err := s.FriendRequestSent(context.Background(), game, "r-1", Player{ali, "Ali"}, ali)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			err := tt.run(New(store))
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want ErrInvalidEvent", err)
			}
			if len(store.upserts)+len(store.removes) != 0 {
				t.Errorf("the database was called for an invalid event")
			}
		})
	}
}

func TestDatabaseFailureIsNotAnInvalidEvent(t *testing.T) {
	errDB := errors.New("connection refused")
	store := &memStore{err: errDB}
	svc := New(store)

	pushes, err := svc.FriendAccepted(context.Background(), game, Player{ali, "Ali"}, Player{rashad, "R"})
	if !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) || pushes != nil {
		t.Errorf("accepted = (%v, %v), want the database error and no frames", pushes, err)
	}
	store.removed.found = true
	pushes, err = svc.FriendRemoved(context.Background(), game, ali, rashad)
	if !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) || pushes != nil {
		t.Errorf("removed = (%v, %v), want the database error and no frames", pushes, err)
	}
}

func TestFriendRemoved(t *testing.T) {
	t.Run("both players hear about it", func(t *testing.T) {
		store := &memStore{}
		store.removed.id, store.removed.found = "c-1", true
		pushes, err := New(store).FriendRemoved(context.Background(), game, rashad, ali)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{game + "|" + ali + ":" + rashad}; !reflect.DeepEqual(store.removes, want) {
			t.Errorf("removes = %v, want %v", store.removes, want)
		}
		if len(pushes) != 1 || pushes[0].Type != "conversation.removed" || pushes[0].GameID != game {
			t.Fatalf("pushes = %+v", pushes)
		}
		if !reflect.DeepEqual(pushes[0].PlayerIDs, []string{rashad, ali}) {
			t.Errorf("sent to %v, want both players", pushes[0].PlayerIDs)
		}
		if s := mustJSON(t, pushes[0].Data); s != `{"conversationId":"c-1"}` {
			t.Errorf("data = %s", s)
		}
	})
	t.Run("no conversation, nothing to send", func(t *testing.T) {
		pushes, err := New(&memStore{}).FriendRemoved(context.Background(), game, ali, rashad)
		if err != nil || pushes != nil {
			t.Errorf("= (%v, %v), want nothing", pushes, err)
		}
	})
}

func TestFriendRequestSent(t *testing.T) {
	store := &memStore{}
	pushes, err := New(store).FriendRequestSent(context.Background(), game, "req-7", Player{ali, "Ali"}, rashad)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.upserts)+len(store.removes) != 0 {
		t.Error("a friend request is not stored")
	}
	if len(pushes) != 1 || pushes[0].Type != "notification" || !reflect.DeepEqual(pushes[0].PlayerIDs, []string{rashad}) {
		t.Fatalf("pushes = %+v, want one notification for the receiver only", pushes)
	}
	want := `{"kind":"friend_request","payload":{"from":{"id":"` + ali + `","name":"Ali"},"requestId":"req-7"}}`
	if s := mustJSON(t, pushes[0].Data); s != want {
		t.Errorf("data = %s, want %s", s, want)
	}
}

// Social does not send a request id yet: the notification is still sent, with requestId null.
func TestFriendRequestSentWithoutARequestID(t *testing.T) {
	for _, id := range []string{"", "  "} {
		pushes, err := New(&memStore{}).FriendRequestSent(context.Background(), game, id, Player{ali, "Ali"}, rashad)
		if err != nil || len(pushes) != 1 {
			t.Fatalf("id %q: = (%v, %v), want one notification", id, pushes, err)
		}
		want := `{"kind":"friend_request","payload":{"from":{"id":"` + ali + `","name":"Ali"},"requestId":null}}`
		if s := mustJSON(t, pushes[0].Data); s != want {
			t.Errorf("id %q: data = %s, want %s", id, s, want)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

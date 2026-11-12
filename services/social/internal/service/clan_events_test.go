package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// oneEvent fails unless rec holds exactly one event of type typ, and returns its data.
func oneEvent(t *testing.T, rec *events.Recorder, typ string) map[string]any {
	t.Helper()
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != typ || evs[0].GameID != gameA {
		t.Fatalf("events = %+v, want one %s for game %s", evs, typ, gameA)
	}
	return evs[0].Data
}

func wantData(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("data:\n got %#v\nwant %#v", got, want)
	}
}

func TestCreateClanPublishesClanCreated(t *testing.T) {
	store := newCreatingStore()
	svc, rec := friendService(store)
	if _, err := svc.CreateClan(context.Background(), gameA, "u_42", "Ali", "Wolves"); err != nil {
		t.Fatal(err)
	}
	owner := store.rows[gameA+"/u_42"]
	wantData(t, oneEvent(t, rec, EventClanCreated), map[string]any{
		"clan":   ref("clan-1", "Wolves"),
		"player": ref(owner.ID, "Ali"),
	})
}

func TestJoinClanPublishesMemberJoined(t *testing.T) {
	store := newJoiningStore()
	svc, rec := friendService(store)
	if _, err := svc.JoinClan(context.Background(), gameA, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	player := store.rows[gameA+"/u_42"]
	wantData(t, oneEvent(t, rec, EventClanMemberJoined), map[string]any{
		"clan":   ref(someClan, "Wolves"),
		"player": ref(player.ID, "Ali"),
	})
}

func TestLeaveClanPublishesWhatTheLeaveDid(t *testing.T) {
	ali := repository.ClanPlayer{ID: "p-ali", DisplayName: "Ali"}
	aysel := repository.ClanPlayer{ID: "p-aysel", DisplayName: "Aysel"}
	cases := []struct {
		name   string
		result repository.LeaveResult
		typ    string
		want   map[string]any
	}{
		{
			name:   "a member leaves",
			result: repository.LeaveResult{ClanID: someClan, ClanName: "Wolves", Player: ali},
			typ:    EventClanMemberLeft,
			want: map[string]any{"clan": ref(someClan, "Wolves"), "player": ref("p-ali", "Ali"),
				"reason": "left", "kicked_by": nil, "new_owner": nil},
		},
		{
			name:   "the owner leaves and someone takes over",
			result: repository.LeaveResult{ClanID: someClan, ClanName: "Wolves", Player: ali, NewOwner: &aysel},
			typ:    EventClanMemberLeft,
			want: map[string]any{"clan": ref(someClan, "Wolves"), "player": ref("p-ali", "Ali"),
				"reason": "left", "kicked_by": nil, "new_owner": ref("p-aysel", "Aysel")},
		},
		{
			name:   "the last member leaves: only clan.disbanded",
			result: repository.LeaveResult{ClanID: someClan, ClanName: "Wolves", Player: ali, Disbanded: true},
			typ:    EventClanDisbanded,
			want:   map[string]any{"clan": ref(someClan, "Wolves")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newLeavingStore()
			store.result = c.result
			svc, rec := friendService(store)
			if err := svc.LeaveClan(context.Background(), gameA, "u_42", someClan); err != nil {
				t.Fatal(err)
			}
			wantData(t, oneEvent(t, rec, c.typ), c.want)
		})
	}
}

func TestClanChangesThatFailPublishNothing(t *testing.T) {
	ctx := context.Background()

	creating := newCreatingStore()
	creating.createErr = repository.ErrClanNameTaken
	svc, rec := friendService(creating)
	if _, err := svc.CreateClan(ctx, gameA, "u_42", "Ali", "Wolves"); err == nil {
		t.Fatal("create should fail")
	}
	if _, err := svc.CreateClan(ctx, gameA, "u_42", "Ali", "x"); err == nil { // name too short
		t.Fatal("create with a bad name should fail")
	}

	joining := newJoiningStore()
	joining.joinErr = repository.ErrClanFull
	svc2, rec2 := friendService(joining)
	if _, err := svc2.JoinClan(ctx, gameA, "u_42", "Ali", someClan); err == nil {
		t.Fatal("join should fail")
	}

	leaving := newLeavingStore()
	leaving.leaveErr = repository.ErrNotInClan
	svc3, rec3 := friendService(leaving)
	if err := svc3.LeaveClan(ctx, gameA, "u_42", someClan); err == nil {
		t.Fatal("leave should fail")
	}
	leaving.leaveErr = errors.New("database is down")
	if err := svc3.LeaveClan(ctx, gameA, "u_42", someClan); err == nil {
		t.Fatal("leave should fail")
	}

	for name, r := range map[string]*events.Recorder{"create": rec, "join": rec2, "leave": rec3} {
		if evs := r.Events(); len(evs) != 0 {
			t.Errorf("%s published %+v after failing", name, evs)
		}
	}
}

// Every clan event Social publishes has exactly the fields of the same event in
// docs/event-messages.md (see TestFriendEventsHaveTheFieldsOfTheEventMessagesFile).
func TestClanEventsHaveTheFieldsOfTheEventMessagesFile(t *testing.T) {
	ctx := context.Background()
	ali := repository.ClanPlayer{ID: "p-ali", DisplayName: "Ali"}
	actions := map[string]func() (Store, func(*Service) error){
		EventClanCreated: func() (Store, func(*Service) error) {
			return newCreatingStore(), func(s *Service) error {
				_, err := s.CreateClan(ctx, gameA, "u_42", "Ali", "Wolves")
				return err
			}
		},
		EventClanMemberJoined: func() (Store, func(*Service) error) {
			return newJoiningStore(), func(s *Service) error {
				_, err := s.JoinClan(ctx, gameA, "u_42", "Ali", someClan)
				return err
			}
		},
		EventClanMemberLeft: func() (Store, func(*Service) error) {
			st := newLeavingStore()
			st.result = repository.LeaveResult{ClanID: someClan, ClanName: "Wolves", Player: ali}
			return st, func(s *Service) error { return s.LeaveClan(ctx, gameA, "u_42", someClan) }
		},
		EventClanRenamed: func() (Store, func(*Service) error) {
			return newRenamingStore(true), func(s *Service) error {
				_, err := s.RenameClan(ctx, gameA, "u_42", someClan, "Grey Wolves")
				return err
			}
		},
		EventClanDisbanded: func() (Store, func(*Service) error) {
			st := newLeavingStore()
			st.result = repository.LeaveResult{ClanID: someClan, ClanName: "Wolves", Player: ali, Disbanded: true}
			return st, func(s *Service) error { return s.LeaveClan(ctx, gameA, "u_42", someClan) }
		},
	}

	want := docShapes(t)
	for typ, setup := range actions {
		t.Run(typ, func(t *testing.T) {
			store, act := setup()
			svc, rec := friendService(store)
			if err := act(svc); err != nil {
				t.Fatal(err)
			}
			evs := rec.Events()
			if len(evs) != 1 || evs[0].Type != typ {
				t.Fatalf("events = %+v, want one %s", evs, typ)
			}
			wantShape, ok := want[typ]
			if !ok {
				t.Fatalf("%s is not in docs/event-messages.md", typ)
			}
			if got := publishedShape(t, evs[0]); got != wantShape {
				t.Errorf("fields of %s:\n  published %s\n  file      %s", typ, got, wantShape)
			}
		})
	}
}

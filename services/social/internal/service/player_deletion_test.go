package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

func (m *memStore) DeletePlayer(context.Context, string, string, string) (repository.PlayerDeletion, error) {
	return repository.PlayerDeletion{}, nil
}

func (m *memStore) DeletedAfter(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

// deletingStore answers DeletePlayer and DeletedAfter with what the test sets and remembers
// the arguments.
type deletingStore struct {
	*memStore
	result     repository.PlayerDeletion
	err        error
	deleted    bool
	game, ext  string // arguments of the last call
	deleteName string
	since      time.Time
}

func (s *deletingStore) DeletePlayer(_ context.Context, gameID, externalID, deletedName string) (repository.PlayerDeletion, error) {
	s.game, s.ext, s.deleteName = gameID, externalID, deletedName
	return s.result, s.err
}

func (s *deletingStore) DeletedAfter(_ context.Context, gameID, externalID string, t time.Time) (bool, error) {
	s.game, s.ext, s.since = gameID, externalID, t
	return s.deleted, s.err
}

func TestDeletePlayerPublishesAnEventForEverythingThatEnded(t *testing.T) {
	ali := repository.FriendPlayer{ID: "p-ali", DisplayName: "Ali"}
	vera := repository.FriendPlayer{ID: "p-vera", DisplayName: "Vera"}
	omar := repository.FriendPlayer{ID: "p-omar", DisplayName: "Omar"}
	store := &deletingStore{memStore: newMemStore(), result: repository.PlayerDeletion{
		Player: ali,
		Clan: &repository.LeaveResult{
			ClanID: "c-1", ClanName: "Wolves",
			Player:   repository.ClanPlayer{ID: ali.ID, DisplayName: ali.DisplayName},
			NewOwner: &repository.ClanPlayer{ID: vera.ID, DisplayName: vera.DisplayName},
		},
		Friendships: []repository.Friendship{{PlayerA: ali, PlayerB: vera}},
		Requests: []repository.FriendRequest{
			{ID: "r-out", Sender: ali, Receiver: omar},
			{ID: "r-in", Sender: vera, Receiver: ali},
		},
		Blocks: []repository.BlockPair{{Blocker: omar, Blocked: ali}},
	}}
	svc, rec := friendService(store)

	if err := svc.DeletePlayer(context.Background(), gameA, "u_42"); err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.ext != "u_42" || store.deleteName != DeletedPlayerName {
		t.Errorf("store got (%q, %q, %q)", store.game, store.ext, store.deleteName)
	}

	var types []string
	for _, e := range rec.Events() {
		if e.GameID != gameA {
			t.Errorf("%s for game %q, want %q", e.Type, e.GameID, gameA)
		}
		types = append(types, e.Type)
	}
	want := []string{
		EventClanMemberLeft, EventFriendRemoved,
		EventFriendRequestCancelled, EventFriendRequestDeclined, // ali sent r-out, received r-in
		EventPlayerUnblocked, EventPlayerRenamed,
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	evs := rec.Events()
	if got := evs[0].Data["reason"]; got != "left" {
		t.Errorf("clan.member_left reason = %v, want left", got)
	}
	if got := evs[1].Data["removed_by"]; got != ali.ID {
		t.Errorf("friend.removed removed_by = %v, want the deleted player", got)
	}
	wantData(t, evs[5].Data, map[string]any{"player": ref(ali.ID, DeletedPlayerName)})
}

func TestDeletePlayerEventsHaveTheFieldsOfTheEventMessagesFile(t *testing.T) {
	ali := repository.FriendPlayer{ID: "p-ali", DisplayName: "Ali"}
	vera := repository.FriendPlayer{ID: "p-vera", DisplayName: "Vera"}
	store := &deletingStore{memStore: newMemStore(), result: repository.PlayerDeletion{
		Player:      ali,
		Clan:        &repository.LeaveResult{ClanID: "c-1", ClanName: "Wolves", Disbanded: true},
		Friendships: []repository.Friendship{{PlayerA: ali, PlayerB: vera}},
		Requests:    []repository.FriendRequest{{ID: "r-1", Sender: ali, Receiver: vera}},
		Blocks:      []repository.BlockPair{{Blocker: ali, Blocked: vera}},
	}}
	svc, rec := friendService(store)
	if err := svc.DeletePlayer(context.Background(), gameA, "u_42"); err != nil {
		t.Fatal(err)
	}
	shapes := docShapes(t)
	for _, e := range rec.Events() {
		want, ok := shapes[e.Type]
		if !ok {
			t.Errorf("%s is not in docs/event-messages.md", e.Type)
			continue
		}
		if got := publishedShape(t, e); got != want {
			t.Errorf("fields of %s:\n  published %s\n  file      %s", e.Type, got, want)
		}
	}
}

func TestDeletePlayerWithoutALivePlayerPublishesNothing(t *testing.T) {
	store := &deletingStore{memStore: newMemStore()} // empty Player.ID: only a deleted record was written
	svc, rec := friendService(store)
	if err := svc.DeletePlayer(context.Background(), gameA, "u_42"); err != nil {
		t.Fatal(err)
	}
	if evs := rec.Events(); len(evs) != 0 {
		t.Fatalf("published %+v, want nothing", evs)
	}
}

func TestDeletePlayerStoreErrorKeepsNoEvent(t *testing.T) {
	boom := errors.New("db down")
	store := &deletingStore{memStore: newMemStore(), err: boom}
	svc, rec := friendService(store)
	if err := svc.DeletePlayer(context.Background(), gameA, "u_42"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if evs := rec.Events(); len(evs) != 0 {
		t.Fatalf("published %+v after a failure", evs)
	}
}

func TestPlayerDeletedSince(t *testing.T) {
	issued := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		deleted bool
		err     error
	}{
		{"deleted after the token", true, nil},
		{"not deleted", false, nil},
		{"store error", false, errors.New("db down")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &deletingStore{memStore: newMemStore(), deleted: tc.deleted, err: tc.err}
			got, err := New(store).PlayerDeletedSince(context.Background(), gameA, "u_42", issued)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if got != tc.deleted {
				t.Errorf("deleted = %v, want %v", got, tc.deleted)
			}
			if store.game != gameA || store.ext != "u_42" || !store.since.Equal(issued) {
				t.Errorf("store got (%q, %q, %v)", store.game, store.ext, store.since)
			}
		})
	}
}

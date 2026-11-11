package service

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

func TestSyncFriendshipStoresTheDMWithoutFrames(t *testing.T) {
	store := &memStore{}
	// the pair comes in "wrong" order and with upper case, as social may list it
	err := New(store).SyncFriendship(context.Background(), game,
		Player{ID: "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB", Name: " Rashad "}, Player{ID: ali, Name: "Ali"})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.gameID != game || got.key != ali+":"+rashad {
		t.Errorf("stored game %q key %q, want %q and %q", got.gameID, got.key, game, ali+":"+rashad)
	}
	if got.a != (repository.Member{PlayerID: rashad, Name: "Rashad"}) || got.b != (repository.Member{PlayerID: ali, Name: "Ali"}) {
		t.Errorf("members = %+v and %+v", got.a, got.b)
	}
}

func TestSyncFriendshipCanRunAgain(t *testing.T) {
	store := &memStore{}
	svc := New(store)
	for i := 0; i < 2; i++ {
		if err := svc.SyncFriendship(context.Background(), game, Player{ID: ali, Name: "Ali"}, Player{ID: rashad, Name: "Rashad"}); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if len(store.upserts) != 2 || store.upserts[0] != store.upserts[1] {
		t.Errorf("upserts = %+v, want the same call twice (the database makes it a no-op)", store.upserts)
	}
}

func TestSyncFriendshipRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		game string
		a, b Player
	}{
		{"game is not a UUID", "g1", Player{ID: ali, Name: "Ali"}, Player{ID: rashad, Name: "Rashad"}},
		{"player a is not a UUID", game, Player{ID: "u_42", Name: "Ali"}, Player{ID: rashad, Name: "Rashad"}},
		{"player b is not a UUID", game, Player{ID: ali, Name: "Ali"}, Player{ID: "", Name: "Rashad"}},
		{"player a has no name", game, Player{ID: ali, Name: "  "}, Player{ID: rashad, Name: "Rashad"}},
		{"a player is their own friend", game, Player{ID: ali, Name: "Ali"}, Player{ID: ali, Name: "Ali"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			err := New(store).SyncFriendship(context.Background(), tt.game, tt.a, tt.b)
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
			if len(store.upserts) != 0 {
				t.Error("something was stored for invalid input")
			}
		})
	}
}

func TestSyncFriendshipDatabaseError(t *testing.T) {
	errDB := errors.New("db down")
	err := New(&memStore{err: errDB}).SyncFriendship(context.Background(), game, Player{ID: ali, Name: "Ali"}, Player{ID: rashad, Name: "Rashad"})
	if !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) {
		t.Errorf("err = %v, want the database error and not ErrInvalidEvent", err)
	}
}

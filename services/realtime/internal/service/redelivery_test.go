package service

import (
	"context"
	"reflect"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// uniqueStore behaves like the database: a conversation is unique per (game, dm key), so storing
// the same pair twice leaves one row. The service decides the key, so if it ever made a different
// key for the same pair on a second delivery, rows would be a different count here.
type uniqueStore struct {
	memStore
	rows    map[string]bool // game|dm key
	renamed map[string]string
}

func newUniqueStore() *uniqueStore {
	return &uniqueStore{
		memStore: memStore{dm: repository.DM{ConversationID: "c-1"}},
		rows:     map[string]bool{},
		renamed:  map[string]string{},
	}
}

func (u *uniqueStore) UpsertDM(ctx context.Context, gameID, key string, a, b repository.Member) (repository.DM, error) {
	u.rows[gameID+"|"+key] = true
	return u.memStore.UpsertDM(ctx, gameID, key, a, b)
}

func (u *uniqueStore) RemoveDMMembers(ctx context.Context, gameID, key string) (string, bool, error) {
	_, existed := u.rows[gameID+"|"+key]
	delete(u.rows, gameID+"|"+key)
	// The first removal finds the conversation; a repeat finds nothing and has nothing to tell.
	return "c-1", existed, nil
}

func (u *uniqueStore) RenamePlayer(_ context.Context, gameID, playerID, name string) error {
	u.renamed[gameID+"|"+playerID] = name
	return nil
}

// RabbitMQ delivers at least once, so every event handler must be safe to run twice.
func TestEventsDeliveredTwiceLeaveOneRow(t *testing.T) {
	ctx := context.Background()
	a, b := Player{ID: ali, Name: "Ali"}, Player{ID: rashad, Name: "Rashad"}

	t.Run("friend.accepted makes one conversation and the same frames both times", func(t *testing.T) {
		store := newUniqueStore()
		svc := New(store)

		first, err := svc.FriendAccepted(ctx, game, req1, a, b)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.FriendAccepted(ctx, game, req1, a, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(store.rows) != 1 {
			t.Errorf("conversations = %d, want 1", len(store.rows))
		}
		if len(store.upserts) != 2 {
			t.Errorf("upserts = %d, want 2 (the service does not guess; the unique key makes it one row)", len(store.upserts))
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("second delivery sent %+v, want the same as the first %+v", second, first)
		}
	})

	t.Run("the second delivery with the players swapped is still one row", func(t *testing.T) {
		store := newUniqueStore()
		svc := New(store)
		if _, err := svc.FriendAccepted(ctx, game, req1, a, b); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.FriendAccepted(ctx, game, req1, b, a); err != nil {
			t.Fatal(err)
		}
		if len(store.rows) != 1 {
			t.Errorf("conversations = %d, want 1", len(store.rows))
		}
	})

	t.Run("friend.removed tells once, then nothing", func(t *testing.T) {
		store := newUniqueStore()
		svc := New(store)
		if _, err := svc.FriendAccepted(ctx, game, req1, a, b); err != nil {
			t.Fatal(err)
		}

		first, err := svc.FriendRemoved(ctx, game, ali, rashad, ali)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.FriendRemoved(ctx, game, ali, rashad, ali)
		if err != nil {
			t.Fatal(err)
		}
		if len(first) != 1 || first[0].Type != "conversation.removed" {
			t.Errorf("first delivery = %+v, want one conversation.removed", first)
		}
		if second != nil {
			t.Errorf("second delivery = %+v, want nothing", second)
		}
		if len(store.rows) != 0 {
			t.Errorf("conversations = %d, want 0", len(store.rows))
		}
	})

	t.Run("player.renamed twice stores the name once", func(t *testing.T) {
		store := newUniqueStore()
		svc := New(store)
		for range 2 {
			if _, err := svc.PlayerRenamed(ctx, game, Player{ID: ali, Name: "Alice"}); err != nil {
				t.Fatal(err)
			}
		}
		if want := map[string]string{game + "|" + ali: "Alice"}; !reflect.DeepEqual(store.renamed, want) {
			t.Errorf("names = %v, want %v", store.renamed, want)
		}
	})
}

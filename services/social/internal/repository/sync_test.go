package repository

import (
	"context"
	"testing"
)

func TestListFriendshipsAfterPagesInKeyOrder(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel, vusal := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel"), newPlayer(t, r, game, "vusal")
	makeFriends(t, r, game, ali, aysel)
	makeFriends(t, r, game, ali, vusal)
	makeFriends(t, r, game, aysel, vusal)

	// The database may hold other games too, so start at this game: its friendships come first.
	start := FriendshipKey{GameID: game, PlayerA: zeroUUID, PlayerB: zeroUUID}
	all, err := r.ListFriendshipsAfter(ctx, start, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d friendships, want 3: %+v", len(all), all)
	}
	for i, f := range all {
		if f.GameID != game {
			t.Errorf("friendship %d is of game %s, want %s", i, f.GameID, game)
		}
		if f.PlayerA.ID >= f.PlayerB.ID {
			t.Errorf("friendship %d: PlayerA %s is not smaller than PlayerB %s", i, f.PlayerA.ID, f.PlayerB.ID)
		}
		if f.PlayerA.DisplayName == "" || f.PlayerB.DisplayName == "" || f.CreatedAt.IsZero() {
			t.Errorf("friendship %d is missing names or time: %+v", i, f)
		}
		if i > 0 && !less(all[i-1].Key(), f.Key()) {
			t.Errorf("friendship %d does not come after %d", i, i-1)
		}
	}

	// Continuing from the first friendship gives exactly the other two; no repeat, no gap.
	rest, err := r.ListFriendshipsAfter(ctx, all[0].Key(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].Key() != all[1].Key() || rest[1].Key() != all[2].Key() {
		t.Errorf("rest = %+v, want the second and third friendship", rest)
	}

	// The limit is honoured.
	one, err := r.ListFriendshipsAfter(ctx, start, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Key() != all[0].Key() {
		t.Errorf("limit 1 = %+v, want the first friendship", one)
	}
}

func TestListFriendshipsAfterZeroKeyStartsAtTheBeginning(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	if _, err := r.ListFriendshipsAfter(context.Background(), FriendshipKey{}, 1); err != nil {
		t.Fatalf("zero key: %v", err)
	}
}

func TestListFriendshipsAfterWithoutDatabase(t *testing.T) {
	_, err := New(nil).ListFriendshipsAfter(context.Background(), FriendshipKey{}, 1)
	if err != ErrNoDatabase {
		t.Errorf("err = %v, want ErrNoDatabase", err)
	}
}

// less reports whether a sorts before b, as the database sorts: game, then player A, then player B.
// All IDs here are lowercase UUID text, which sorts the same as the uuid type.
func less(a, b FriendshipKey) bool {
	if a.GameID != b.GameID {
		return a.GameID < b.GameID
	}
	if a.PlayerA != b.PlayerA {
		return a.PlayerA < b.PlayerA
	}
	return a.PlayerB < b.PlayerB
}

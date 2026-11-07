package repository

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
)

// These tests talk to a real Postgres with the social migrations applied. They run only
// when SOCIAL_TEST_DATABASE_URL is set (see README "Integration tests"), otherwise they are skipped.
func testRepo(t *testing.T) (*Repository, func(gameIDs ...string)) {
	t.Helper()
	url := os.Getenv("SOCIAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SOCIAL_TEST_DATABASE_URL is not set; skipping database test")
	}
	pool, err := pg.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(gameIDs ...string) {
		for _, g := range gameIDs {
			_, _ = pool.Exec(context.Background(), "delete from players where game_id = $1", g)
		}
		pool.Close()
	}
	return New(pool), cleanup
}

// newGameID makes a random game ID so test runs never collide with each other or real data.
func newGameID(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestUpsertPlayerFirstAndRepeatedCalls(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()

	first, err := r.UpsertPlayer(ctx, game, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.UpsertPlayer(ctx, game, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("second call created a new player: %+v vs %+v", again, first)
	}

	renamed, err := r.UpsertPlayer(ctx, game, "u_42", "Ali the Great")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != first.ID || renamed.DisplayName != "Ali the Great" {
		t.Fatalf("rename: %+v", renamed)
	}

	other, err := r.UpsertPlayer(ctx, otherGame, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("the same player ID in another game must be a different player")
	}
}

// Two requests for a brand-new player at the same moment must still give one player.
func TestUpsertPlayerConcurrentFirstCalls(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)

	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := r.UpsertPlayer(context.Background(), game, "u_new", "Newbie")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.ID
		}()
	}
	wg.Wait()
	close(ids)
	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	if len(distinct) != 1 {
		t.Fatalf("20 parallel first calls produced %d different players, want 1", len(distinct))
	}
}

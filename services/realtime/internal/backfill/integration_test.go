package backfill

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// TestBackfillFillsAnEmptyDatabase is the "done when" of the task: a Realtime database that knows no
// friendship gets one from Social's list alone, with no event, and a second run changes nothing.
// It runs only when REALTIME_TEST_DATABASE_URL is set (see README "Integration tests").
func TestBackfillFillsAnEmptyDatabase(t *testing.T) {
	url := os.Getenv("REALTIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("REALTIME_TEST_DATABASE_URL is not set; skipping database test")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	game := newUUID(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "delete from conversations where game_id = $1", game) // members and messages go with it
		pool.Close()
	})
	repo := repository.New(pool)
	svc := service.New(repo)

	ali, aysel, vusal := newUUID(t), newUUID(t), newUUID(t)
	src := &pager{all: []social.Friendship{
		{GameID: game, A: social.Player{ID: ali, DisplayName: "Ali"}, B: social.Player{ID: aysel, DisplayName: "Aysel"}},
		{GameID: game, A: social.Player{ID: ali, DisplayName: "Ali"}, B: social.Player{ID: vusal, DisplayName: "Vusal"}},
	}}

	for run := 1; run <= 2; run++ {
		res, err := Run(ctx, src, svc, Options{PageSize: 1})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if res.Synced != 2 || res.Skipped != 0 {
			t.Fatalf("run %d: result %+v, want 2 synced", run, res)
		}

		friends, err := repo.FriendsOf(ctx, game, ali)
		if err != nil {
			t.Fatal(err)
		}
		if len(friends) != 2 {
			t.Errorf("run %d: Ali has %d friends, want 2: %v", run, len(friends), friends)
		}
		for _, p := range []string{aysel, vusal} {
			convs, err := repo.ListConversations(ctx, game, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(convs) != 1 {
				t.Errorf("run %d: player %s has %d conversations, want 1", run, p, len(convs))
			}
		}
	}
}

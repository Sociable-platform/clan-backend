package consumer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// TestEventsDeliveredTwiceStoreAndSendOnce is the "done when" of the task: the same friend.accepted,
// clan.member_joined and clan.member_left delivered twice give one set of rows and frames, with the
// real service and database. It runs only when REALTIME_TEST_DATABASE_URL is set (README "Integration tests").
func TestEventsDeliveredTwiceStoreAndSendOnce(t *testing.T) {
	url := os.Getenv("REALTIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("REALTIME_TEST_DATABASE_URL is not set; skipping database test")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	game := randomUUID(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "delete from conversations where game_id = $1", game) // members and messages go with it
		_, _ = pool.Exec(ctx, "delete from processed_events where game_id = $1", game)
		pool.Close()
	})

	send := &fakeSender{}
	d := NewDispatcher(service.New(repository.New(pool)), send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ali, aysel, clan := randomUUID(t), randomUUID(t), randomUUID(t)
	aliPlayer := map[string]any{"id": ali, "name": "Ali"}

	steps := []struct {
		name    string
		event   events.Event
		members int // conversation_members rows of the game afterwards
	}{
		{"friend.accepted", events.Event{ID: randomUUID(t), Type: typeFriendAccepted, GameID: game, Data: map[string]any{
			"players": []any{aliPlayer, map[string]any{"id": aysel, "name": "Aysel"}},
		}}, 2},
		{"clan.member_joined", events.Event{ID: randomUUID(t), Type: typeClanMemberJoined, GameID: game, Data: map[string]any{
			"clan": map[string]any{"id": clan, "name": "Wolves"}, "player": aliPlayer,
		}}, 3},
		{"clan.member_left", events.Event{ID: randomUUID(t), Type: typeClanMemberLeft, GameID: game, Data: map[string]any{
			"clan": map[string]any{"id": clan, "name": "Wolves"}, "player": aliPlayer, "reason": "kicked",
		}}, 2},
	}
	for _, s := range steps {
		body, err := json.Marshal(s.event)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Handle(ctx, body); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		frames := len(send.got)
		if frames == 0 {
			t.Fatalf("%s: no frames on the first delivery", s.name)
		}

		if err := d.Handle(ctx, body); err != nil {
			t.Fatalf("%s copy: %v", s.name, err)
		}
		if len(send.got) != frames {
			t.Errorf("%s copy sent %d more frames, want none", s.name, len(send.got)-frames)
		}
		if n := countMembers(t, pool, game); n != s.members {
			t.Errorf("%s: %d membership rows, want %d", s.name, n, s.members)
		}
	}
}

func countMembers(t *testing.T, pool *pgxpool.Pool, game string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "select count(*) from conversation_members where game_id = $1", game).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func randomUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

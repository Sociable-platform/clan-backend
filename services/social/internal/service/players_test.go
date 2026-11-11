package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// memStore is an in-memory stand-in for the database that behaves like UpsertPlayer:
// it creates a row the first time a (game, player) pair appears and updates the name after that.
type memStore struct {
	rows   map[string]repository.Player // key: gameID + "/" + externalID
	err    error
	called int
}

func newMemStore() *memStore { return &memStore{rows: map[string]repository.Player{}} }

func (m *memStore) Ping(context.Context) error { return nil }

func (m *memStore) UpsertPlayer(_ context.Context, gameID, externalID, displayName string) (repository.Player, error) {
	m.called++
	if m.err != nil {
		return repository.Player{}, m.err
	}
	key := gameID + "/" + externalID
	p, ok := m.rows[key]
	if !ok {
		p = repository.Player{
			ID:         fmt.Sprintf("player-%d", len(m.rows)+1),
			GameID:     gameID,
			ExternalID: externalID,
			CreatedAt:  time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
		}
	}
	p.Renamed = ok && p.DisplayName != displayName
	p.DisplayName = displayName
	m.rows[key] = p
	return p, nil
}

const gameA = "a6c1e2f0-1111-4222-8333-944455556666"

func TestEnsurePlayerFirstAndRepeatedCalls(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := context.Background()

	first, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.GameID != gameA || first.ExternalID != "u_42" || first.DisplayName != "Ali" {
		t.Fatalf("first call: %+v", first)
	}

	again, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("second call returned a different player: %+v vs %+v", again, first)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows = %d, want 1 (no duplicate)", len(store.rows))
	}
}

func TestEnsurePlayerUpdatesDisplayNameFromToken(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	first, _ := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali")
	renamed, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali the Great")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != first.ID || renamed.DisplayName != "Ali the Great" {
		t.Fatalf("rename: %+v", renamed)
	}
}

func TestEnsurePlayerKeepsGamesApart(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	a, _ := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali")
	b, _ := svc.EnsurePlayer(ctx, "99999999-9999-4999-8999-999999999999", "u_42", "Ali")
	if a.ID == b.ID {
		t.Fatal("the same player ID in two games must be two different players")
	}
}

func TestCleanDisplayName(t *testing.T) {
	long := strings.Repeat("a", 40)
	azeri := strings.Repeat("Ə", 40) // multi-byte letters must be counted as characters, not bytes
	tests := []struct {
		name, in, want string
	}{
		{"trims spaces", "  Ali  ", "Ali"},
		{"empty falls back to the player ID", "", "u_42"},
		{"only spaces falls back to the player ID", "   ", "u_42"},
		{"cut to 32 characters", long, long[:32]},
		{"cut by characters, not bytes", azeri, strings.Repeat("Ə", 32)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanDisplayName(tc.in, "u_42"); got != tc.want {
				t.Errorf("cleanDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEnsurePlayerStoreError(t *testing.T) {
	store := newMemStore()
	store.err = errors.New("database is down")
	if _, err := New(store).EnsurePlayer(context.Background(), gameA, "u_42", "Ali"); err == nil {
		t.Fatal("expected the database error to come back")
	}
}

func (m *memStore) SearchPlayers(context.Context, string, string, string, int) ([]repository.PlayerSearchResult, error) {
	return []repository.PlayerSearchResult{}, nil
}

// searchStore remembers what SearchPlayers was asked and answers with what the test gave it.
type searchStore struct {
	*memStore
	results      []repository.PlayerSearchResult
	err          error
	called       int
	game, player string
	query        string
	limit        int
}

func (s *searchStore) SearchPlayers(_ context.Context, gameID, playerID, query string, limit int) ([]repository.PlayerSearchResult, error) {
	s.called++
	s.game, s.player, s.query, s.limit = gameID, playerID, query, limit
	return s.results, s.err
}

func TestSearchPlayersPassesCallerQueryAndLimit(t *testing.T) {
	store := &searchStore{memStore: newMemStore(), results: []repository.PlayerSearchResult{
		{ID: "p-2", DisplayName: "Aysel", Relation: repository.RelationRequestReceived, RequestID: "req-1"},
	}}
	got, err := New(store).SearchPlayers(context.Background(), gameA, "u_1", "Ali", SearchPlayersInput{Query: "  ays  ", Limit: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.player != "player-1" || store.query != "ays" || store.limit != 5 {
		t.Errorf("store got game=%q player=%q query=%q limit=%d", store.game, store.player, store.query, store.limit)
	}
	want := PlayerSearchResult{ID: "p-2", DisplayName: "Aysel", Relation: "request_received", RequestID: "req-1"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want [%+v]", got, want)
	}
}

func TestSearchPlayersDefaultLimitAndEmptyResult(t *testing.T) {
	store := &searchStore{memStore: newMemStore(), results: []repository.PlayerSearchResult{}}
	got, err := New(store).SearchPlayers(context.Background(), gameA, "u_1", "Ali", SearchPlayersInput{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if store.limit != playerSearchLimit {
		t.Errorf("limit = %d, want the default %d", store.limit, playerSearchLimit)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want an empty list (not nil)", got)
	}
}

func TestSearchPlayersBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   SearchPlayersInput
		code string
	}{
		{"no q", SearchPlayersInput{}, "invalid_query"},
		{"only spaces", SearchPlayersInput{Query: "   "}, "invalid_query"},
		{"q too long", SearchPlayersInput{Query: strings.Repeat("ə", maxPlayerSearchLen+1)}, "invalid_query"},
		{"limit 0", SearchPlayersInput{Query: "a", Limit: "0"}, "invalid_limit"},
		{"limit 101", SearchPlayersInput{Query: "a", Limit: "101"}, "invalid_limit"},
		{"limit not a number", SearchPlayersInput{Query: "a", Limit: "ten"}, "invalid_limit"},
		{"limit with a sign", SearchPlayersInput{Query: "a", Limit: "+5"}, "invalid_limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &searchStore{memStore: newMemStore()}
			_, err := New(store).SearchPlayers(context.Background(), gameA, "u_1", "Ali", c.in)
			var he *httpx.Error
			if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != c.code {
				t.Fatalf("err = %v, want 400 %s", err, c.code)
			}
			if store.called != 0 || store.memStore.called != 0 {
				t.Error("bad input reached the database")
			}
		})
	}
}

func TestSearchPlayersAtMostFiftyCharacters(t *testing.T) {
	store := &searchStore{memStore: newMemStore(), results: []repository.PlayerSearchResult{}}
	q := strings.Repeat("ə", maxPlayerSearchLen) // 50 characters, 100 bytes
	if _, err := New(store).SearchPlayers(context.Background(), gameA, "u_1", "Ali", SearchPlayersInput{Query: q}); err != nil {
		t.Fatalf("50 characters: %v", err)
	}
}

func TestSearchPlayersDatabaseFailure(t *testing.T) {
	store := &searchStore{memStore: newMemStore(), err: errors.New("connection lost")}
	_, err := New(store).SearchPlayers(context.Background(), gameA, "u_1", "Ali", SearchPlayersInput{Query: "a"})
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain error (500)", err)
	}
}

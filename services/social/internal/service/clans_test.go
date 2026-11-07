package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// memStore is used by the player tests; it has no clans.
func (m *memStore) ListClans(context.Context, string, string, int) ([]repository.Clan, error) {
	return []repository.Clan{}, nil
}

// clanStore remembers what ListClans was asked and returns the clans it was given.
type clanStore struct {
	*memStore
	clans  []repository.Clan
	err    error
	called int
	game   string
	query  string
	limit  int
}

func (c *clanStore) ListClans(_ context.Context, gameID, query string, limit int) ([]repository.Clan, error) {
	c.called++
	c.game, c.query, c.limit = gameID, query, limit
	return c.clans, c.err
}

func newClanStore(clans ...repository.Clan) *clanStore {
	return &clanStore{memStore: newMemStore(), clans: clans}
}

func TestListClansPassesGameQueryAndLimit(t *testing.T) {
	store := newClanStore(repository.Clan{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10})
	got, err := New(store).ListClans(context.Background(), gameA, "  wol  ")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.query != "wol" || store.limit != clanListLimit {
		t.Fatalf("store got game=%q query=%q limit=%d", store.game, store.query, store.limit)
	}
	want := Clan{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want [%+v]", got, want)
	}
}

func TestListClansEmptyIsNotNil(t *testing.T) {
	got, err := New(newClanStore()).ListClans(context.Background(), gameA, "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty (non-nil) list", got)
	}
}

func TestListClansRejectsLongSearch(t *testing.T) {
	store := newClanStore()
	svc := New(store)

	// exactly 50 characters (multi-byte letters count as one) is fine
	if _, err := svc.ListClans(context.Background(), gameA, strings.Repeat("Ə", maxClanSearchLen)); err != nil {
		t.Fatalf("50 characters should be allowed: %v", err)
	}
	store.called = 0

	_, err := svc.ListClans(context.Background(), gameA, strings.Repeat("a", maxClanSearchLen+1))
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "invalid_query" {
		t.Fatalf("err = %v, want 400 invalid_query", err)
	}
	if store.called != 0 {
		t.Fatal("the database must not be asked when the search is invalid")
	}
}

func TestListClansStoreError(t *testing.T) {
	store := newClanStore()
	store.err = errors.New("database is down")
	if _, err := New(store).ListClans(context.Background(), gameA, ""); err == nil {
		t.Fatal("expected the database error to come back")
	}
}

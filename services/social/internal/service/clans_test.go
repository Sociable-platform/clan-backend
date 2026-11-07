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

func (m *memStore) CreateClan(context.Context, string, string, string) (repository.Clan, error) {
	return repository.Clan{}, errors.New("memStore has no clans")
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

// creatingStore records what CreateClan was asked and answers with createErr, or a new clan.
type creatingStore struct {
	*memStore
	createErr error
	creates   int
	game      string
	owner     string
	name      string
}

func (c *creatingStore) CreateClan(_ context.Context, gameID, ownerID, name string) (repository.Clan, error) {
	c.creates++
	c.game, c.owner, c.name = gameID, ownerID, name
	if c.createErr != nil {
		return repository.Clan{}, c.createErr
	}
	return repository.Clan{ID: "clan-1", Name: name, MemberCount: 1, MaxMembers: 10}, nil
}

func newCreatingStore() *creatingStore { return &creatingStore{memStore: newMemStore()} }

func TestCreateClanMakesThePlayerTheOwner(t *testing.T) {
	store := newCreatingStore()
	got, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", "  Wolf   Pack ")
	if err != nil {
		t.Fatal(err)
	}
	owner := store.rows[gameA+"/u_42"]
	if owner.ID == "" {
		t.Fatal("the player record should be created for the owner")
	}
	if store.game != gameA || store.owner != owner.ID || store.name != "Wolf Pack" {
		t.Fatalf("store got game=%q owner=%q name=%q", store.game, store.owner, store.name)
	}
	want := Clan{ID: "clan-1", Name: "Wolf Pack", MemberCount: 1, MaxMembers: 10}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCleanClanName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"Wolves", "Wolves"},
		{"abc", "abc"}, // 3 characters: shortest allowed
		{strings.Repeat("a", 20), strings.Repeat("a", 20)}, // 20: longest allowed
		{"  Wolf   Pack  ", "Wolf Pack"},                   // spaces trimmed and squeezed
		{"Şəhər Qartalları", "Şəhər Qartalları"},           // letters outside English count as one each
		{"Ə🐺Ə", "Ə🐺Ə"},                                     // 3 characters, even though each is several bytes
		{"x  y", "x y"},                                    // 3 characters after squeezing
	}
	for _, tc := range ok {
		if got, err := cleanClanName(tc.in); err != nil || got != tc.want {
			t.Errorf("cleanClanName(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	bad := []string{
		"", "   ", "ab", " ab ", "a  ",
		strings.Repeat("a", 21),
		strings.Repeat("Ə", 21),
		"Wolf\x00Pack", "Wolf\u200bPack", // control character, zero-width space
	}
	for _, in := range bad {
		_, err := cleanClanName(in)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "invalid_clan_name" {
			t.Errorf("cleanClanName(%q): err = %v, want 400 invalid_clan_name", in, err)
		}
	}
}

func TestCreateClanInvalidNameTouchesNothing(t *testing.T) {
	store := newCreatingStore()
	if _, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", "ab"); err == nil {
		t.Fatal("expected invalid_clan_name")
	}
	if store.creates != 0 || store.memStore.called != 0 {
		t.Fatalf("an invalid name must not reach the database (creates=%d, player upserts=%d)",
			store.creates, store.memStore.called)
	}
}

func TestCreateClanConflicts(t *testing.T) {
	tests := []struct {
		storeErr error
		code     string
	}{
		{repository.ErrClanNameTaken, "clan_name_taken"},
		{repository.ErrAlreadyInClan, "already_in_clan"},
	}
	for _, tc := range tests {
		store := newCreatingStore()
		store.createErr = tc.storeErr
		_, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", "Wolves")
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusConflict || he.Code != tc.code {
			t.Errorf("%v: err = %v, want 409 %s", tc.storeErr, err, tc.code)
		}
	}
}

func TestCreateClanDatabaseError(t *testing.T) {
	store := newCreatingStore()
	store.createErr = errors.New("database is down")
	_, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", "Wolves")
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error (answered as 500, details only in the log)", err)
	}
}

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// memStore is used by the player tests; it has no clans.
func (m *memStore) ListClans(context.Context, string, string, repository.ListClansOptions) ([]repository.Clan, error) {
	return []repository.Clan{}, nil
}

func (m *memStore) CreateClan(context.Context, string, string, string) (repository.Clan, error) {
	return repository.Clan{}, errors.New("memStore has no clans")
}

func (m *memStore) JoinClan(context.Context, string, string, string, int, string) (repository.Clan, error) {
	return repository.Clan{}, errors.New("memStore has no clans")
}

func (m *memStore) LeaveClan(context.Context, string, string, string) (repository.LeaveResult, error) {
	return repository.LeaveResult{}, errors.New("memStore has no clans")
}

func (m *memStore) KickMember(context.Context, string, string, string, string,
	func(repository.MemberRole, repository.MemberRole) bool) (repository.LeaveResult, error) {
	return repository.LeaveResult{}, errors.New("memStore has no clans")
}

func (m *memStore) RenameClan(context.Context, string, string, string, string) (repository.RenameResult, error) {
	return repository.RenameResult{}, errors.New("memStore has no clans")
}

func (m *memStore) MyClan(context.Context, string, string) (repository.PlayerClan, error) {
	return repository.PlayerClan{}, repository.ErrNotInClan
}

func (m *memStore) ListMembers(context.Context, string, string) ([]repository.Member, error) {
	return nil, repository.ErrClanNotFound
}

// clanStore remembers what ListClans was asked and returns the clans it was given.
type clanStore struct {
	*memStore
	clans  []repository.Clan
	err    error
	called int
	game   string
	query  string
	opts   repository.ListClansOptions
}

func (c *clanStore) ListClans(_ context.Context, gameID, query string, opts repository.ListClansOptions) ([]repository.Clan, error) {
	c.called++
	c.game, c.query, c.opts = gameID, query, opts
	return c.clans, c.err
}

func newClanStore(clans ...repository.Clan) *clanStore {
	return &clanStore{memStore: newMemStore(), clans: clans}
}

func TestListClansPassesGameQueryAndLimit(t *testing.T) {
	store := newClanStore(repository.Clan{ID: "c-1", Name: "Wolves", MemberCount: 3})
	got, err := New(store).ListClans(context.Background(), gameA, ListClansInput{Query: "  wol  "})
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.query != "wol" || store.opts != (repository.ListClansOptions{Limit: clanListLimit, MaxMembers: 10}) {
		t.Fatalf("store got game=%q query=%q opts=%+v", store.game, store.query, store.opts)
	}
	want := Clan{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want [%+v]", got, want)
	}
}

func TestListClansEmptyIsNotNil(t *testing.T) {
	got, err := New(newClanStore()).ListClans(context.Background(), gameA, ListClansInput{})
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
	if _, err := svc.ListClans(context.Background(), gameA, ListClansInput{Query: strings.Repeat("Ə", maxClanSearchLen)}); err != nil {
		t.Fatalf("50 characters should be allowed: %v", err)
	}
	store.called = 0

	_, err := svc.ListClans(context.Background(), gameA, ListClansInput{Query: strings.Repeat("a", maxClanSearchLen+1)})
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
	if _, err := New(store).ListClans(context.Background(), gameA, ListClansInput{}); err == nil {
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
	return repository.Clan{ID: "clan-1", Name: name, MemberCount: 1}, nil
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

// joiningStore records what JoinClan was asked and answers with joinErr, or the joined clan.
type joiningStore struct {
	*memStore
	joinErr error
	joins   int
	game    string
	clan    string
	player  string
	limit   int // the maxMembers the service passed
}

func (j *joiningStore) JoinClan(_ context.Context, gameID, clanID, playerID string, maxMembers int, _ string) (repository.Clan, error) {
	j.joins++
	j.game, j.clan, j.player, j.limit = gameID, clanID, playerID, maxMembers
	if j.joinErr != nil {
		return repository.Clan{}, j.joinErr
	}
	return repository.Clan{ID: clanID, Name: "Wolves", MemberCount: 2}, nil
}

func newJoiningStore() *joiningStore { return &joiningStore{memStore: newMemStore()} }

// leavingStore records what LeaveClan was asked and answers with leaveErr.
type leavingStore struct {
	*memStore
	leaveErr error
	result   repository.LeaveResult // what a successful leave reports
	leaves   int
	game     string
	clan     string
	external string
}

func (l *leavingStore) LeaveClan(_ context.Context, gameID, clanID, externalID string) (repository.LeaveResult, error) {
	l.leaves++
	l.game, l.clan, l.external = gameID, clanID, externalID
	if l.leaveErr != nil {
		return repository.LeaveResult{}, l.leaveErr
	}
	return l.result, nil
}

func newLeavingStore() *leavingStore { return &leavingStore{memStore: newMemStore()} }

const someClan = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"

func TestJoinClanAddsThePlayer(t *testing.T) {
	store := newJoiningStore()
	got, err := New(store).JoinClan(context.Background(), gameA, "u_42", "Ali", strings.ToUpper(someClan))
	if err != nil {
		t.Fatal(err)
	}
	player := store.rows[gameA+"/u_42"]
	if player.ID == "" {
		t.Fatal("the player record should be created")
	}
	if store.game != gameA || store.clan != someClan || store.player != player.ID {
		t.Fatalf("store got game=%q clan=%q player=%q", store.game, store.clan, store.player)
	}
	if want := (Clan{ID: someClan, Name: "Wolves", MemberCount: 2, MaxMembers: 10}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestJoinClanBadIDIsNotFound(t *testing.T) {
	for _, id := range []string{"", "abc", someClan + "0", "' or 1=1 --"} {
		store := newJoiningStore()
		_, err := New(store).JoinClan(context.Background(), gameA, "u_42", "Ali", id)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "clan_not_found" {
			t.Errorf("id %q: err = %v, want 404 clan_not_found", id, err)
		}
		if store.joins != 0 || store.memStore.called != 0 {
			t.Errorf("id %q: a bad ID must not reach the database", id)
		}
	}
}

func TestJoinClanErrors(t *testing.T) {
	tests := []struct {
		storeErr error
		status   int
		code     string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrAlreadyInClan, http.StatusConflict, "already_in_clan"},
		{repository.ErrClanFull, http.StatusConflict, "clan_full"},
	}
	for _, tc := range tests {
		store := newJoiningStore()
		store.joinErr = tc.storeErr
		_, err := New(store).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != tc.status || he.Code != tc.code {
			t.Errorf("%v: err = %v, want %d %s", tc.storeErr, err, tc.status, tc.code)
		}
	}

	store := newJoiningStore()
	store.joinErr = errors.New("database is down")
	_, err := New(store).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan)
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error", err)
	}
}

// readingStore answers MyClan and ListMembers with fixed data or errors, and records the calls.
type readingStore struct {
	*memStore
	myClan     repository.PlayerClan
	myClanErr  error
	members    []repository.Member
	membersErr error
	listCalls  int
	game       string
	externalID string
	clan       string
}

func (s *readingStore) MyClan(_ context.Context, gameID, externalID string) (repository.PlayerClan, error) {
	s.game, s.externalID = gameID, externalID
	return s.myClan, s.myClanErr
}

func (s *readingStore) ListMembers(_ context.Context, gameID, clanID string) ([]repository.Member, error) {
	s.listCalls++
	s.game, s.clan = gameID, clanID
	return s.members, s.membersErr
}

func newReadingStore() *readingStore { return &readingStore{memStore: newMemStore()} }

func TestLeaveClanPassesTheTokensGameAndPlayer(t *testing.T) {
	store := newLeavingStore()
	if err := New(store).LeaveClan(context.Background(), gameA, "u_42", strings.ToUpper(someClan)); err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.clan != someClan || store.external != "u_42" {
		t.Fatalf("store got game=%q clan=%q player=%q", store.game, store.clan, store.external)
	}
	// leaving never creates the player record
	if len(store.rows) != 0 || store.memStore.called != 0 {
		t.Fatalf("leaving must not touch the player table: rows=%d calls=%d", len(store.rows), store.memStore.called)
	}
}

func TestLeaveClanBadIDIsNotFound(t *testing.T) {
	for _, id := range []string{"", "abc", someClan + "0", "' or 1=1 --"} {
		store := newLeavingStore()
		err := New(store).LeaveClan(context.Background(), gameA, "u_42", id)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "clan_not_found" {
			t.Errorf("id %q: err = %v, want 404 clan_not_found", id, err)
		}
		if store.leaves != 0 {
			t.Errorf("id %q: a bad ID must not reach the database", id)
		}
	}
}

func TestLeaveClanErrors(t *testing.T) {
	tests := []struct {
		storeErr error
		status   int
		code     string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrNotInClan, http.StatusConflict, "not_in_clan"},
	}
	for _, tc := range tests {
		store := newLeavingStore()
		store.leaveErr = tc.storeErr
		err := New(store).LeaveClan(context.Background(), gameA, "u_42", someClan)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != tc.status || he.Code != tc.code {
			t.Errorf("%v: err = %v, want %d %s", tc.storeErr, err, tc.status, tc.code)
		}
	}

	store := newLeavingStore()
	store.leaveErr = errors.New("database is down")
	err := New(store).LeaveClan(context.Background(), gameA, "u_42", someClan)
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error", err)
	}
}

func TestMyClanReturnsClanAndRole(t *testing.T) {
	store := newReadingStore()
	store.myClan = repository.PlayerClan{
		Clan: repository.Clan{ID: someClan, Name: "Wolves", MemberCount: 3},
		Role: "owner",
	}
	got, err := New(store).MyClan(context.Background(), gameA, "u_42")
	if err != nil {
		t.Fatal(err)
	}
	want := PlayerClan{Clan: Clan{ID: someClan, Name: "Wolves", MemberCount: 3, MaxMembers: 10}, Role: "owner"}
	if got != want || store.game != gameA || store.externalID != "u_42" {
		t.Fatalf("got %+v (store asked game=%q player=%q), want %+v", got, store.game, store.externalID, want)
	}
	if store.memStore.called != 0 {
		t.Fatal("reading my clan must not create the player record")
	}
}

func TestMyClanNotInClan(t *testing.T) {
	store := newReadingStore()
	store.myClanErr = repository.ErrNotInClan
	_, err := New(store).MyClan(context.Background(), gameA, "u_42")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "not_in_clan" {
		t.Fatalf("err = %v, want 404 not_in_clan", err)
	}

	store.myClanErr = errors.New("database is down")
	if _, err := New(store).MyClan(context.Background(), gameA, "u_42"); err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error", err)
	}
}

func TestListMembers(t *testing.T) {
	joined := time.Date(2026, 11, 7, 17, 30, 0, 0, time.UTC)
	store := newReadingStore()
	store.members = []repository.Member{
		{PlayerID: "p-1", DisplayName: "Ali", Role: "owner", JoinedAt: joined},
		{PlayerID: "p-2", DisplayName: "Leyla", Role: "member", JoinedAt: joined.Add(time.Minute)},
	}
	got, err := New(store).ListMembers(context.Background(), gameA, strings.ToUpper(someClan))
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.clan != someClan {
		t.Fatalf("store asked game=%q clan=%q", store.game, store.clan)
	}
	if len(got) != 2 || got[0] != (Member{PlayerID: "p-1", DisplayName: "Ali", Role: "owner", JoinedAt: joined}) || got[1].DisplayName != "Leyla" {
		t.Fatalf("got %+v", got)
	}
}

func TestListMembersNotFound(t *testing.T) {
	for _, id := range []string{"", "abc", "' or 1=1 --"} {
		store := newReadingStore()
		_, err := New(store).ListMembers(context.Background(), gameA, id)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "clan_not_found" {
			t.Errorf("id %q: err = %v, want 404 clan_not_found", id, err)
		}
		if store.listCalls != 0 {
			t.Errorf("id %q: a bad ID must not reach the database", id)
		}
	}

	store := newReadingStore()
	store.membersErr = repository.ErrClanNotFound
	_, err := New(store).ListMembers(context.Background(), gameA, someClan)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotFound || he.Code != "clan_not_found" {
		t.Fatalf("clan of another game: err = %v, want 404 clan_not_found", err)
	}
}

func TestListClansOptionsFromInput(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   ListClansInput
		want repository.ListClansOptions
	}{
		{"nothing given", ListClansInput{}, repository.ListClansOptions{Limit: clanListLimit, MaxMembers: 10}},
		{"featured clans", ListClansInput{Limit: "3", Sort: "members", HasSpace: "true"},
			repository.ListClansOptions{Limit: 3, ByMembers: true, OnlyWithSpace: true, MaxMembers: 10}},
		{"sort by name said out loud", ListClansInput{Sort: "name"}, repository.ListClansOptions{Limit: clanListLimit, MaxMembers: 10}},
		{"hasSpace false", ListClansInput{HasSpace: "false"}, repository.ListClansOptions{Limit: clanListLimit, MaxMembers: 10}},
		{"limit 1", ListClansInput{Limit: "1"}, repository.ListClansOptions{Limit: 1, MaxMembers: 10}},
		{"limit at the top", ListClansInput{Limit: "100"}, repository.ListClansOptions{Limit: 100, MaxMembers: 10}},
		{"a search does not change the options", ListClansInput{Query: "nova"}, repository.ListClansOptions{Limit: clanListLimit, MaxMembers: 10}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newClanStore()
			if _, err := New(store).ListClans(context.Background(), gameA, tt.in); err != nil {
				t.Fatal(err)
			}
			if store.opts != tt.want {
				t.Fatalf("opts = %+v, want %+v", store.opts, tt.want)
			}
		})
	}
}

func TestListClansRejectsBadOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   ListClansInput
		code string
	}{
		{"limit zero", ListClansInput{Limit: "0"}, "invalid_limit"},
		{"limit negative", ListClansInput{Limit: "-1"}, "invalid_limit"},
		{"limit too big", ListClansInput{Limit: "101"}, "invalid_limit"},
		{"limit not a number", ListClansInput{Limit: "three"}, "invalid_limit"},
		{"limit with a decimal", ListClansInput{Limit: "2.5"}, "invalid_limit"},
		{"limit with a plus sign", ListClansInput{Limit: "+3"}, "invalid_limit"},
		{"unknown sort", ListClansInput{Sort: "random"}, "invalid_sort"},
		{"sort in capitals", ListClansInput{Sort: "MEMBERS"}, "invalid_sort"},
		{"hasSpace yes", ListClansInput{HasSpace: "yes"}, "invalid_has_space"},
		{"hasSpace 1", ListClansInput{HasSpace: "1"}, "invalid_has_space"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newClanStore()
			_, err := New(store).ListClans(context.Background(), gameA, tt.in)
			var he *httpx.Error
			if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != tt.code {
				t.Fatalf("err = %v, want 400 %s", err, tt.code)
			}
			if store.called != 0 {
				t.Fatal("the database must not be asked when the input is invalid")
			}
		})
	}
}

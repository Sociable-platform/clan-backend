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

// renamingStore records what RenameClan was asked and answers with renameErr, or result.
type renamingStore struct {
	*memStore
	renameErr                  error
	result                     repository.RenameResult
	renames                    int
	game, clan, external, name string
}

func (r *renamingStore) RenameClan(_ context.Context, gameID, clanID, ownerExternalID, name string) (repository.RenameResult, error) {
	r.renames++
	r.game, r.clan, r.external, r.name = gameID, clanID, ownerExternalID, name
	if r.renameErr != nil {
		return repository.RenameResult{}, r.renameErr
	}
	return r.result, nil
}

func newRenamingStore(changed bool) *renamingStore {
	prev := "Wolves"
	if !changed {
		prev = "Grey Wolves"
	}
	return &renamingStore{memStore: newMemStore(), result: repository.RenameResult{
		Clan:         repository.Clan{ID: someClan, Name: "Grey Wolves", MemberCount: 4},
		PreviousName: prev, Changed: changed,
	}}
}

func TestRenameClanCleansTheNameAndPublishes(t *testing.T) {
	store := newRenamingStore(true)
	svc, rec := friendService(store)
	got, err := svc.RenameClan(context.Background(), gameA, "u_42", strings.ToUpper(someClan), "  Grey   Wolves ")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.external != "u_42" || store.clan != someClan || store.name != "Grey Wolves" {
		t.Fatalf("store got game=%q caller=%q clan=%q name=%q", store.game, store.external, store.clan, store.name)
	}
	if want := (Clan{ID: someClan, Name: "Grey Wolves", MemberCount: 4, MaxMembers: 10, JoinPolicy: "open"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	wantData(t, oneEvent(t, rec, EventClanRenamed), map[string]any{
		"clan":          ref(someClan, "Grey Wolves"),
		"previous_name": "Wolves",
	})
}

func TestRenameClanToTheSameNamePublishesNothing(t *testing.T) {
	svc, rec := friendService(newRenamingStore(false))
	got, err := svc.RenameClan(context.Background(), gameA, "u_42", someClan, "Grey Wolves")
	if err != nil || got.Name != "Grey Wolves" {
		t.Fatalf("got %+v, %v; want the clan back", got, err)
	}
	if evs := rec.Events(); len(evs) != 0 {
		t.Fatalf("published %+v for a name that did not change", evs)
	}
}

func TestRenameClanBadInputDoesNotReachTheDatabase(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"", "ab", "   x  ", strings.Repeat("a", maxClanNameLen+1), "Wol\u200bves"} {
		store := newRenamingStore(true)
		_, err := New(store).RenameClan(ctx, gameA, "u_42", someClan, name)
		wantHTTPError(t, err, http.StatusBadRequest, "invalid_clan_name")
		if store.renames != 0 {
			t.Errorf("name %q: the database was asked", name)
		}
	}
	store := newRenamingStore(true)
	_, err := New(store).RenameClan(ctx, gameA, "u_42", "not-a-uuid", "Grey Wolves")
	wantHTTPError(t, err, http.StatusNotFound, "clan_not_found")
	if store.renames != 0 {
		t.Error("bad clan ID: the database was asked")
	}
}

func TestRenameClanErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrNotClanOwner, http.StatusForbidden, "not_clan_owner"},
		{repository.ErrClanNameTaken, http.StatusConflict, "clan_name_taken"},
	}
	for _, c := range cases {
		store := newRenamingStore(true)
		store.renameErr = c.err
		svc, rec := friendService(store)
		_, err := svc.RenameClan(context.Background(), gameA, "u_42", someClan, "Grey Wolves")
		wantHTTPError(t, err, c.status, c.code)
		if evs := rec.Events(); len(evs) != 0 {
			t.Errorf("%v: published %+v after failing", c.err, evs)
		}
	}
	store := newRenamingStore(true)
	store.renameErr = errors.New("database is down")
	_, err := New(store).RenameClan(context.Background(), gameA, "u_42", someClan, "Grey Wolves")
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error (answered as 500)", err)
	}
}

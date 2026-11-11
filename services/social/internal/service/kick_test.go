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

const somePlayer = "7d2f1c0e-2b5a-4e8f-9a61-3c4d5e6f7a8b"

// kickingStore records what KickMember was asked and answers with kickErr, or result.
type kickingStore struct {
	*memStore
	kickErr                        error
	result                         repository.LeaveResult
	kicks                          int
	game, clan, external, playerID string
}

func (k *kickingStore) KickMember(_ context.Context, gameID, clanID, ownerExternalID, playerID string,
	_ func(repository.MemberRole, repository.MemberRole) bool) (repository.LeaveResult, error) {
	k.kicks++
	k.game, k.clan, k.external, k.playerID = gameID, clanID, ownerExternalID, playerID
	if k.kickErr != nil {
		return repository.LeaveResult{}, k.kickErr
	}
	return k.result, nil
}

func newKickingStore() *kickingStore { return &kickingStore{memStore: newMemStore()} }

func TestKickMemberPassesTheTokensGameAndCaller(t *testing.T) {
	store := newKickingStore()
	err := New(store).KickMember(context.Background(), gameA, "u_42", strings.ToUpper(someClan), strings.ToUpper(somePlayer))
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.external != "u_42" || store.clan != someClan || store.playerID != somePlayer {
		t.Fatalf("store got game=%q caller=%q clan=%q player=%q; want the IDs in lower case",
			store.game, store.external, store.clan, store.playerID)
	}
}

func TestKickMemberBadIDsDoNotReachTheDatabase(t *testing.T) {
	cases := []struct{ clan, player, code string }{
		{"not-a-uuid", somePlayer, "clan_not_found"},
		{"", somePlayer, "clan_not_found"},
		{someClan, "u_7", "member_not_found"},
		{someClan, "", "member_not_found"},
	}
	for _, c := range cases {
		store := newKickingStore()
		err := New(store).KickMember(context.Background(), gameA, "u_42", c.clan, c.player)
		wantHTTPError(t, err, http.StatusNotFound, c.code)
		if store.kicks != 0 {
			t.Errorf("clan %q player %q: the database was asked", c.clan, c.player)
		}
	}
}

func TestKickMemberErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrNotAllowed, http.StatusForbidden, "not_allowed"},
		{repository.ErrKickSelf, http.StatusBadRequest, "cannot_kick_self"},
		{repository.ErrMemberNotFound, http.StatusNotFound, "member_not_found"},
	}
	for _, c := range cases {
		store := newKickingStore()
		store.kickErr = c.err
		svc, rec := friendService(store)
		err := svc.KickMember(context.Background(), gameA, "u_42", someClan, somePlayer)
		wantHTTPError(t, err, c.status, c.code)
		if evs := rec.Events(); len(evs) != 0 {
			t.Errorf("%v: published %+v after failing", c.err, evs)
		}
	}

	// any other error stays an internal one (500, details only in the log)
	store := newKickingStore()
	store.kickErr = errors.New("database is down")
	err := New(store).KickMember(context.Background(), gameA, "u_42", someClan, somePlayer)
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain internal error (answered as 500)", err)
	}
}

func TestKickMemberPublishesMemberLeftAsKicked(t *testing.T) {
	store := newKickingStore()
	store.result = repository.LeaveResult{
		ClanID: someClan, ClanName: "Wolves",
		Player:   repository.ClanPlayer{ID: somePlayer, DisplayName: "Leyla"},
		KickedBy: &repository.ClanPlayer{ID: "p-ali", DisplayName: "Ali"},
	}
	svc, rec := friendService(store)
	if err := svc.KickMember(context.Background(), gameA, "u_42", someClan, somePlayer); err != nil {
		t.Fatal(err)
	}
	wantData(t, oneEvent(t, rec, EventClanMemberLeft), map[string]any{
		"clan":      ref(someClan, "Wolves"),
		"player":    ref(somePlayer, "Leyla"),
		"reason":    "kicked",
		"kicked_by": ref("p-ali", "Ali"),
		"new_owner": nil,
	})
}

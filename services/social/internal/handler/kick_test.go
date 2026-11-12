package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

const kickPlayerID = "7d2f1c0e-2b5a-4e8f-9a61-3c4d5e6f7a8b"

var kickPath = "/api/v1/clans/" + joinClanID + "/members/" + kickPlayerID

// kickingService remembers what kickMember passed on and answers with err.
type kickingService struct {
	fakeService
	err                                  error
	called                               bool
	gameID, externalID, clanID, playerID string
}

func (s *kickingService) KickMember(_ context.Context, gameID, externalID, clanID, playerID string) error {
	s.called = true
	s.gameID, s.externalID, s.clanID, s.playerID = gameID, externalID, clanID, playerID
	return s.err
}

func TestKickMemberReturns204WithNoBody(t *testing.T) {
	svc := &kickingService{}
	rec := postJoin(t, svc, http.MethodDelete, kickPath, false)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d, body %q; want 204 and nothing", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID || svc.playerID != kickPlayerID {
		t.Fatalf("service got game=%q caller=%q clan=%q player=%q; want the token's game and player, and the URL's IDs",
			svc.gameID, svc.externalID, svc.clanID, svc.playerID)
	}
}

func TestKickMemberPassesServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusForbidden, "not_clan_owner", "x"), http.StatusForbidden, "not_clan_owner"},
		{httpx.NewError(http.StatusBadRequest, "cannot_kick_self", "x"), http.StatusBadRequest, "cannot_kick_self"},
		{httpx.NewError(http.StatusNotFound, "member_not_found", "x"), http.StatusNotFound, "member_not_found"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := postJoin(t, &kickingService{err: tc.err}, http.MethodDelete, kickPath, false)
		var e httpx.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: status %d, body %s; want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestKickMemberRouting(t *testing.T) {
	// no token
	svc := &kickingService{}
	if rec := postJoin(t, svc, http.MethodDelete, kickPath, true); rec.Code != http.StatusUnauthorized || svc.called {
		t.Errorf("no token: status %d, service called=%v; want 401 and no call", rec.Code, svc.called)
	}
	// only DELETE removes; POST and GET on the same path are not routes
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		svc = &kickingService{}
		if rec := postJoin(t, svc, method, kickPath, false); rec.Code == http.StatusNoContent || svc.called {
			t.Errorf("%s kick: status %d, service called=%v; want it refused", method, rec.Code, svc.called)
		}
	}
	// the player ID is one path segment
	svc = &kickingService{}
	if rec := postJoin(t, svc, http.MethodDelete, kickPath+"/extra", false); svc.called {
		t.Errorf("extra path segment: status %d, but the service was called", rec.Code)
	}
	// DELETE on the member list itself is not a route
	svc = &kickingService{}
	if rec := postJoin(t, svc, http.MethodDelete, "/api/v1/clans/"+joinClanID+"/members", false); svc.called || rec.Code == http.StatusNoContent {
		t.Errorf("DELETE members: status %d, service called=%v; want it refused", rec.Code, svc.called)
	}
}

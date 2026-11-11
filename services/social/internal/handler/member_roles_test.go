package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// roleService remembers which change was asked, with what, and answers with err or the role.
type roleService struct {
	fakeService
	err                                  error
	called                               string // "promote" or "demote"
	gameID, externalID, clanID, playerID string
}

func (s *roleService) answer(kind, gameID, externalID, clanID, playerID string) (service.MemberRole, error) {
	s.called = kind
	s.gameID, s.externalID, s.clanID, s.playerID = gameID, externalID, clanID, playerID
	if s.err != nil {
		return service.MemberRole{}, s.err
	}
	return service.MemberRole{PlayerID: playerID, DisplayName: "Leyla", Role: kind + "d-role"}, nil
}

func (s *roleService) PromoteMember(_ context.Context, g, e, c, p string) (service.MemberRole, error) {
	return s.answer("promote", g, e, c, p)
}

func (s *roleService) DemoteMember(_ context.Context, g, e, c, p string) (service.MemberRole, error) {
	return s.answer("demote", g, e, c, p)
}

const rolePlayerID = "7d2f1c0e-2b5a-4e8f-9a61-3c4d5e6f7a8b"

var memberPath = "/api/v1/clans/" + joinClanID + "/members/" + rolePlayerID

func TestPromoteAndDemoteReturnTheNewRole(t *testing.T) {
	for _, kind := range []string{"promote", "demote"} {
		svc := &roleService{}
		rec := postJoin(t, svc, http.MethodPost, memberPath+"/"+kind, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", kind, rec.Code, rec.Body.String())
		}
		if svc.called != kind || svc.gameID != tokenGame || svc.externalID != "u_42" ||
			svc.clanID != joinClanID || svc.playerID != rolePlayerID {
			t.Fatalf("%s: service got %+v; want the token's game and player, and the URL's IDs", kind, svc)
		}
		var got memberRoleResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := (memberRoleResponse{PlayerID: rolePlayerID, DisplayName: "Leyla", Role: kind + "d-role"}); got != want {
			t.Fatalf("%s: body %+v, want %+v", kind, got, want)
		}
	}
}

func TestPromoteAndDemotePassServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusNotFound, "member_not_found", "x"), http.StatusNotFound, "member_not_found"},
		{httpx.NewError(http.StatusForbidden, "not_allowed", "x"), http.StatusForbidden, "not_allowed"},
		{httpx.NewError(http.StatusConflict, "already_lowest_role", "x"), http.StatusConflict, "already_lowest_role"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, kind := range []string{"promote", "demote"} {
		for _, tc := range tests {
			rec := postJoin(t, &roleService{err: tc.err}, http.MethodPost, memberPath+"/"+kind, false)
			var e httpx.ErrorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Errorf("%s %v: status %d, body %s; want %d %s", kind, tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		}
	}
}

func TestPromoteAndDemoteRouting(t *testing.T) {
	for _, kind := range []string{"promote", "demote"} {
		svc := &roleService{}
		if rec := postJoin(t, svc, http.MethodPost, memberPath+"/"+kind, true); rec.Code != http.StatusUnauthorized || svc.called != "" {
			t.Errorf("%s without a token: status %d, called %q; want 401 and no call", kind, rec.Code, svc.called)
		}
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			svc = &roleService{}
			if rec := postJoin(t, svc, method, memberPath+"/"+kind, false); rec.Code == http.StatusOK || svc.called != "" {
				t.Errorf("%s %s: status %d, called %q; want it refused", method, kind, rec.Code, svc.called)
			}
		}
	}
	svc := &roleService{}
	if rec := postJoin(t, svc, http.MethodPost, memberPath+"/crown", false); rec.Code == http.StatusOK || svc.called != "" {
		t.Errorf("an unknown action: status %d, called %q; want it refused", rec.Code, svc.called)
	}
}

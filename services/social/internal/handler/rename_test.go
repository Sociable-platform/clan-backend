package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// renamingService remembers what renameClan passed on and answers with err, or the clan.
type renamingService struct {
	fakeService
	err                              error
	called                           bool
	gameID, externalID, clanID, name string
}

func (s *renamingService) RenameClan(_ context.Context, gameID, externalID, clanID, name string) (service.Clan, error) {
	s.called = true
	s.gameID, s.externalID, s.clanID, s.name = gameID, externalID, clanID, name
	if s.err != nil {
		return service.Clan{}, s.err
	}
	return service.Clan{ID: clanID, Name: name, MemberCount: 4, MaxMembers: 10}, nil
}

// sendRename sends method path with body and a real player token for tokenGame, unless noToken.
func sendRename(t *testing.T, svc Service, method, path, body string, noToken bool) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if !noToken {
		tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: tokenGame, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

var renamePath = "/api/v1/clans/" + joinClanID

func TestRenameClanReturns200WithTheClan(t *testing.T) {
	svc := &renamingService{}
	rec := sendRename(t, svc, http.MethodPatch, renamePath, `{"name":"Grey Wolves"}`, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID || svc.name != "Grey Wolves" {
		t.Fatalf("service got game=%q caller=%q clan=%q name=%q", svc.gameID, svc.externalID, svc.clanID, svc.name)
	}
	var got clanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := (clanResponse{ID: joinClanID, Name: "Grey Wolves", MemberCount: 4, MaxMembers: 10}); got != want {
		t.Fatalf("body %+v, want %+v", got, want)
	}
}

func TestRenameClanBadBody(t *testing.T) {
	for _, body := range []string{``, `{`, `{"name":"x","extra":1}`, `["Wolves"]`} {
		svc := &renamingService{}
		rec := sendRename(t, svc, http.MethodPatch, renamePath, body, false)
		if rec.Code != http.StatusBadRequest || svc.called {
			t.Errorf("body %q: status %d, service called=%v; want 400 and no call", body, rec.Code, svc.called)
		}
	}
}

func TestRenameClanPassesServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusBadRequest, "invalid_clan_name", "x"), http.StatusBadRequest, "invalid_clan_name"},
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusForbidden, "not_clan_owner", "x"), http.StatusForbidden, "not_clan_owner"},
		{httpx.NewError(http.StatusConflict, "clan_name_taken", "x"), http.StatusConflict, "clan_name_taken"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := sendRename(t, &renamingService{err: tc.err}, http.MethodPatch, renamePath, `{"name":"Grey Wolves"}`, false)
		var e httpx.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: status %d, body %s; want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestRenameClanRouting(t *testing.T) {
	svc := &renamingService{}
	if rec := sendRename(t, svc, http.MethodPatch, renamePath, `{"name":"Grey Wolves"}`, true); rec.Code != http.StatusUnauthorized || svc.called {
		t.Errorf("no token: status %d, service called=%v; want 401 and no call", rec.Code, svc.called)
	}
	// only PATCH renames
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodGet} {
		svc = &renamingService{}
		if rec := sendRename(t, svc, method, renamePath, `{"name":"Grey Wolves"}`, false); rec.Code == http.StatusOK || svc.called {
			t.Errorf("%s clan: status %d, service called=%v; want it refused", method, rec.Code, svc.called)
		}
	}
	// PATCH on the clan list is not a route
	svc = &renamingService{}
	if rec := sendRename(t, svc, http.MethodPatch, "/api/v1/clans", `{"name":"Grey Wolves"}`, false); svc.called || rec.Code == http.StatusOK {
		t.Errorf("PATCH /clans: status %d, service called=%v; want it refused", rec.Code, svc.called)
	}
}

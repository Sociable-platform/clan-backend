package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

var (
	invitedAt   = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	invitedTill = invitedAt.Add(7 * 24 * time.Hour)
)

func developerFake() fakeService {
	return fakeService{auth: func(string) (string, error) { return "d1", nil }}
}

func TestInviteToGame_Created(t *testing.T) {
	var gotDev, gotGame, gotEmail string
	svc := developerFake()
	svc.inviteToGame = func(dev, game, email string) (service.GameInvite, error) {
		gotDev, gotGame, gotEmail = dev, game, email
		return service.GameInvite{ID: "i1", GameID: game, GameName: "Space", Email: "carol@example.com", InvitedByName: "Alice",
			CreatedAt: invitedAt, ExpiresAt: invitedTill}, nil
	}
	// The game and the inviter come from the path and the token; a developer ID in the body is refused.
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/invites", `{"email":"Carol@Example.com"}`, "tok")
	if rec.Code != http.StatusCreated || gotDev != "d1" || gotGame != "g1" || gotEmail != "Carol@Example.com" {
		t.Fatalf("%d %s (developer %q, game %q, email %q)", rec.Code, rec.Body.String(), gotDev, gotGame, gotEmail)
	}
	want := `{"id":"i1","email":"carol@example.com","invitedByName":"Alice","createdAt":"2026-11-17T10:00:00Z","expiresAt":"2026-11-24T10:00:00Z"}` + "\n"
	if rec.Body.String() != want {
		t.Errorf("body\n got %s\nwant %s", rec.Body.String(), want)
	}
	rec = call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/invites", `{"email":"a@example.com","gameId":"g2"}`, "tok")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestInviteErrorsReachTheClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    *httpx.Error
		status int
		code   string
	}{
		{"already on the game", httpx.NewError(http.StatusConflict, "already_member", "x"), http.StatusConflict, "already_member"},
		{"too many", httpx.NewError(http.StatusTooManyRequests, "rate_limited", "x"), http.StatusTooManyRequests, "rate_limited"},
		{"bad address", httpx.NewError(http.StatusBadRequest, "invalid_email", "x"), http.StatusBadRequest, "invalid_email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := developerFake()
			svc.inviteToGame = func(string, string, string) (service.GameInvite, error) { return service.GameInvite{}, tc.err }
			rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/invites", `{"email":"a@example.com"}`, "tok")
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Errorf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListGameInvites(t *testing.T) {
	svc := developerFake()
	svc.gameInvites = func(dev, game string) ([]service.GameInvite, error) {
		return []service.GameInvite{{ID: "i1", Email: "carol@example.com", InvitedByName: "Alice", CreatedAt: invitedAt, ExpiresAt: invitedTill}}, nil
	}
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/invites", "", "tok")
	want := `{"invites":[{"id":"i1","email":"carol@example.com","invitedByName":"Alice","createdAt":"2026-11-17T10:00:00Z","expiresAt":"2026-11-24T10:00:00Z"}]}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	svc.gameInvites = func(string, string) ([]service.GameInvite, error) { return nil, nil }
	if rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/invites", "", "tok"); rec.Body.String() != `{"invites":[]}`+"\n" {
		t.Errorf("an empty list is %s, want [] and not null", rec.Body.String())
	}
}

func TestCancelGameInvite(t *testing.T) {
	var gotGame, gotInvite string
	svc := developerFake()
	svc.cancelGameInvite = func(_, game, invite string) error { gotGame, gotInvite = game, invite; return nil }
	rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/games/g1/invites/i9", "", "tok")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || gotGame != "g1" || gotInvite != "i9" {
		t.Fatalf("%d %q (game %q, invite %q)", rec.Code, rec.Body.String(), gotGame, gotInvite)
	}
	svc.cancelGameInvite = func(string, string, string) error {
		return httpx.NewError(http.StatusNotFound, "invite_not_found", "x")
	}
	if rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/games/g1/invites/i9", "", "tok"); rec.Code != http.StatusNotFound || errorCode(t, rec) != "invite_not_found" {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMyInvitesAcceptAndDecline(t *testing.T) {
	var accepted, declined string
	svc := developerFake()
	svc.myInvites = func(dev string) ([]service.GameInvite, error) {
		return []service.GameInvite{{ID: "i1", GameID: "g1", GameName: "Space", Email: "me@example.com", InvitedByName: "Alice", CreatedAt: invitedAt, ExpiresAt: invitedTill}}, nil
	}
	svc.acceptInvite = func(_, id string) (service.Game, error) {
		accepted = id
		return service.Game{ID: "g1", Role: "member", Name: "Space", CreatedAt: invitedAt}, nil
	}
	svc.declineInvite = func(_, id string) error { declined = id; return nil }

	// The invited person sees which game and who asked, and nothing about the address.
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/invites", "", "tok")
	want := `{"invites":[{"id":"i1","gameId":"g1","gameName":"Space","invitedByName":"Alice","createdAt":"2026-11-17T10:00:00Z","expiresAt":"2026-11-24T10:00:00Z"}]}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, svc, discardLog(), http.MethodPost, "/api/v1/invites/i1/accept", "", "tok")
	if rec.Code != http.StatusOK || accepted != "i1" || !strings.Contains(rec.Body.String(), `"role":"member"`) || !strings.Contains(rec.Body.String(), `"id":"g1"`) {
		t.Fatalf("accept: %d %s (accepted %q)", rec.Code, rec.Body.String(), accepted)
	}
	rec = call(t, svc, discardLog(), http.MethodPost, "/api/v1/invites/i2/decline", "", "tok")
	if rec.Code != http.StatusNoContent || declined != "i2" {
		t.Fatalf("decline: %d %s (declined %q)", rec.Code, rec.Body.String(), declined)
	}
}

func TestAcceptInviteErrors(t *testing.T) {
	for _, tc := range []struct {
		err    *httpx.Error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "invite_not_found", "x"), http.StatusNotFound, "invite_not_found"},
		{httpx.NewError(http.StatusGone, "invite_expired", "x"), http.StatusGone, "invite_expired"},
	} {
		svc := developerFake()
		svc.acceptInvite = func(string, string) (service.Game, error) { return service.Game{}, tc.err }
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/invites/i1/accept", "", "tok")
		if rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", tc.code, rec.Code, rec.Body.String())
		}
	}
}

// Nothing about invitations is open without a login, and the invited person needs no game to use them.
func TestInviteRoutesNeedALogin(t *testing.T) {
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/games/g1/invites"}, {http.MethodGet, "/api/v1/games/g1/invites"},
		{http.MethodDelete, "/api/v1/games/g1/invites/i1"}, {http.MethodGet, "/api/v1/invites"},
		{http.MethodPost, "/api/v1/invites/i1/accept"}, {http.MethodPost, "/api/v1/invites/i1/decline"},
	} {
		if rec := call(t, fakeService{}, discardLog(), r.method, r.path, "{}", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", r.method, r.path, rec.Code)
		}
	}
}

// A suspended developer can read their invitations (GET is not guarded) but not answer one.
func TestASuspendedDeveloperCanListButNotAnswerInvites(t *testing.T) {
	svc := suspendedDeveloper()
	svc.myInvites = func(string) ([]service.GameInvite, error) { return nil, nil }
	if rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/invites", "", "dev-token"); rec.Code != http.StatusOK {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/invites/i1/accept", "", "dev-token"); rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
		t.Errorf("accept: %d %s", rec.Code, rec.Body.String())
	}
}

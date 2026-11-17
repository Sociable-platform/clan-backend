package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

func TestRemoveGameMemberHandler(t *testing.T) {
	var gotDev, gotGame, gotMember string
	svc := developerFake()
	svc.removeGameMember = func(dev, game, member string) error { gotDev, gotGame, gotMember = dev, game, member; return nil }

	rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/games/g1/members/d2", "", "tok")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || gotDev != "d1" || gotGame != "g1" || gotMember != "d2" {
		t.Fatalf("%d %q (developer %q, game %q, member %q)", rec.Code, rec.Body.String(), gotDev, gotGame, gotMember)
	}
	for _, tc := range []struct {
		err    *httpx.Error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusConflict, "last_owner", "x"), http.StatusConflict, "last_owner"},
		{httpx.NewError(http.StatusNotFound, "member_not_found", "x"), http.StatusNotFound, "member_not_found"},
		{httpx.NewError(http.StatusForbidden, "owner_required", "x"), http.StatusForbidden, "owner_required"},
	} {
		svc.removeGameMember = func(string, string, string) error { return tc.err }
		if rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/games/g1/members/d2", "", "tok"); rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", tc.code, rec.Code, rec.Body.String())
		}
	}
}

func TestLeaveGameHandler(t *testing.T) {
	var gotDev, gotGame string
	svc := developerFake()
	svc.leaveGame = func(dev, game string) error { gotDev, gotGame = dev, game; return nil }
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/leave", "", "tok")
	if rec.Code != http.StatusNoContent || gotDev != "d1" || gotGame != "g1" {
		t.Fatalf("%d %q (developer %q, game %q)", rec.Code, rec.Body.String(), gotDev, gotGame)
	}
	svc.leaveGame = func(string, string) error { return httpx.NewError(http.StatusConflict, "last_owner", "x") }
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/leave", "", "tok"); rec.Code != http.StatusConflict || errorCode(t, rec) != "last_owner" {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSetGameMemberRoleHandler(t *testing.T) {
	var gotMember, gotRole string
	svc := developerFake()
	svc.setMemberRole = func(_, _, member, role string) (service.GameMember, error) {
		gotMember, gotRole = member, role
		return service.GameMember{DeveloperID: member, Name: "Emma", Email: "emma@example.com", Role: role, JoinedAt: invitedAt}, nil
	}
	rec := call(t, svc, discardLog(), http.MethodPut, "/api/v1/games/g1/members/d2/role", `{"role":"owner"}`, "tok")
	want := `{"developerId":"d2","name":"Emma","email":"emma@example.com","role":"owner","joinedAt":"2026-11-17T10:00:00Z"}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want || gotMember != "d2" || gotRole != "owner" {
		t.Fatalf("%d %s (member %q, role %q)", rec.Code, rec.Body.String(), gotMember, gotRole)
	}
	// Unknown fields are refused: nobody picks a game or a developer in the body.
	if rec := call(t, svc, discardLog(), http.MethodPut, "/api/v1/games/g1/members/d2/role", `{"role":"owner","gameId":"g2"}`, "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", rec.Code)
	}
	svc.setMemberRole = func(string, string, string, string) (service.GameMember, error) {
		return service.GameMember{}, httpx.NewError(http.StatusConflict, "last_owner", "x")
	}
	if rec := call(t, svc, discardLog(), http.MethodPut, "/api/v1/games/g1/members/d1/role", `{"role":"member"}`, "tok"); rec.Code != http.StatusConflict || errorCode(t, rec) != "last_owner" {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestTransferGameOwnershipHandler(t *testing.T) {
	var gotTo string
	svc := developerFake()
	svc.transferOwnership = func(_, _, to string) (service.Game, error) {
		gotTo = to
		return service.Game{ID: "g1", Role: "member", Name: "Space", CreatedAt: invitedAt}, nil
	}
	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/transfer-ownership", `{"developerId":"d2"}`, "tok")
	if rec.Code != http.StatusOK || gotTo != "d2" || !strings.Contains(rec.Body.String(), `"role":"member"`) || !strings.Contains(rec.Body.String(), `"id":"g1"`) {
		t.Fatalf("%d %s (to %q)", rec.Code, rec.Body.String(), gotTo)
	}
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/transfer-ownership", `{"to":"d2"}`, "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("a wrong field: %d, want 400", rec.Code)
	}
	svc.transferOwnership = func(string, string, string) (service.Game, error) {
		return service.Game{}, httpx.NewError(http.StatusConflict, "already_owner", "x")
	}
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/g1/transfer-ownership", `{"developerId":"d1"}`, "tok"); rec.Code != http.StatusConflict || errorCode(t, rec) != "already_owner" {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

// A suspended developer can still read every game they belong to (to see why), but cannot change any: the check is on
// the developer, so it does not matter whether they are an owner or a member of it.
func TestASuspendedMemberReadsButCannotWrite(t *testing.T) {
	for _, role := range []string{"owner", "member"} {
		t.Run(role, func(t *testing.T) {
			svc := suspendedDeveloper()
			svc.memberGame = func(_, gameID string) (service.Game, error) { return service.Game{ID: gameID, Role: role}, nil }
			svc.widgetDraft = func(string, string) (service.WidgetDraft, error) {
				return service.WidgetDraft{Config: []byte(`{}`)}, nil
			}
			svc.gameMembers = func(string, string) ([]service.GameMember, error) { return nil, nil }
			for _, path := range []string{"/api/v1/games/g1/widget/draft", "/api/v1/games/g1/members"} {
				if rec := call(t, svc, discardLog(), http.MethodGet, path, "", "dev-token"); rec.Code != http.StatusOK {
					t.Errorf("GET %s: %d %s, want 200", path, rec.Code, rec.Body.String())
				}
			}
			for _, w := range []struct{ method, path, body string }{
				{http.MethodPut, "/api/v1/games/g1/widget/draft", `{"config":{}}`},
				{http.MethodPost, "/api/v1/games/g1/widget/publish", ""},
				{http.MethodPost, "/api/v1/games/g1/leave", ""},
			} {
				if rec := call(t, svc, discardLog(), w.method, w.path, w.body, "dev-token"); rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
					t.Errorf("%s %s: %d %s, want 403 account_suspended", w.method, w.path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func TestGetAdminGame(t *testing.T) {
	joined := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	svc := adminSvc(fakeService{adminGameDetail: func(id string) (service.AdminGameDetail, error) {
		return service.AdminGameDetail{
			AdminGame: service.AdminGame{ID: id, Name: "Space", Status: "suspended", StatusReason: "abuse", CreatedAt: joined},
			Members: []service.AdminGameMember{
				{DeveloperID: "d1", Name: "Ali", Email: "ali@example.com", Role: "owner", Status: "active", JoinedAt: joined},
				{DeveloperID: "d2", Name: "Emma", Email: "emma@example.com", Role: "member", Status: "suspended", JoinedAt: joined.Add(time.Hour)},
			},
		}, nil
	}})
	rec := adminGet(t, svc, "/api/v1/admin/games/g1")
	want := `{"id":"g1","name":"Space","description":"","status":"suspended","statusReason":"abuse","statusChangedAt":null,"createdAt":"2026-11-06T10:00:00Z",` +
		`"members":[{"developerId":"d1","name":"Ali","email":"ali@example.com","role":"owner","status":"active","joinedAt":"2026-11-06T10:00:00Z"},` +
		`{"developerId":"d2","name":"Emma","email":"emma@example.com","role":"member","status":"suspended","joinedAt":"2026-11-06T11:00:00Z"}]}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("%d\n got %s\nwant %s", rec.Code, rec.Body.String(), want)
	}
	svc.adminGameDetail = func(string) (service.AdminGameDetail, error) {
		return service.AdminGameDetail{}, httpx.NewError(http.StatusNotFound, "game_not_found", "x")
	}
	if rec := adminGet(t, svc, "/api/v1/admin/games/g1"); rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	// Only an admin: a developer token is not enough, and neither is no token.
	if rec := call(t, fakeService{}, discardLog(), http.MethodGet, "/api/v1/admin/games/g1", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d, want 401", rec.Code)
	}
}

func TestAdminDeveloperGamesCarryTheRole(t *testing.T) {
	svc := adminSvc(fakeService{adminDeveloper: func(string) (service.AdminDeveloper, error) {
		return service.AdminDeveloper{ID: "d1", Games: []service.AdminGame{{ID: "g1", Role: "member", Status: "active"}}}, nil
	}})
	rec := adminGet(t, svc, "/api/v1/admin/developers/d1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"member"`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	// The response of a game status change is not for one developer: no role.
	svc2 := adminSvc(fakeService{setGameStatus: func(string, string, service.SetStatusInput) (service.AdminGame, error) {
		return service.AdminGame{ID: "g1", Status: "suspended"}, nil
	}})
	rec = adminPut(t, svc2, "/api/v1/admin/games/g1/status", `{"status":"suspended","reason":"abuse"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"role"`) {
		t.Errorf("%d %s, want no role", rec.Code, rec.Body.String())
	}
}

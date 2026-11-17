package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// strangerToEveryGame is a fake service for developer "d1", who belongs to no game: the token is accepted and
// MemberGame answers 404 game_not_found. Any other call a handler makes panics on its nil field, which the
// tests below report as "not behind the membership check".
func strangerToEveryGame() fakeService {
	return fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		memberGame: func(string, string) (service.Game, error) {
			return service.Game{}, httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
		},
	}
}

// TestEveryGameRouteChecksMembership walks Routes(): every developer route of a game (/api/v1/games/{gameID}/...)
// must answer 404 game_not_found to a developer who does not belong to the game, before it reads the body or
// touches anything. A new game route that forgets requireGameMember fails here, and so it cannot open another
// developer's game: this is the test that keeps phase 1 of the teams work (TEAM-305) from leaving a hole.
func TestEveryGameRouteChecksMembership(t *testing.T) {
	h := New("test", strangerToEveryGame(), discardLog())
	checked := 0
	for _, r := range h.Routes() {
		if !strings.Contains(r.Path, "{gameID}") {
			continue
		}
		key := r.Method + " " + r.Path
		// The game ID in these is not a developer asking for their game: the admin panel, the public widget
		// config of a published game, and the internal endpoint of the other services.
		if strings.HasPrefix(r.Path, "/api/v1/admin/") || strings.HasPrefix(r.Path, "/api/v1/public/") || strings.HasPrefix(r.Path, "/internal/") {
			continue
		}
		if !strings.HasPrefix(r.Path, "/api/v1/games/{gameID}") {
			t.Errorf("%s: a route with a game ID that is not under /api/v1/games/{gameID}: decide how it checks membership and teach this test", key)
			continue
		}
		checked++
		t.Run(key, func(t *testing.T) {
			path := strings.NewReplacer("{gameID}", "g1", "{version}", "1").Replace(r.Path)
			// An invalid body: the 404 must come before the body is read.
			req := httptest.NewRequest(r.Method, path, strings.NewReader("not json"))
			req.SetPathValue("gameID", "g1")
			req.Header.Set("Authorization", "Bearer dev-token")
			rec := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("not behind the membership check: the handler ran and panicked on %v", p)
					}
				}()
				r.Handle(rec, req)
			}()
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
				t.Fatalf("%d %s, want 404 game_not_found", rec.Code, rec.Body.String())
			}
		})
	}
	if checked < 9 {
		t.Fatalf("only %d game routes checked; keys (2), origins, draft (2), publish, versions (2) and rollback make 9", checked)
	}
}

// A developer who is not logged in is 401, not a 404 that would say nothing: the login comes first.
func TestGameRoutesNeedALoginBeforeMembership(t *testing.T) {
	asked := false
	svc := strangerToEveryGame()
	svc.auth = func(string) (string, error) {
		return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
	}
	svc.memberGame = func(string, string) (service.Game, error) { asked = true; return service.Game{}, nil }
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/widget/draft", "", "bad")
	if rec.Code != http.StatusUnauthorized || asked {
		t.Fatalf("%d %s (membership asked: %v)", rec.Code, rec.Body.String(), asked)
	}
}

// requireGameMember asks with the developer of the token and the game of the URL, hands the handler the game with
// the developer's role, and takes both from nowhere else (not from the query or the body).
func TestRequireGameMemberPassesTheGameAndRoleOn(t *testing.T) {
	var gotDeveloper, gotGame string
	h := New("test", fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		memberGame: func(developerID, gameID string) (service.Game, error) {
			gotDeveloper, gotGame = developerID, gameID
			return service.Game{ID: gameID, Name: "Farm", Role: "member"}, nil
		},
	}, discardLog())

	var seen service.Game
	var ok bool
	next := func(w http.ResponseWriter, r *http.Request) {
		seen, ok = gameFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}
	req := httptest.NewRequest(http.MethodGet, "/x?gameID=other&developer_id=d2", strings.NewReader(`{"gameID":"other"}`))
	req.SetPathValue("gameID", "g1")
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "d1"))
	rec := httptest.NewRecorder()
	h.requireGameMember(next)(rec, req)

	if rec.Code != http.StatusNoContent || !ok || seen.ID != "g1" || seen.Role != "member" {
		t.Fatalf("%d, game %+v (found %v); want the game with the member role", rec.Code, seen, ok)
	}
	if gotDeveloper != "d1" || gotGame != "g1" {
		t.Fatalf("membership asked for developer %q and game %q, want d1 and g1 (the token and the URL)", gotDeveloper, gotGame)
	}
	if _, found := gameFrom(context.Background()); found {
		t.Error("a request that did not go through requireGameMember has a game")
	}
}

func TestRequireGameMemberDoesNotLeakAnInternalError(t *testing.T) {
	svc := strangerToEveryGame()
	svc.memberGame = func(string, string) (service.Game, error) {
		return service.Game{}, errTest("pq: connection to 10.0.0.5 refused")
	}
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/widget/versions", "", "tok")
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

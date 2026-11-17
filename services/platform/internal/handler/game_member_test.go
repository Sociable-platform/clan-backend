package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// gameRouteRole says who may use each developer route of a game: an "owner" only, or any "member" of the game (an
// owner too). A new game route has to be added here, which is the moment to decide who may use it.
var gameRouteRole = map[string]string{
	"GET /api/v1/games/{gameID}/members":                             "member",
	"POST /api/v1/games/{gameID}/api-keys":                           "owner",
	"GET /api/v1/games/{gameID}/api-keys":                            "owner",
	"PUT /api/v1/games/{gameID}/allowed-origins":                     "owner",
	"GET /api/v1/games/{gameID}/widget/draft":                        "member",
	"PUT /api/v1/games/{gameID}/widget/draft":                        "member",
	"POST /api/v1/games/{gameID}/widget/publish":                     "member",
	"GET /api/v1/games/{gameID}/widget/versions":                     "member",
	"GET /api/v1/games/{gameID}/widget/versions/{version}":           "member",
	"POST /api/v1/games/{gameID}/widget/versions/{version}/rollback": "member",
}

// gameRoutes are the developer routes of a game, by "METHOD path".
func gameRoutes(h *Handler) map[string]Route {
	out := map[string]Route{}
	for _, r := range h.Routes() {
		if strings.HasPrefix(r.Path, "/api/v1/games/{gameID}") {
			out[r.Method+" "+r.Path] = r
		}
	}
	return out
}

// Every game route is listed with who may use it, and the list has no route that is gone: a new route cannot slip
// in without somebody deciding whether a member may use it.
func TestEveryGameRouteSaysWhoMayUseIt(t *testing.T) {
	routes := gameRoutes(New("test", fakeService{}, discardLog()))
	for key := range routes {
		if gameRouteRole[key] == "" {
			t.Errorf("%s is a game route that is not in gameRouteRole: decide whether a member may use it", key)
		}
	}
	for key := range gameRouteRole {
		if _, ok := routes[key]; !ok {
			t.Errorf("%s is in gameRouteRole but is not a route", key)
		}
	}
}

// reaches serves the route as a developer with the given role on the game and says whether the request got past the gates
// to the handler. The handler of a fake with no functions panics on its nil field, or answers a bad body with a 400; either
// way it ran. A gate that refused answers 403 owner_required, 404 game_not_found or 401.
func reaches(t *testing.T, r Route, role string) (reached bool, rec *httptest.ResponseRecorder) {
	t.Helper()
	svc := fakeService{
		auth:       func(string) (string, error) { return "d1", nil },
		memberGame: func(_, gameID string) (service.Game, error) { return service.Game{ID: gameID, Role: role}, nil },
	}
	h := New("test", svc, discardLog())
	var route Route
	for _, candidate := range h.Routes() {
		if candidate.Method == r.Method && candidate.Path == r.Path {
			route = candidate
		}
	}
	path := strings.NewReplacer("{gameID}", "g1", "{version}", "1").Replace(r.Path)
	req := httptest.NewRequest(route.Method, path, strings.NewReader("{}"))
	req.SetPathValue("gameID", "g1")
	req.SetPathValue("version", "1")
	req.Header.Set("Authorization", "Bearer dev-token")
	rec = httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		route.Handle(rec, req)
	}()
	if reached {
		return true, rec
	}
	// Nothing panicked: the handler answered (a bad body is a 400 before the service is called), or a gate refused.
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	refusedByAGate := body.Error.Code == "owner_required" || body.Error.Code == "game_not_found" || body.Error.Code == "unauthorized"
	return !refusedByAGate, rec
}

// An owner reaches every game route; a member reaches the member routes, and is refused the owner routes with a 403
// owner_required before the handler runs (not a 404: they know the game).
func TestOwnerAndMemberRoutes(t *testing.T) {
	for key, r := range gameRoutes(New("test", fakeService{}, discardLog())) {
		who := gameRouteRole[key]
		t.Run(key, func(t *testing.T) {
			if reached, rec := reaches(t, r, "owner"); !reached {
				t.Errorf("an owner did not reach %s: %d %s", key, rec.Code, rec.Body.String())
			}
			reached, rec := reaches(t, r, "member")
			switch who {
			case "owner":
				if reached || rec.Code != http.StatusForbidden || errorCode(t, rec) != "owner_required" {
					t.Errorf("a member on an owner route: reached=%v, %d %s; want 403 owner_required before the handler", reached, rec.Code, rec.Body.String())
				}
			default:
				if !reached {
					t.Errorf("a member did not reach %s: %d %s", key, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// The role comes from the membership of this request, never from the token or the client: a role that says anything
// else than owner is not an owner, an empty one included.
func TestOnlyTheOwnerRoleOpensAnOwnerRoute(t *testing.T) {
	r := gameRoutes(New("test", fakeService{}, discardLog()))["POST /api/v1/games/{gameID}/api-keys"]
	for _, role := range []string{"member", "", "Owner", "admin", "owner "} {
		if reached, rec := reaches(t, r, role); reached || rec.Code != http.StatusForbidden {
			t.Errorf("role %q: reached=%v, %d; want 403 and the handler not run", role, reached, rec.Code)
		}
	}
}

func TestRevokingAKeyAsAMemberIs403(t *testing.T) {
	svc := fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		revokeAPIKey: func(string, string) error {
			return httpx.NewError(http.StatusForbidden, "owner_required", "Only the game's owner can do this")
		},
	}
	rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/api-keys/3b1c6f0e-9d2a-4a53-8f0b-7a9d2f6c1e44", "", "tok")
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "owner_required" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestListGameMembersAnswersWithTheTeam(t *testing.T) {
	joined := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	var gotDeveloper, gotGame string
	svc := fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		gameMembers: func(developerID, gameID string) ([]service.GameMember, error) {
			gotDeveloper, gotGame = developerID, gameID
			return []service.GameMember{
				{DeveloperID: "d1", Name: "Ali", Email: "ali@example.com", Role: "owner", JoinedAt: joined},
				{DeveloperID: "d2", Name: "Emma", Email: "emma@example.com", Role: "member", JoinedAt: joined.Add(time.Hour)},
			}, nil
		},
	}
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/members?developerId=d9", "", "tok")
	if rec.Code != http.StatusOK || gotDeveloper != "d1" || gotGame != "g1" {
		t.Fatalf("%d %s (developer %q, game %q)", rec.Code, rec.Body.String(), gotDeveloper, gotGame)
	}
	want := `{"members":[{"developerId":"d1","name":"Ali","email":"ali@example.com","role":"owner","joinedAt":"2026-11-07T09:30:00Z"},` +
		`{"developerId":"d2","name":"Emma","email":"emma@example.com","role":"member","joinedAt":"2026-11-07T10:30:00Z"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body\n got %s\nwant %s", rec.Body.String(), want)
	}

	// Nobody on the team yet reads as an empty list, never null.
	svc.gameMembers = func(string, string) ([]service.GameMember, error) { return nil, nil }
	rec = call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/g1/members", "", "tok")
	if rec.Body.String() != `{"members":[]}`+"\n" {
		t.Errorf("empty body = %s", rec.Body.String())
	}
}

func TestGamesCarryTheRoleOfTheDeveloperAsking(t *testing.T) {
	svc := fakeService{
		auth: func(string) (string, error) { return "d1", nil },
		listGames: func(string) ([]service.Game, error) {
			return []service.Game{{ID: "g1", Role: "owner", Name: "A"}, {ID: "g2", Role: "member", Name: "B"}}, nil
		},
	}
	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", "tok")
	var out struct {
		Games []struct{ ID, Role string } `json:"games"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Games) != 2 || out.Games[0].Role != "owner" || out.Games[1].Role != "member" {
		t.Fatalf("%d %s (%v)", rec.Code, rec.Body.String(), err)
	}
}

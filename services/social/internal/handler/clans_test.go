package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// The other fakes must satisfy the Service interface too.
func (f fakeService) ListClans(context.Context, string, string) ([]service.Clan, error) {
	return []service.Clan{}, f.err
}

func (s *recordingService) ListClans(context.Context, string, string) ([]service.Clan, error) {
	return []service.Clan{}, s.err
}

func (f fakeService) CreateClan(context.Context, string, string, string, string) (service.Clan, error) {
	return service.Clan{}, f.err
}

func (s *recordingService) CreateClan(context.Context, string, string, string, string) (service.Clan, error) {
	return service.Clan{}, s.err
}

func (f fakeService) JoinClan(context.Context, string, string, string, string) (service.Clan, error) {
	return service.Clan{}, f.err
}

func (s *recordingService) JoinClan(context.Context, string, string, string, string) (service.Clan, error) {
	return service.Clan{}, s.err
}

func (f fakeService) LeaveClan(context.Context, string, string, string) error { return f.err }

func (s *recordingService) LeaveClan(context.Context, string, string, string) error { return s.err }

func (f fakeService) MyClan(context.Context, string, string) (service.PlayerClan, error) {
	return service.PlayerClan{}, f.err
}

func (s *recordingService) MyClan(context.Context, string, string) (service.PlayerClan, error) {
	return service.PlayerClan{}, s.err
}

func (f fakeService) ListMembers(context.Context, string, string) ([]service.Member, error) {
	return []service.Member{}, f.err
}

func (s *recordingService) ListMembers(context.Context, string, string) ([]service.Member, error) {
	return []service.Member{}, s.err
}

// clanService remembers which game and search text the handler asked for.
type clanService struct {
	fakeService
	clans     []service.Clan
	err       error
	gameID, q string
}

func (s *clanService) ListClans(_ context.Context, gameID, q string) ([]service.Clan, error) {
	s.gameID, s.q = gameID, q
	return s.clans, s.err
}

const tokenGame = "a6c1e2f0-1111-4222-8333-944455556666"

// callClans sends GET /api/v1/clans with a real player token for tokenGame.
func callClans(t *testing.T, svc Service, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: tokenGame, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/clans"+rawQuery, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestListClansUsesGameFromTokenAndQFromURL(t *testing.T) {
	svc := &clanService{clans: []service.Clan{{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10}}}
	// a game_id in the URL must be ignored: only the token decides the game
	rec := callClans(t, svc, "?q="+url.QueryEscape("Wol f")+"&game_id=99999999-9999-4999-8999-999999999999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.q != "Wol f" {
		t.Fatalf("service got game=%q q=%q", svc.gameID, svc.q)
	}
	want := `{"clans":[{"id":"c-1","name":"Wolves","memberCount":3,"maxMembers":10}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestListClansEmptyIsEmptyArray(t *testing.T) {
	rec := callClans(t, &clanService{clans: []service.Clan{}}, "")
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"clans":[]}` {
		t.Fatalf("status %d, body %s (want {\"clans\":[]}, not null)", rec.Code, got)
	}
}

func TestListClansBadQuery(t *testing.T) {
	svc := &clanService{err: httpx.NewError(http.StatusBadRequest, "invalid_query", "Search text must be at most 50 characters")}
	rec := callClans(t, svc, "?q=x")
	var body httpx.ErrorBody
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != "invalid_query" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestListClansNeedsAToken(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", &clanService{}, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clans", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

// creatingService remembers what createClan passed on and answers with err or a new clan.
type creatingService struct {
	fakeService
	err                                error
	gameID, externalID, playerName, nm string
}

func (s *creatingService) CreateClan(_ context.Context, gameID, externalID, playerName, name string) (service.Clan, error) {
	s.gameID, s.externalID, s.playerName, s.nm = gameID, externalID, playerName, name
	if s.err != nil {
		return service.Clan{}, s.err
	}
	return service.Clan{ID: "c-9", Name: name, MemberCount: 1, MaxMembers: 10}, nil
}

// postClan sends POST /api/v1/clans with body, using a real player token for tokenGame
// unless noToken is set.
func postClan(t *testing.T, svc Service, body string, noToken bool) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/clans", strings.NewReader(body))
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

func TestCreateClanReturns201WithTheClan(t *testing.T) {
	svc := &creatingService{}
	rec := postClan(t, svc, `{"name":"Wolves"}`, false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.playerName != "Ali" || svc.nm != "Wolves" {
		t.Fatalf("service got game=%q player=%q name=%q clan=%q; want the token's game and player, and the body's name",
			svc.gameID, svc.externalID, svc.playerName, svc.nm)
	}
	want := `{"id":"c-9","name":"Wolves","memberCount":1,"maxMembers":10}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestCreateClanBadBody(t *testing.T) {
	for _, body := range []string{
		``,                                   // no body
		`{"name":`,                           // broken JSON
		`{"name":"Wolves","gameId":"other"}`, // unknown field: the game can never come from the body
		`{"name":"Wolves"}{"name":"Bears"}`,  // two objects
	} {
		svc := &creatingService{}
		rec := postClan(t, svc, body, false)
		var e httpx.ErrorBody
		if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != "invalid_json" {
			t.Errorf("body %q: status %d, response %s; want 400 invalid_json", body, rec.Code, rec.Body.String())
		}
		if svc.nm != "" {
			t.Errorf("body %q: the service must not be called", body)
		}
	}
}

func TestCreateClanPassesServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusBadRequest, "invalid_clan_name", "x"), http.StatusBadRequest, "invalid_clan_name"},
		{httpx.NewError(http.StatusConflict, "clan_name_taken", "x"), http.StatusConflict, "clan_name_taken"},
		{httpx.NewError(http.StatusConflict, "already_in_clan", "x"), http.StatusConflict, "already_in_clan"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := postClan(t, &creatingService{err: tc.err}, `{"name":"Wolves"}`, false)
		var e httpx.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: status %d, body %s; want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestCreateClanNeedsAToken(t *testing.T) {
	if rec := postClan(t, &creatingService{}, `{"name":"Wolves"}`, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

// joiningService remembers what joinClan passed on and answers with err or the joined clan.
type joiningService struct {
	fakeService
	err                                    error
	called                                 bool
	gameID, externalID, playerName, clanID string
}

func (s *joiningService) JoinClan(_ context.Context, gameID, externalID, playerName, clanID string) (service.Clan, error) {
	s.called = true
	s.gameID, s.externalID, s.playerName, s.clanID = gameID, externalID, playerName, clanID
	if s.err != nil {
		return service.Clan{}, s.err
	}
	return service.Clan{ID: clanID, Name: "Wolves", MemberCount: 2, MaxMembers: 10}, nil
}

// postJoin sends POST path with a real player token for tokenGame, unless noToken is set.
func postJoin(t *testing.T, svc Service, method, path string, noToken bool) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(method, path, nil)
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

const joinClanID = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"

func TestJoinClanReturns200WithTheClan(t *testing.T) {
	svc := &joiningService{}
	rec := postJoin(t, svc, http.MethodPost, "/api/v1/clans/"+joinClanID+"/join", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.playerName != "Ali" || svc.clanID != joinClanID {
		t.Fatalf("service got game=%q player=%q name=%q clan=%q; want the token's game and player, and the URL's clan",
			svc.gameID, svc.externalID, svc.playerName, svc.clanID)
	}
	want := `{"id":"` + joinClanID + `","name":"Wolves","memberCount":2,"maxMembers":10}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestJoinClanPassesServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusConflict, "already_in_clan", "x"), http.StatusConflict, "already_in_clan"},
		{httpx.NewError(http.StatusConflict, "clan_full", "x"), http.StatusConflict, "clan_full"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := postJoin(t, &joiningService{err: tc.err}, http.MethodPost, "/api/v1/clans/"+joinClanID+"/join", false)
		var e httpx.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: status %d, body %s; want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestJoinClanRouting(t *testing.T) {
	// no token
	if rec := postJoin(t, &joiningService{}, http.MethodPost, "/api/v1/clans/"+joinClanID+"/join", true); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", rec.Code)
	}
	// only POST joins; GET on the same path is not a route
	svc := &joiningService{}
	if rec := postJoin(t, svc, http.MethodGet, "/api/v1/clans/"+joinClanID+"/join", false); rec.Code == http.StatusOK || svc.called {
		t.Errorf("GET join: status %d, service called=%v; want it refused", rec.Code, svc.called)
	}
	// the clan ID is one path segment; extra segments do not reach the handler
	svc = &joiningService{}
	if rec := postJoin(t, svc, http.MethodPost, "/api/v1/clans/a/b/join", false); svc.called {
		t.Errorf("extra path segment: status %d, but the service was called", rec.Code)
	}
}

// leavingService remembers what leaveClan passed on and answers with err.
type leavingService struct {
	fakeService
	err                        error
	called                     bool
	gameID, externalID, clanID string
}

func (s *leavingService) LeaveClan(_ context.Context, gameID, externalID, clanID string) error {
	s.called = true
	s.gameID, s.externalID, s.clanID = gameID, externalID, clanID
	return s.err
}

func TestLeaveClanReturns204WithNoBody(t *testing.T) {
	svc := &leavingService{}
	rec := postJoin(t, svc, http.MethodPost, "/api/v1/clans/"+joinClanID+"/leave", false)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d, body %q; want 204 and nothing", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID {
		t.Fatalf("service got game=%q player=%q clan=%q; want the token's game and player, and the URL's clan",
			svc.gameID, svc.externalID, svc.clanID)
	}
}

func TestLeaveClanPassesServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusConflict, "not_in_clan", "x"), http.StatusConflict, "not_in_clan"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := postJoin(t, &leavingService{err: tc.err}, http.MethodPost, "/api/v1/clans/"+joinClanID+"/leave", false)
		var e httpx.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: status %d, body %s; want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestLeaveClanRouting(t *testing.T) {
	// no token
	svc := &leavingService{}
	if rec := postJoin(t, svc, http.MethodPost, "/api/v1/clans/"+joinClanID+"/leave", true); rec.Code != http.StatusUnauthorized || svc.called {
		t.Errorf("no token: status %d, service called=%v; want 401 and no call", rec.Code, svc.called)
	}
	// only POST leaves; GET and DELETE on the same path are not routes
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		svc = &leavingService{}
		if rec := postJoin(t, svc, method, "/api/v1/clans/"+joinClanID+"/leave", false); rec.Code == http.StatusNoContent || svc.called {
			t.Errorf("%s leave: status %d, service called=%v; want it refused", method, rec.Code, svc.called)
		}
	}
	// the clan ID is one path segment; extra segments do not reach the handler
	svc = &leavingService{}
	if rec := postJoin(t, svc, http.MethodPost, "/api/v1/clans/a/b/leave", false); svc.called {
		t.Errorf("extra path segment: status %d, but the service was called", rec.Code)
	}
}

// readingService answers MyClan and ListMembers and remembers what it was asked.
type readingService struct {
	fakeService
	err                        error
	gameID, externalID, clanID string
}

func (s *readingService) MyClan(_ context.Context, gameID, externalID string) (service.PlayerClan, error) {
	s.gameID, s.externalID = gameID, externalID
	if s.err != nil {
		return service.PlayerClan{}, s.err
	}
	return service.PlayerClan{Clan: service.Clan{ID: joinClanID, Name: "Wolves", MemberCount: 2, MaxMembers: 10}, Role: "owner"}, nil
}

func (s *readingService) ListMembers(_ context.Context, gameID, clanID string) ([]service.Member, error) {
	s.gameID, s.clanID = gameID, clanID
	if s.err != nil {
		return nil, s.err
	}
	at := time.Date(2026, 11, 7, 21, 30, 0, 0, time.FixedZone("Baku", 4*60*60)) // answered in UTC
	return []service.Member{
		{PlayerID: "p-1", DisplayName: "Ali", Role: "owner", JoinedAt: at},
		{PlayerID: "p-2", DisplayName: "Leyla", Role: "member", JoinedAt: at.Add(time.Minute)},
	}, nil
}

func TestGetMyClan(t *testing.T) {
	svc := &readingService{}
	rec := postJoin(t, svc, http.MethodGet, "/api/v1/players/me/clan", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.externalID != "u_42" {
		t.Fatalf("service got game=%q player=%q, want the token's", svc.gameID, svc.externalID)
	}
	want := `{"clan":{"id":"` + joinClanID + `","name":"Wolves","memberCount":2,"maxMembers":10},"role":"owner"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}

	rec = postJoin(t, &readingService{err: httpx.NewError(http.StatusNotFound, "not_in_clan", "x")}, http.MethodGet, "/api/v1/players/me/clan", false)
	var e httpx.ErrorBody
	if rec.Code != http.StatusNotFound || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != "not_in_clan" {
		t.Fatalf("not in clan: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestGetMeStillWorksNextToMyClan(t *testing.T) {
	// /players/me and /players/me/clan are different routes
	svc := &recordingService{}
	if rec := postJoin(t, svc, http.MethodGet, "/api/v1/players/me", false); rec.Code != http.StatusOK || svc.externalID != "u_42" {
		t.Fatalf("GET /players/me: status %d", rec.Code)
	}
}

func TestListMembersHandler(t *testing.T) {
	svc := &readingService{}
	rec := postJoin(t, svc, http.MethodGet, "/api/v1/clans/"+joinClanID+"/members", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.clanID != joinClanID {
		t.Fatalf("service got game=%q clan=%q", svc.gameID, svc.clanID)
	}
	want := `{"members":[` +
		`{"playerId":"p-1","displayName":"Ali","role":"owner","joinedAt":"2026-11-07T17:30:00Z"},` +
		`{"playerId":"p-2","displayName":"Leyla","role":"member","joinedAt":"2026-11-07T17:31:00Z"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}

	rec = postJoin(t, &readingService{err: httpx.NewError(http.StatusNotFound, "clan_not_found", "x")}, http.MethodGet, "/api/v1/clans/"+joinClanID+"/members", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found: status %d", rec.Code)
	}
	if rec := postJoin(t, &readingService{}, http.MethodGet, "/api/v1/clans/"+joinClanID+"/members", true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}
}

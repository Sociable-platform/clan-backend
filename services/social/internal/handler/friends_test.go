package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

// The other fakes must satisfy the Service interface too.
func (f fakeService) SendFriendRequest(context.Context, string, string, string, string) (service.SendFriendRequestResult, error) {
	return service.SendFriendRequestResult{}, f.err
}

func (s *recordingService) SendFriendRequest(context.Context, string, string, string, string) (service.SendFriendRequestResult, error) {
	return service.SendFriendRequestResult{}, s.err
}

func (f fakeService) AcceptFriendRequest(context.Context, string, string, string, string) (service.Friend, error) {
	return service.Friend{}, f.err
}

func (s *recordingService) AcceptFriendRequest(context.Context, string, string, string, string) (service.Friend, error) {
	return service.Friend{}, s.err
}

func (f fakeService) ListFriendRequests(context.Context, string, string, string) (service.FriendRequests, error) {
	return service.FriendRequests{}, f.err
}

func (s *recordingService) ListFriendRequests(context.Context, string, string, string) (service.FriendRequests, error) {
	return service.FriendRequests{}, s.err
}

func (f fakeService) ListFriends(context.Context, string, string, string) ([]service.Friend, error) {
	return []service.Friend{}, f.err
}

func (s *recordingService) ListFriends(context.Context, string, string, string) ([]service.Friend, error) {
	return []service.Friend{}, s.err
}

func (f fakeService) DeclineFriendRequest(context.Context, string, string, string, string) error {
	return f.err
}

func (s *recordingService) DeclineFriendRequest(context.Context, string, string, string, string) error {
	return s.err
}

func (f fakeService) CancelFriendRequest(context.Context, string, string, string, string) error {
	return f.err
}

func (s *recordingService) CancelFriendRequest(context.Context, string, string, string, string) error {
	return s.err
}

func (f fakeService) RemoveFriend(context.Context, string, string, string, string) error {
	return f.err
}

func (s *recordingService) RemoveFriend(context.Context, string, string, string, string) error {
	return s.err
}

// friendService remembers what the handler passed on and answers with what the test gave it.
type friendService struct {
	fakeService
	gameID, externalID, name, arg string
	called                        string // which of decline, cancel, remove was called

	send     service.SendFriendRequestResult
	friend   service.Friend
	requests service.FriendRequests
	friends  []service.Friend
	err      error
}

func (s *friendService) SendFriendRequest(_ context.Context, gameID, externalID, name, playerID string) (service.SendFriendRequestResult, error) {
	s.gameID, s.externalID, s.name, s.arg = gameID, externalID, name, playerID
	return s.send, s.err
}

func (s *friendService) AcceptFriendRequest(_ context.Context, gameID, externalID, name, requestID string) (service.Friend, error) {
	s.gameID, s.externalID, s.name, s.arg = gameID, externalID, name, requestID
	return s.friend, s.err
}

func (s *friendService) ListFriendRequests(_ context.Context, gameID, externalID, name string) (service.FriendRequests, error) {
	s.gameID, s.externalID, s.name = gameID, externalID, name
	return s.requests, s.err
}

func (s *friendService) ListFriends(_ context.Context, gameID, externalID, name string) ([]service.Friend, error) {
	s.gameID, s.externalID, s.name = gameID, externalID, name
	return s.friends, s.err
}

func (s *friendService) DeclineFriendRequest(_ context.Context, gameID, externalID, name, requestID string) error {
	s.gameID, s.externalID, s.name, s.arg, s.called = gameID, externalID, name, requestID, "decline"
	return s.err
}

func (s *friendService) CancelFriendRequest(_ context.Context, gameID, externalID, name, requestID string) error {
	s.gameID, s.externalID, s.name, s.arg, s.called = gameID, externalID, name, requestID, "cancel"
	return s.err
}

func (s *friendService) RemoveFriend(_ context.Context, gameID, externalID, name, playerID string) error {
	s.gameID, s.externalID, s.name, s.arg, s.called = gameID, externalID, name, playerID, "remove"
	return s.err
}

// callFriends sends method path (with body, if any) through the real router and token guard,
// with a player token for tokenGame unless noToken is set.
func callFriends(t *testing.T, svc Service, method, path, body string, noToken bool) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
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

var friendTime = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

const friendID = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"

func (s *friendService) checkCaller(t *testing.T) {
	t.Helper()
	if s.gameID != tokenGame || s.externalID != "u_42" || s.name != "Ali" {
		t.Errorf("service got game=%q player=%q name=%q, want the token's", s.gameID, s.externalID, s.name)
	}
}

func checkBody(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != status || got != want {
		t.Fatalf("status %d, body\n got %s\nwant %d %s", rec.Code, got, status, want)
	}
}

func TestSendFriendRequestCreated(t *testing.T) {
	svc := &friendService{send: service.SendFriendRequestResult{Request: &service.FriendRequest{
		ID: "req-1", Player: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, CreatedAt: friendTime,
	}}}
	rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests", `{"playerId":"`+friendID+`"}`, false)
	checkBody(t, rec, http.StatusCreated,
		`{"id":"req-1","player":{"id":"`+friendID+`","displayName":"Aysel"},"createdAt":"2026-11-10T08:00:00Z"}`)
	svc.checkCaller(t)
	if svc.arg != friendID {
		t.Errorf("service got playerId %q", svc.arg)
	}
}

func TestSendFriendRequestBackIsOKWithTheFriend(t *testing.T) {
	svc := &friendService{send: service.SendFriendRequestResult{Friend: &service.Friend{
		FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, Since: friendTime,
	}}}
	rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests", `{"playerId":"`+friendID+`"}`, false)
	checkBody(t, rec, http.StatusOK, `{"id":"`+friendID+`","displayName":"Aysel","since":"2026-11-10T08:00:00Z"}`)
}

func TestSendFriendRequestBadBodies(t *testing.T) {
	for _, body := range []string{`{`, `{"playerId":"x","gameId":"other"}`} {
		svc := &friendService{}
		rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests", body, false)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_json") {
			t.Errorf("body %s: status %d, body %s; want 400 invalid_json", body, rec.Code, rec.Body.String())
		}
		if svc.gameID != "" {
			t.Errorf("body %s reached the service", body)
		}
	}
}

func TestSendFriendRequestServiceErrorIsPassedOn(t *testing.T) {
	svc := &friendService{err: httpx.NewError(http.StatusConflict, "already_friends", "You are already friends")}
	rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests", `{"playerId":"`+friendID+`"}`, false)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"already_friends"`) {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestListFriendRequestsHandler(t *testing.T) {
	svc := &friendService{requests: service.FriendRequests{
		Incoming: []service.FriendRequest{{ID: "in-1", Player: service.FriendPlayer{ID: "p-2", DisplayName: "Murad"}, CreatedAt: friendTime}},
		Outgoing: []service.FriendRequest{},
	}}
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/friends/requests", "", false)
	checkBody(t, rec, http.StatusOK,
		`{"incoming":[{"id":"in-1","player":{"id":"p-2","displayName":"Murad"},"createdAt":"2026-11-10T08:00:00Z"}],"outgoing":[]}`)
	svc.checkCaller(t)
}

func TestAcceptFriendRequestHandler(t *testing.T) {
	svc := &friendService{friend: service.Friend{
		FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, Since: friendTime,
	}}
	rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests/req-1/accept", "", false)
	checkBody(t, rec, http.StatusOK, `{"id":"`+friendID+`","displayName":"Aysel","since":"2026-11-10T08:00:00Z"}`)
	svc.checkCaller(t)
	if svc.arg != "req-1" {
		t.Errorf("service got request %q, want the one in the URL", svc.arg)
	}
}

func TestListFriendsHandler(t *testing.T) {
	const clanID = "c1a2b3c4-0000-4000-8000-000000000001"
	svc := &friendService{friends: []service.Friend{
		{FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, Since: friendTime,
			Clan: &service.FriendClan{ID: clanID, Name: "Wolves"}},
		{FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Nobody"}, Since: friendTime},
	}}
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/friends", "", false)
	checkBody(t, rec, http.StatusOK, `{"friends":[`+
		`{"id":"`+friendID+`","displayName":"Aysel","since":"2026-11-10T08:00:00Z","clan":{"id":"`+clanID+`","name":"Wolves"}},`+
		`{"id":"`+friendID+`","displayName":"Nobody","since":"2026-11-10T08:00:00Z","clan":null}]}`)
	svc.checkCaller(t)
}

// The friend returned by accept (and by a request sent back) carries no clan: the field
// belongs to the list only.
func TestAcceptedFriendHasNoClanField(t *testing.T) {
	svc := &friendService{friend: service.Friend{FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, Since: friendTime,
		Clan: &service.FriendClan{ID: "c1", Name: "Wolves"}}}
	rec := callFriends(t, svc, http.MethodPost, "/api/v1/friends/requests/req-1/accept", "", false)
	checkBody(t, rec, http.StatusOK, `{"id":"`+friendID+`","displayName":"Aysel","since":"2026-11-10T08:00:00Z"}`)
}

func TestFriendListsEmptyAreEmptyArrays(t *testing.T) {
	svc := &friendService{friends: []service.Friend{}}
	checkBody(t, callFriends(t, svc, http.MethodGet, "/api/v1/friends", "", false), http.StatusOK, `{"friends":[]}`)
	checkBody(t, callFriends(t, svc, http.MethodGet, "/api/v1/friends/requests", "", false), http.StatusOK,
		`{"incoming":[],"outgoing":[]}`)
}

func TestFriendRoutesNeedAToken(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/friends/requests", `{"playerId":"` + friendID + `"}`},
		{http.MethodGet, "/api/v1/friends/requests", ""},
		{http.MethodPost, "/api/v1/friends/requests/req-1/accept", ""},
		{http.MethodGet, "/api/v1/friends", ""},
	}
	for _, rt := range routes {
		svc := &friendService{}
		rec := callFriends(t, svc, rt.method, rt.path, rt.body, true)
		if rec.Code != http.StatusUnauthorized || svc.gameID != "" {
			t.Errorf("%s %s without a token: status %d, service reached = %v", rt.method, rt.path, rec.Code, svc.gameID != "")
		}
	}
}

func TestDeclineCancelAndRemoveAnswerNoContent(t *testing.T) {
	cases := []struct {
		method, path, call, arg string
	}{
		{http.MethodPost, "/api/v1/friends/requests/req-1/decline", "decline", "req-1"},
		{http.MethodDelete, "/api/v1/friends/requests/req-1", "cancel", "req-1"},
		{http.MethodDelete, "/api/v1/friends/" + friendID, "remove", friendID},
	}
	for _, c := range cases {
		t.Run(c.call, func(t *testing.T) {
			svc := &friendService{}
			rec := callFriends(t, svc, c.method, c.path, "", false)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status %d, body %q; want 204 and no body", rec.Code, rec.Body.String())
			}
			svc.checkCaller(t)
			if svc.called != c.call || svc.arg != c.arg {
				t.Errorf("service got %s(%q), want %s(%q)", svc.called, svc.arg, c.call, c.arg)
			}
		})
	}
}

func TestDeclineCancelAndRemovePassErrorsOn(t *testing.T) {
	cases := []struct {
		method, path string
		err          error
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/friends/requests/req-1/decline",
			httpx.NewError(http.StatusForbidden, "not_request_receiver", "x"), http.StatusForbidden, "not_request_receiver"},
		{http.MethodDelete, "/api/v1/friends/requests/req-1",
			httpx.NewError(http.StatusForbidden, "not_request_sender", "x"), http.StatusForbidden, "not_request_sender"},
		{http.MethodDelete, "/api/v1/friends/" + friendID,
			httpx.NewError(http.StatusNotFound, "not_friends", "x"), http.StatusNotFound, "not_friends"},
	}
	for _, c := range cases {
		rec := callFriends(t, &friendService{err: c.err}, c.method, c.path, "", false)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s %s: status %d, body %s; want %d %s", c.method, c.path, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

func TestDeclineCancelAndRemoveNeedAToken(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/friends/requests/req-1/decline"},
		{http.MethodDelete, "/api/v1/friends/requests/req-1"},
		{http.MethodDelete, "/api/v1/friends/" + friendID},
	}
	for _, c := range cases {
		svc := &friendService{}
		rec := callFriends(t, svc, c.method, c.path, "", true)
		if rec.Code != http.StatusUnauthorized || svc.called != "" {
			t.Errorf("%s %s without a token: status %d, service called %q", c.method, c.path, rec.Code, svc.called)
		}
	}
}

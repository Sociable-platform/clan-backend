package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

const requestID = "8c1f4d2a-5b6e-4f70-9a81-b2c3d4e5f607"

var (
	requestsPath = "/api/v1/clans/" + joinClanID + "/requests"
	acceptPath   = requestsPath + "/" + requestID + "/accept"
	declinePath  = requestsPath + "/" + requestID + "/decline"
	myRequest    = requestsPath + "/me"
)

// requestingService remembers which call reached it, with what, and answers with err or the requests.
type requestingService struct {
	fakeService
	err                                                error
	call                                               string
	gameID, externalID, name, clanID, reqID, policyArg string
	rows                                               []service.JoinRequest
}

func (s *requestingService) note(call, gameID, externalID, clanID string) {
	s.call, s.gameID, s.externalID, s.clanID = call, gameID, externalID, clanID
}

func (s *requestingService) RequestJoin(_ context.Context, gameID, externalID, name, clanID string) (service.JoinRequest, error) {
	s.note("request", gameID, externalID, clanID)
	s.name = name
	if s.err != nil {
		return service.JoinRequest{}, s.err
	}
	return service.JoinRequest{ID: requestID, PlayerID: "p-1", DisplayName: name, CreatedAt: time.Date(2026, 11, 17, 9, 12, 0, 0, time.UTC)}, nil
}

func (s *requestingService) CancelJoinRequest(_ context.Context, gameID, externalID, clanID string) error {
	s.note("cancel", gameID, externalID, clanID)
	return s.err
}

func (s *requestingService) ListJoinRequests(_ context.Context, gameID, externalID, clanID string) ([]service.JoinRequest, error) {
	s.note("list", gameID, externalID, clanID)
	return s.rows, s.err
}

func (s *requestingService) AcceptJoinRequest(_ context.Context, gameID, externalID, clanID, reqID string) error {
	s.note("accept", gameID, externalID, clanID)
	s.reqID = reqID
	return s.err
}

func (s *requestingService) DeclineJoinRequest(_ context.Context, gameID, externalID, clanID, reqID string) error {
	s.note("decline", gameID, externalID, clanID)
	s.reqID = reqID
	return s.err
}

func (s *requestingService) SetJoinPolicy(_ context.Context, gameID, externalID, clanID, policy string) (service.Clan, error) {
	s.note("policy", gameID, externalID, clanID)
	s.policyArg = policy
	if s.err != nil {
		return service.Clan{}, s.err
	}
	return service.Clan{ID: clanID, Name: "Wolves", MemberCount: 4, MaxMembers: 10, JoinPolicy: policy}, nil
}

func TestRequestJoinReturns201WithTheRequest(t *testing.T) {
	svc := &requestingService{}
	rec := postJoin(t, svc, http.MethodPost, requestsPath, false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.call != "request" || svc.gameID != tokenGame || svc.externalID != "u_42" || svc.name != "Ali" || svc.clanID != joinClanID {
		t.Fatalf("service got %s game=%q caller=%q name=%q clan=%q; want the token's game and player, and the URL's clan",
			svc.call, svc.gameID, svc.externalID, svc.name, svc.clanID)
	}
	want := `{"id":"` + requestID + `","playerId":"p-1","displayName":"Ali","createdAt":"2026-11-17T09:12:00Z"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestListJoinRequestsReturnsTheRequests(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 12, 0, 0, time.UTC)
	svc := &requestingService{rows: []service.JoinRequest{{ID: requestID, PlayerID: "p-1", DisplayName: "Rashad", CreatedAt: at}}}
	rec := postJoin(t, svc, http.MethodGet, requestsPath, false)
	if rec.Code != http.StatusOK || svc.call != "list" || svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID {
		t.Fatalf("status %d, call %q game=%q caller=%q clan=%q", rec.Code, svc.call, svc.gameID, svc.externalID, svc.clanID)
	}
	want := `{"requests":[{"id":"` + requestID + `","playerId":"p-1","displayName":"Rashad","createdAt":"2026-11-17T09:12:00Z"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}

	// No requests is an empty list, never null.
	rec = postJoin(t, &requestingService{}, http.MethodGet, requestsPath, false)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"requests":[]}` {
		t.Fatalf("empty body = %s, want an empty list", got)
	}
}

func TestDecidingAndCancellingReturn204WithNoBody(t *testing.T) {
	for _, tc := range []struct{ name, method, path, call string }{
		{"cancel", http.MethodDelete, myRequest, "cancel"},
		{"accept", http.MethodPost, acceptPath, "accept"},
		{"decline", http.MethodPost, declinePath, "decline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &requestingService{}
			rec := postJoin(t, svc, tc.method, tc.path, false)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status %d, body %q; want 204 and nothing", rec.Code, rec.Body.String())
			}
			if svc.call != tc.call || svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID {
				t.Fatalf("service got %s game=%q caller=%q clan=%q", svc.call, svc.gameID, svc.externalID, svc.clanID)
			}
			if tc.call != "cancel" && svc.reqID != requestID {
				t.Fatalf("the request ID is %q, want the one in the URL", svc.reqID)
			}
		})
	}
}

func TestRequestEndpointsPassServiceErrorsAndHideInternalOnes(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{httpx.NewError(http.StatusNotFound, "clan_not_found", "x"), http.StatusNotFound, "clan_not_found"},
		{httpx.NewError(http.StatusForbidden, "not_allowed", "x"), http.StatusForbidden, "not_allowed"},
		{httpx.NewError(http.StatusNotFound, "request_not_found", "x"), http.StatusNotFound, "request_not_found"},
		{httpx.NewError(http.StatusConflict, "clan_open", "x"), http.StatusConflict, "clan_open"},
		{httpx.NewError(http.StatusConflict, "too_many_requests", "x"), http.StatusConflict, "too_many_requests"},
		{errors.New("db down at 10.0.0.5"), http.StatusInternalServerError, "internal_error"},
	}
	for _, path := range []struct{ method, path string }{
		{http.MethodPost, requestsPath}, {http.MethodGet, requestsPath}, {http.MethodDelete, myRequest},
		{http.MethodPost, acceptPath}, {http.MethodPost, declinePath},
	} {
		for _, tc := range tests {
			rec := postJoin(t, &requestingService{err: tc.err}, path.method, path.path, false)
			var e httpx.ErrorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code != tc.status || e.Error.Code != tc.code || strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Errorf("%s %s with %v: status %d code %q body %s", path.method, path.path, tc.err, rec.Code, e.Error.Code, rec.Body.String())
			}
		}
	}
}

func TestRequestEndpointsNeedAPlayerToken(t *testing.T) {
	for _, path := range []struct{ method, path string }{
		{http.MethodPost, requestsPath}, {http.MethodGet, requestsPath}, {http.MethodDelete, myRequest},
		{http.MethodPost, acceptPath}, {http.MethodPost, declinePath},
	} {
		svc := &requestingService{}
		if rec := postJoin(t, svc, path.method, path.path, true); rec.Code != http.StatusUnauthorized || svc.call != "" {
			t.Errorf("%s %s without a token: status %d, called %q; want 401 and no call", path.method, path.path, rec.Code, svc.call)
		}
	}
}

func TestPatchClanOpensOrClosesIt(t *testing.T) {
	svc := &requestingService{}
	rec := sendRename(t, svc, http.MethodPatch, renamePath, `{"joinPolicy":"closed"}`, false)
	if rec.Code != http.StatusOK || svc.call != "policy" || svc.policyArg != "closed" || svc.gameID != tokenGame || svc.externalID != "u_42" || svc.clanID != joinClanID {
		t.Fatalf("status %d, call %q policy %q game=%q caller=%q clan=%q", rec.Code, svc.call, svc.policyArg, svc.gameID, svc.externalID, svc.clanID)
	}
	want := `{"id":"` + joinClanID + `","name":"Wolves","memberCount":4,"maxMembers":10,"joinPolicy":"closed"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}

	// A name and a policy in one call is refused, and nothing is changed.
	svc = &requestingService{}
	rec = sendRename(t, svc, http.MethodPatch, renamePath, `{"name":"Grey Wolves","joinPolicy":"open"}`, false)
	var e httpx.ErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusBadRequest || e.Error.Code != "invalid_clan_update" || svc.call != "" {
		t.Fatalf("both: status %d code %q called %q; want 400 invalid_clan_update and no call", rec.Code, e.Error.Code, svc.call)
	}

	// A policy that is not text is a bad body, like any other.
	if rec := sendRename(t, &requestingService{}, http.MethodPatch, renamePath, `{"joinPolicy":7}`, false); rec.Code != http.StatusBadRequest {
		t.Errorf("a number as joinPolicy: status %d, want 400", rec.Code)
	}
}

func TestCreateClanPassesTheJoinPolicy(t *testing.T) {
	svc := &creatingService{}
	rec := sendRename(t, svc, http.MethodPost, "/api/v1/clans", `{"name":"Wolves","joinPolicy":"closed"}`, false)
	if rec.Code != http.StatusCreated || svc.in.JoinPolicy != "closed" {
		t.Fatalf("status %d, the service got policy %q; want 201 and closed", rec.Code, svc.in.JoinPolicy)
	}
}

// A clan in the list says whether the caller asked to join it, only when they did.
func TestClanListMarksTheCallersRequests(t *testing.T) {
	svc := &clanService{clans: []service.Clan{
		{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10, JoinPolicy: "closed", Requested: true},
		{ID: "c-2", Name: "Foxes", MemberCount: 5, MaxMembers: 10, JoinPolicy: "open"},
	}}
	rec := callClans(t, svc, "")
	want := `{"clans":[{"id":"c-1","name":"Wolves","memberCount":3,"maxMembers":10,"joinPolicy":"closed","requested":true},` +
		`{"id":"c-2","name":"Foxes","memberCount":5,"maxMembers":10,"joinPolicy":"open"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

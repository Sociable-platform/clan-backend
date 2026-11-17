package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// requestStore is a closed clan with the four members of member_roles_test.go. It records what the service
// asked, runs the "may this member decide" rule the service gave it on every member (as the repository does on
// the caller), and answers with err or the results below.
type requestStore struct {
	*memStore
	err error

	maxMembers, perClan, perPlayer int
	roleID                         string
	external, clan, request        string
	allowed                        map[string]bool // by the member's player ID: what the service's rule said

	listed   []repository.JoinRequest
	requests int // how many calls reached the store
}

func newRequestStore() *requestStore {
	return &requestStore{memStore: newMemStore()}
}

var clanMembers = []repository.MemberRole{
	{Player: repository.ClanPlayer{ID: pOwner, DisplayName: "Ali"}, RoleID: "owner", IsOwner: true},
	{Player: repository.ClanPlayer{ID: pCo, DisplayName: "Aysel"}, RoleID: "co-leader"},
	{Player: repository.ClanPlayer{ID: pElder, DisplayName: "Leyla"}, RoleID: "elder"},
	{Player: repository.ClanPlayer{ID: pNew, DisplayName: "Murad"}, RoleID: "member"},
}

func (s *requestStore) judge(allowed func(repository.MemberRole) bool) {
	s.allowed = map[string]bool{}
	for _, m := range clanMembers {
		s.allowed[m.Player.ID] = allowed(m)
	}
}

func (s *requestStore) RequestJoin(_ context.Context, gameID, clanID, playerID string, maxMembers, perClan, perPlayer int) (repository.JoinRequestResult, error) {
	s.requests++
	s.clan, s.maxMembers, s.perClan, s.perPlayer = clanID, maxMembers, perClan, perPlayer
	if s.err != nil {
		return repository.JoinRequestResult{}, s.err
	}
	return repository.JoinRequestResult{
		Request: repository.JoinRequest{ID: "req-1", Player: repository.ClanPlayer{ID: playerID, DisplayName: "Rashad"}, CreatedAt: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)},
		Clan:    repository.ClanRef{ID: clanID, Name: "Wolves"},
		Members: clanMembers,
	}, nil
}

func (s *requestStore) CancelJoinRequest(_ context.Context, _, clanID, externalID string) error {
	s.requests++
	s.clan, s.external = clanID, externalID
	return s.err
}

func (s *requestStore) ListJoinRequests(_ context.Context, _, clanID, externalID string, allowed func(repository.MemberRole) bool) ([]repository.JoinRequest, error) {
	s.requests++
	s.clan, s.external = clanID, externalID
	s.judge(allowed)
	return s.listed, s.err
}

func (s *requestStore) AcceptJoinRequest(_ context.Context, _, clanID, requestID, externalID string, maxMembers int, roleID string,
	allowed func(repository.MemberRole) bool) (repository.RequestDecision, error) {
	s.requests++
	s.clan, s.request, s.external, s.maxMembers, s.roleID = clanID, requestID, externalID, maxMembers, roleID
	s.judge(allowed)
	if s.err != nil {
		return repository.RequestDecision{}, s.err
	}
	return repository.RequestDecision{
		Clan: repository.ClanRef{ID: clanID, Name: "Wolves"}, Player: repository.ClanPlayer{ID: pNew, DisplayName: "Rashad"},
		By: repository.ClanPlayer{ID: pOwner, DisplayName: "Ali"},
	}, nil
}

func (s *requestStore) DeclineJoinRequest(_ context.Context, _, clanID, requestID, externalID string,
	allowed func(repository.MemberRole) bool) (repository.RequestDecision, error) {
	s.requests++
	s.clan, s.request, s.external = clanID, requestID, externalID
	s.judge(allowed)
	if s.err != nil {
		return repository.RequestDecision{}, s.err
	}
	return repository.RequestDecision{
		Clan: repository.ClanRef{ID: clanID, Name: "Wolves"}, Player: repository.ClanPlayer{ID: pNew, DisplayName: "Rashad"},
		By: repository.ClanPlayer{ID: pCo, DisplayName: "Aysel"},
	}, nil
}

func (s *requestStore) SetJoinPolicy(_ context.Context, _, clanID, externalID, policy string) (repository.Clan, error) {
	s.requests++
	s.clan, s.external = clanID, externalID
	if s.err != nil {
		return repository.Clan{}, s.err
	}
	return repository.Clan{ID: clanID, Name: "Wolves", MemberCount: 4, JoinPolicy: policy}, nil
}

const someRequest = "8c1f4d2a-5b6e-4f70-9a81-b2c3d4e5f607"

func TestRequestJoinTellsTheLeadersAndOnlyThem(t *testing.T) {
	store := newRequestStore()
	svc, rec := friendService(store)
	svc.WithRules(arenaRules{}) // co-leader and leader may remove members; elder and member may not

	got, err := svc.RequestJoin(context.Background(), gameA, "u_rashad", "Rashad", strings.ToUpper(someClan))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "req-1" || got.DisplayName != "Rashad" || got.PlayerID == "" {
		t.Errorf("request = %+v, want the saved one", got)
	}
	if store.clan != someClan || store.maxMembers != 30 || store.perClan != maxRequestsPerClan || store.perPlayer != maxRequestsPerPlayer {
		t.Errorf("store got clan=%q max=%d perClan=%d perPlayer=%d; want the clan in lower case, the game's size and the two limits",
			store.clan, store.maxMembers, store.perClan, store.perPlayer)
	}
	data := oneEvent(t, rec, EventClanJoinRequested)
	wantData(t, data, map[string]any{
		"clan":       map[string]any{"id": someClan, "name": "Wolves"},
		"player":     map[string]any{"id": got.PlayerID, "name": "Rashad"},
		"request_id": "req-1",
		"managers":   []string{pOwner, pCo}, // the owner and the co-leader: not the elder, not the member
	})
}

func TestRequestJoinWithoutPublishedRolesTellsTheOwnerOnly(t *testing.T) {
	store := newRequestStore()
	svc, rec := friendService(store)
	if _, err := svc.RequestJoin(context.Background(), gameA, "u_rashad", "Rashad", someClan); err != nil {
		t.Fatal(err)
	}
	data := oneEvent(t, rec, EventClanJoinRequested)
	if !reflect.DeepEqual(data["managers"], []string{pOwner}) {
		t.Errorf("managers = %v, want only the owner while the game has no roles", data["managers"])
	}
}

func TestJoinRequestErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrClanOpen, http.StatusConflict, "clan_open"},
		{repository.ErrAlreadyInClan, http.StatusConflict, "already_in_clan"},
		{repository.ErrAlreadyRequested, http.StatusConflict, "already_requested"},
		{repository.ErrClanFull, http.StatusConflict, "clan_full"},
		{repository.ErrClanRequestsFull, http.StatusConflict, "clan_requests_full"},
		{repository.ErrTooManyRequests, http.StatusConflict, "too_many_requests"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			store := newRequestStore()
			store.err = c.err
			svc, rec := friendService(store)
			_, err := svc.RequestJoin(context.Background(), gameA, "u_rashad", "Rashad", someClan)
			wantHTTPError(t, err, c.status, c.code)
			if n := len(rec.Events()); n != 0 {
				t.Errorf("%d events after a refused request, want none", n)
			}
		})
	}
	// A database failure is not an answer for the client: it is wrapped and stays an internal error.
	store := newRequestStore()
	store.err = errors.New("database down")
	_, err := New(store).RequestJoin(context.Background(), gameA, "u_rashad", "Rashad", someClan)
	if err == nil || errors.Is(err, repository.ErrClanFull) || !strings.Contains(err.Error(), "database down") {
		t.Errorf("err = %v, want the wrapped database error", err)
	}
}

func TestJoinRequestBadIDsDoNotReachTheDatabase(t *testing.T) {
	calls := map[string]func(*Service) error{
		"request": func(s *Service) error {
			_, err := s.RequestJoin(context.Background(), gameA, "u_1", "Ali", "x")
			return err
		},
		"cancel": func(s *Service) error { return s.CancelJoinRequest(context.Background(), gameA, "u_1", "x") },
		"list": func(s *Service) error {
			_, err := s.ListJoinRequests(context.Background(), gameA, "u_1", "x")
			return err
		},
		"accept": func(s *Service) error {
			return s.AcceptJoinRequest(context.Background(), gameA, "u_1", "x", someRequest)
		},
		"decline": func(s *Service) error {
			return s.DeclineJoinRequest(context.Background(), gameA, "u_1", "x", someRequest)
		},
		"policy": func(s *Service) error {
			_, err := s.SetJoinPolicy(context.Background(), gameA, "u_1", "x", "closed")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			store := newRequestStore()
			wantHTTPError(t, call(New(store)), http.StatusNotFound, "clan_not_found")
			if store.requests != 0 {
				t.Errorf("the database was asked")
			}
		})
	}
	for name, call := range map[string]func(*Service) error{
		"accept": func(s *Service) error {
			return s.AcceptJoinRequest(context.Background(), gameA, "u_1", someClan, "not-a-uuid")
		},
		"decline": func(s *Service) error { return s.DeclineJoinRequest(context.Background(), gameA, "u_1", someClan, "") },
	} {
		t.Run(name+": a request ID that is not a UUID", func(t *testing.T) {
			store := newRequestStore()
			wantHTTPError(t, call(New(store)), http.StatusNotFound, "request_not_found")
			if store.requests != 0 {
				t.Errorf("the database was asked")
			}
		})
	}
}

// The rule the repository applies to the caller: the owner and a role that may remove members decide; an
// elder and a plain member do not.
func TestWhoMayDecideOnRequests(t *testing.T) {
	want := map[string]bool{pOwner: true, pCo: true, pElder: false, pNew: false}
	for name, call := range map[string]func(*Service, *requestStore) error{
		"list": func(s *Service, _ *requestStore) error {
			_, err := s.ListJoinRequests(context.Background(), gameA, "u_co", someClan)
			return err
		},
		"accept": func(s *Service, _ *requestStore) error {
			return s.AcceptJoinRequest(context.Background(), gameA, "u_co", someClan, someRequest)
		},
		"decline": func(s *Service, _ *requestStore) error {
			return s.DeclineJoinRequest(context.Background(), gameA, "u_co", someClan, someRequest)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRequestStore()
			if err := call(New(store).WithRules(arenaRules{}), store); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(store.allowed, want) {
				t.Errorf("rule = %v, want %v", store.allowed, want)
			}
		})
	}
	// With no published roles only the owner may.
	store := newRequestStore()
	if _, err := New(store).ListJoinRequests(context.Background(), gameA, "u_co", someClan); err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{pOwner: true, pCo: false, pElder: false, pNew: false}; !reflect.DeepEqual(store.allowed, want) {
		t.Errorf("default rule = %v, want only the owner", store.allowed)
	}
}

func TestListJoinRequests(t *testing.T) {
	store := newRequestStore()
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	store.listed = []repository.JoinRequest{{ID: "r1", Player: repository.ClanPlayer{ID: pNew, DisplayName: "Rashad"}, CreatedAt: at}}
	got, err := New(store).ListJoinRequests(context.Background(), gameA, "u_owner", strings.ToUpper(someClan))
	if err != nil {
		t.Fatal(err)
	}
	if want := []JoinRequest{{ID: "r1", PlayerID: pNew, DisplayName: "Rashad", CreatedAt: at}}; !reflect.DeepEqual(got, want) || store.clan != someClan {
		t.Errorf("list = %+v for clan %q, want %+v", got, store.clan, want)
	}

	store.listed = nil
	if got, err = New(store).ListJoinRequests(context.Background(), gameA, "u_owner", someClan); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty list = %#v, %v; want an empty, not nil, list", got, err)
	}
	store.err = repository.ErrNotAllowed
	_, err = New(store).ListJoinRequests(context.Background(), gameA, "u_new", someClan)
	wantHTTPError(t, err, http.StatusForbidden, "not_allowed")
}

func TestAcceptJoinRequestMakesAMemberAndPublishesClanMemberJoined(t *testing.T) {
	store := newRequestStore()
	svc, rec := friendService(store)
	svc.WithRules(arenaRules{})
	if err := svc.AcceptJoinRequest(context.Background(), gameA, "u_co", strings.ToUpper(someClan), strings.ToUpper(someRequest)); err != nil {
		t.Fatal(err)
	}
	if store.clan != someClan || store.request != someRequest || store.external != "u_co" || store.maxMembers != 30 || store.roleID != "member" {
		t.Errorf("store got clan=%q request=%q caller=%q max=%d role=%q", store.clan, store.request, store.external, store.maxMembers, store.roleID)
	}
	// The same event a direct join writes: Realtime gives the player the room and the others a join line.
	wantData(t, oneEvent(t, rec, EventClanMemberJoined), map[string]any{
		"clan":   map[string]any{"id": someClan, "name": "Wolves"},
		"player": map[string]any{"id": pNew, "name": "Rashad"},
	})
}

func TestDeclineJoinRequestPublishesWhoDeclined(t *testing.T) {
	store := newRequestStore()
	svc, rec := friendService(store)
	if err := svc.DeclineJoinRequest(context.Background(), gameA, "u_owner", someClan, someRequest); err != nil {
		t.Fatal(err)
	}
	wantData(t, oneEvent(t, rec, EventClanJoinRequestDeclined), map[string]any{
		"clan":        map[string]any{"id": someClan, "name": "Wolves"},
		"player":      map[string]any{"id": pNew, "name": "Rashad"},
		"declined_by": map[string]any{"id": pCo, "name": "Aysel"},
	})
}

func TestDecidingOnARequestErrorsWriteNoEvent(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{repository.ErrClanNotFound, http.StatusNotFound, "clan_not_found"},
		{repository.ErrNotAllowed, http.StatusForbidden, "not_allowed"},
		{repository.ErrRequestNotFound, http.StatusNotFound, "request_not_found"},
		{repository.ErrClanFull, http.StatusConflict, "clan_full"},
		{repository.ErrAlreadyInClan, http.StatusConflict, "already_in_clan"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			for name, call := range map[string]func(*Service) error{
				"accept": func(s *Service) error {
					return s.AcceptJoinRequest(context.Background(), gameA, "u_1", someClan, someRequest)
				},
				"decline": func(s *Service) error {
					return s.DeclineJoinRequest(context.Background(), gameA, "u_1", someClan, someRequest)
				},
			} {
				store := newRequestStore()
				store.err = c.err
				svc, rec := friendService(store)
				wantHTTPError(t, call(svc), c.status, c.code)
				if n := len(rec.Events()); n != 0 {
					t.Errorf("%s: %d events after a refusal, want none", name, n)
				}
			}
		})
	}
}

func TestCancelJoinRequestWritesNoEvent(t *testing.T) {
	store := newRequestStore()
	svc, rec := friendService(store)
	if err := svc.CancelJoinRequest(context.Background(), gameA, "u_rashad", strings.ToUpper(someClan)); err != nil {
		t.Fatal(err)
	}
	if store.clan != someClan || store.external != "u_rashad" || len(rec.Events()) != 0 {
		t.Errorf("store got clan=%q caller=%q, events=%d; want the clan in lower case, the caller, no event", store.clan, store.external, len(rec.Events()))
	}
	store.err = repository.ErrRequestNotFound
	wantHTTPError(t, svc.CancelJoinRequest(context.Background(), gameA, "u_rashad", someClan), http.StatusNotFound, "request_not_found")
}

func TestSetJoinPolicy(t *testing.T) {
	for _, policy := range []string{"open", "closed", " closed ", ""} {
		store := newRequestStore()
		c, err := New(store).SetJoinPolicy(context.Background(), gameA, "u_owner", someClan, policy)
		want := strings.TrimSpace(policy)
		if want == "" {
			want = "open"
		}
		if err != nil || c.JoinPolicy != want {
			t.Errorf("policy %q: clan %+v, %v; want %q", policy, c, err, want)
		}
	}
	store := newRequestStore()
	_, err := New(store).SetJoinPolicy(context.Background(), gameA, "u_owner", someClan, "secret")
	wantHTTPError(t, err, http.StatusBadRequest, "invalid_join_policy")
	if store.requests != 0 {
		t.Error("a policy that is not one reached the database")
	}
	store.err = repository.ErrNotClanOwner
	_, err = New(store).SetJoinPolicy(context.Background(), gameA, "u_new", someClan, "closed")
	wantHTTPError(t, err, http.StatusForbidden, "not_clan_owner")
}

func TestCreateClanTakesAJoinPolicy(t *testing.T) {
	for in, want := range map[string]string{"": "open", "open": "open", "closed": "closed", " closed ": "closed"} {
		store := newCreatingStore()
		if _, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", NewClan{Name: "Wolves", JoinPolicy: in}); err != nil {
			t.Fatalf("policy %q: %v", in, err)
		}
		if store.got.JoinPolicy != want {
			t.Errorf("policy %q: the store got %q, want %q", in, store.got.JoinPolicy, want)
		}
	}
	store := newCreatingStore()
	_, err := New(store).CreateClan(context.Background(), gameA, "u_42", "Ali", NewClan{Name: "Wolves", JoinPolicy: "secret"})
	wantHTTPError(t, err, http.StatusBadRequest, "invalid_join_policy")
	if store.creates != 0 {
		t.Error("a clan with a policy that is not one was created")
	}
}

func TestJoinClanOfAClosedClanIs403(t *testing.T) {
	store := newJoiningStore()
	store.joinErr = repository.ErrClanClosed
	_, err := New(store).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan)
	wantHTTPError(t, err, http.StatusForbidden, "clan_closed")
}

// requestRules is a game that chose who decides on requests (TEAM-300): every member, the elder too, but not
// the co-leader, though it may remove members. The leader (the owner's role) says no, which the owner ignores.
type requestRules struct{}

func (requestRules) Rules(context.Context, string) gameconfig.Rules {
	yes, no := true, false
	return gameconfig.Rules{MaxMembers: 30, Published: true, Roles: []gameconfig.Role{
		{ID: "member", Name: "Member", CanHandleRequests: &yes},
		{ID: "elder", Name: "Elder", CanHandleRequests: &yes},
		{ID: "co-leader", Name: "Co-leader", CanPromote: true, CanKick: true, CanHandleRequests: &no},
		{ID: "leader", Name: "Leader", CanPromote: true, CanKick: true, CanHandleRequests: &no},
	}}
}

func TestTheGameChoosesWhoDecidesOnRequests(t *testing.T) {
	want := map[string]bool{pOwner: true, pCo: false, pElder: true, pNew: true}
	for name, call := range map[string]func(*Service) error{
		"list": func(s *Service) error {
			_, err := s.ListJoinRequests(context.Background(), gameA, "u_elder", someClan)
			return err
		},
		"accept": func(s *Service) error {
			return s.AcceptJoinRequest(context.Background(), gameA, "u_elder", someClan, someRequest)
		},
		"decline": func(s *Service) error {
			return s.DeclineJoinRequest(context.Background(), gameA, "u_elder", someClan, someRequest)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRequestStore()
			if err := call(New(store).WithRules(requestRules{})); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(store.allowed, want) {
				t.Errorf("rule = %v, want %v", store.allowed, want)
			}
		})
	}

	// The new-request notification goes to the same members.
	store := newRequestStore()
	svc, rec := friendService(store)
	svc.WithRules(requestRules{})
	if _, err := svc.RequestJoin(context.Background(), gameA, "u_rashad", "Rashad", someClan); err != nil {
		t.Fatal(err)
	}
	if got := oneEvent(t, rec, EventClanJoinRequested)["managers"]; !reflect.DeepEqual(got, []string{pOwner, pElder, pNew}) {
		t.Errorf("managers = %v, want the owner, the elder and the member: not the co-leader", got)
	}
}

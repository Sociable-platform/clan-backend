package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// arenaRules is a game with Studio's default roles, lowest first.
type arenaRules struct{}

func (arenaRules) Rules(context.Context, string) gameconfig.Rules {
	return gameconfig.Rules{MaxMembers: 30, Published: true, Roles: []gameconfig.Role{
		{ID: "member", Name: "Member"},
		{ID: "elder", Name: "Elder"},
		{ID: "co-leader", Name: "Co-leader", CanPromote: true, CanKick: true},
		{ID: "leader", Name: "Leader", CanPromote: true, CanKick: true},
	}}
}

const (
	pOwner = "aaaaaaaa-0000-4000-8000-00000000000a"
	pCo    = "aaaaaaaa-0000-4000-8000-00000000000b"
	pElder = "aaaaaaaa-0000-4000-8000-00000000000c"
	pNew   = "aaaaaaaa-0000-4000-8000-00000000000d"
)

// clanWithRoles is a clan of four, by the game's player IDs ("u_owner" ...) and social's
// player IDs. KickMember and ChangeRole ask the service's rules with the stored roles, as the
// repository does inside its lock, and keep what they decided.
type clanWithRoles struct {
	*memStore
	byExternal map[string]repository.MemberRole
	byID       map[string]repository.MemberRole
	kicked     string
	changedTo  string
}

func newClanWithRoles() *clanWithRoles {
	c := &clanWithRoles{memStore: newMemStore(), byExternal: map[string]repository.MemberRole{}, byID: map[string]repository.MemberRole{}}
	add := func(ext, id, name, role string) {
		m := repository.MemberRole{Player: repository.ClanPlayer{ID: id, DisplayName: name}, RoleID: role, IsOwner: role == "owner"}
		c.byExternal[ext], c.byID[id] = m, m
	}
	add("u_owner", pOwner, "Ali", "owner")
	add("u_co", pCo, "Aysel", "co-leader")
	add("u_elder", pElder, "Leyla", "elder")
	add("u_new", pNew, "Murad", "member")
	return c
}

func (c *clanWithRoles) members(actorExt, playerID string) (repository.MemberRole, repository.MemberRole, error) {
	actor, ok := c.byExternal[actorExt]
	if !ok {
		return repository.MemberRole{}, repository.MemberRole{}, repository.ErrNotAllowed
	}
	target, ok := c.byID[playerID]
	if !ok {
		return repository.MemberRole{}, repository.MemberRole{}, repository.ErrMemberNotFound
	}
	return actor, target, nil
}

func (c *clanWithRoles) KickMember(_ context.Context, _, clanID, actorExt, playerID string,
	allowed func(actor, target repository.MemberRole) bool) (repository.LeaveResult, error) {
	actor, target, err := c.members(actorExt, playerID)
	if err != nil {
		return repository.LeaveResult{}, err
	}
	if !allowed(actor, target) {
		return repository.LeaveResult{}, repository.ErrNotAllowed
	}
	c.kicked = playerID
	by := actor.Player
	return repository.LeaveResult{ClanID: clanID, ClanName: "Wolves", Player: target.Player, KickedBy: &by}, nil
}

func (c *clanWithRoles) ChangeRole(_ context.Context, _, clanID, actorExt, playerID string,
	decide func(actor, target repository.MemberRole) (string, error)) (repository.RoleChange, error) {
	actor, target, err := c.members(actorExt, playerID)
	if err != nil {
		return repository.RoleChange{}, err
	}
	next, err := decide(actor, target)
	if err != nil {
		return repository.RoleChange{}, err
	}
	c.changedTo = next
	return repository.RoleChange{Clan: repository.ClanRef{ID: clanID, Name: "Wolves"}, Player: target.Player,
		PreviousRoleID: target.RoleID, NewRoleID: next, ChangedBy: actor.Player}, nil
}

func TestANewMemberGetsTheFirstRoleOfTheGame(t *testing.T) {
	join := newJoiningStore()
	if _, err := New(join).WithRules(arenaRules{}).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	if join.role != "member" {
		t.Fatalf("role = %q, want the first role, member", join.role)
	}
	recruit := gameRolesSource{"recruit", "veteran"}
	join = newJoiningStore()
	if _, err := New(join).WithRules(recruit).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	if join.role != "recruit" {
		t.Fatalf("role = %q, want the game's first role, recruit", join.role)
	}
	join = newJoiningStore()
	if _, err := New(join).JoinClan(context.Background(), gameA, "u_42", "Ali", someClan); err != nil {
		t.Fatal(err)
	}
	if join.role != "member" {
		t.Fatalf("no roles published: role = %q, want member", join.role)
	}
}

// gameRolesSource is a game whose roles have these ids (no powers), lowest first.
type gameRolesSource []string

func (g gameRolesSource) Rules(context.Context, string) gameconfig.Rules {
	r := gameconfig.Rules{MaxMembers: 10, Published: true}
	for _, id := range g {
		r.Roles = append(r.Roles, gameconfig.Role{ID: id, Name: id})
	}
	return r
}

func TestKickFollowsTheRoles(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, actor, target string
		code                string // "" = allowed
	}{
		{"a co-leader kicks an elder", "u_co", pElder, ""},
		{"a co-leader kicks a member", "u_co", pNew, ""},
		{"the owner kicks a co-leader", "u_owner", pCo, ""},
		{"an elder has no canKick", "u_elder", pNew, "not_allowed"},
		{"nobody kicks the owner", "u_co", pOwner, "not_allowed"},
		{"not someone of the same role or higher", "u_elder", pCo, "not_allowed"},
		{"not in the clan", "u_stranger", pNew, "not_allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newClanWithRoles()
			svc, rec := friendService(store)
			svc.WithRules(arenaRules{})
			err := svc.KickMember(ctx, gameA, c.actor, someClan, c.target)
			if c.code == "" {
				if err != nil || store.kicked != c.target {
					t.Fatalf("err = %v, kicked %q; want %s kicked", err, store.kicked, c.target)
				}
				if data := oneEvent(t, rec, EventClanMemberLeft); data["reason"] != "kicked" {
					t.Fatalf("event %v, want reason kicked", data)
				}
				return
			}
			wantHTTPError(t, err, http.StatusForbidden, c.code)
			if evs := rec.Events(); len(evs) != 0 || store.kicked != "" {
				t.Fatalf("a refused kick removed %q or published %+v", store.kicked, evs)
			}
		})
	}
}

func TestPromoteAndDemote(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, actor, target string
		promote             bool
		role                string // the new role; "" = refused with code
		status              int
		code                string
	}{
		{"owner promotes a member to elder", "u_owner", pNew, true, "elder", 0, ""},
		{"owner promotes an elder to co-leader", "u_owner", pElder, true, "co-leader", 0, ""},
		{"nobody is promoted to owner", "u_owner", pCo, true, "", http.StatusForbidden, "not_allowed"},
		{"a co-leader promotes a member to elder", "u_co", pNew, true, "elder", 0, ""},
		{"a co-leader cannot make an equal", "u_co", pElder, true, "", http.StatusForbidden, "not_allowed"},
		{"an elder has no canPromote", "u_elder", pNew, true, "", http.StatusForbidden, "not_allowed"},
		{"owner demotes a co-leader to elder", "u_owner", pCo, false, "elder", 0, ""},
		{"a co-leader demotes an elder", "u_co", pElder, false, "member", 0, ""},
		{"nobody demotes the owner", "u_co", pOwner, false, "", http.StatusForbidden, "not_allowed"},
		{"a member is already lowest", "u_owner", pNew, false, "", http.StatusConflict, "already_lowest_role"},
		{"not themselves", "u_co", pCo, true, "", http.StatusForbidden, "not_allowed"},
		{"not in the clan", "u_stranger", pNew, true, "", http.StatusForbidden, "not_allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newClanWithRoles()
			svc, rec := friendService(store)
			svc.WithRules(arenaRules{})
			change := svc.DemoteMember
			if c.promote {
				change = svc.PromoteMember
			}
			got, err := change(ctx, gameA, c.actor, someClan, c.target)
			if c.role == "" {
				wantHTTPError(t, err, c.status, c.code)
				if evs := rec.Events(); len(evs) != 0 || store.changedTo != "" {
					t.Fatalf("a refused change saved %q or published %+v", store.changedTo, evs)
				}
				return
			}
			if err != nil || got.Role != c.role || got.PlayerID != c.target || store.changedTo != c.role {
				t.Fatalf("got %+v, %v; saved %q; want %s", got, err, store.changedTo, c.role)
			}
		})
	}
}

func TestRoleChangePublishesTheEvent(t *testing.T) {
	store := newClanWithRoles()
	// Murad's stored role was removed from the game's roles: he counts as the first role, and
	// the event says so, not the stale id
	m := store.byID[pNew]
	m.RoleID = "veteran"
	store.byID[pNew], store.byExternal["u_new"] = m, m
	svc, rec := friendService(store)
	svc.WithRules(arenaRules{})
	if _, err := svc.PromoteMember(context.Background(), gameA, "u_co", someClan, pNew); err != nil {
		t.Fatal(err)
	}
	wantData(t, oneEvent(t, rec, EventClanMemberRoleChanged), map[string]any{
		"clan":          ref(someClan, "Wolves"),
		"player":        ref(pNew, "Murad"),
		"role":          "elder",
		"previous_role": "member",
		"changed_by":    ref(pCo, "Aysel"),
	})
}

func TestRoleChangeBadIDsAndRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := New(newClanWithRoles()).WithRules(arenaRules{})
	_, err := svc.PromoteMember(ctx, gameA, "u_owner", "nope", pNew)
	wantHTTPError(t, err, http.StatusNotFound, "clan_not_found")
	_, err = svc.PromoteMember(ctx, gameA, "u_owner", someClan, "nope")
	wantHTTPError(t, err, http.StatusNotFound, "member_not_found")
	_, err = svc.DemoteMember(ctx, gameA, "u_owner", someClan, "aaaaaaaa-0000-4000-8000-0000000000ff")
	wantHTTPError(t, err, http.StatusNotFound, "member_not_found")

	broken := &brokenRoleStore{clanWithRoles: newClanWithRoles(), err: errors.New("database is down")}
	if _, err := New(broken).WithRules(arenaRules{}).PromoteMember(ctx, gameA, "u_owner", someClan, pNew); err == nil {
		t.Fatal("want an internal error")
	}
	broken.err = repository.ErrClanNotFound
	_, err = New(broken).WithRules(arenaRules{}).PromoteMember(ctx, gameA, "u_owner", someClan, pNew)
	wantHTTPError(t, err, http.StatusNotFound, "clan_not_found")
}

type brokenRoleStore struct {
	*clanWithRoles
	err error
}

func (b *brokenRoleStore) ChangeRole(context.Context, string, string, string, string,
	func(repository.MemberRole, repository.MemberRole) (string, error)) (repository.RoleChange, error) {
	return repository.RoleChange{}, b.err
}

// Without published roles the old rules hold: only the owner kicks, nothing to promote to.
func TestWithoutRolesNothingChanges(t *testing.T) {
	ctx := context.Background()
	svc := New(newClanWithRoles()) // default rules: no roles
	_, err := svc.PromoteMember(ctx, gameA, "u_owner", someClan, pNew)
	wantHTTPError(t, err, http.StatusForbidden, "not_allowed")
	if err := svc.KickMember(ctx, gameA, "u_owner", someClan, pNew); err != nil {
		t.Fatalf("the owner kicks: %v", err)
	}
	err = New(newClanWithRoles()).KickMember(ctx, gameA, "u_co", someClan, pNew)
	wantHTTPError(t, err, http.StatusForbidden, "not_allowed")
}

// Members and my clan show each member's real role in the game's ladder.
func TestMembersShowTheirRoleInTheGame(t *testing.T) {
	store := newReadingStore()
	store.members = []repository.Member{
		{PlayerID: pOwner, Role: "owner"},
		{PlayerID: pCo, Role: "co-leader"},
		{PlayerID: pElder, Role: "veteran"}, // a role the developer removed
		{PlayerID: pNew, Role: "member"},
	}
	store.myClan = repository.PlayerClan{Clan: repository.Clan{ID: someClan, Name: "Wolves", MemberCount: 4}, Role: "owner"}
	svc := New(store).WithRules(arenaRules{})
	got, err := svc.ListMembers(context.Background(), gameA, someClan)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"leader", "co-leader", "member", "member"}
	for i, m := range got {
		if m.Role != want[i] {
			t.Errorf("member %d: role %q, want %q", i, m.Role, want[i])
		}
	}
	mine, err := svc.MyClan(context.Background(), gameA, "u_owner")
	if err != nil || mine.Role != "leader" {
		t.Fatalf("my clan: %+v, %v; want role leader", mine, err)
	}
}

func TestClanMemberRoleChangedHasTheFieldsOfTheEventMessagesFile(t *testing.T) {
	store := newClanWithRoles()
	svc, rec := friendService(store)
	svc.WithRules(arenaRules{})
	if _, err := svc.PromoteMember(context.Background(), gameA, "u_owner", someClan, pNew); err != nil {
		t.Fatal(err)
	}
	want, ok := docShapes(t)[EventClanMemberRoleChanged]
	if !ok {
		t.Fatalf("%s is not in docs/event-messages.md", EventClanMemberRoleChanged)
	}
	if got := publishedShape(t, rec.Events()[0]); got != want {
		t.Errorf("fields of %s:\n  published %s\n  file      %s", EventClanMemberRoleChanged, got, want)
	}
}

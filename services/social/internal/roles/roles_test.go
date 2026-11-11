package roles

import (
	"errors"
	"testing"
)

// arena is the default roles of Studio: member, elder, co-leader, leader.
var arena = NewLadder([]Role{
	{ID: "member", Name: "Member"},
	{ID: "elder", Name: "Elder"},
	{ID: "co-leader", Name: "Co-leader", CanPromote: true, CanKick: true},
	{ID: "leader", Name: "Leader", CanPromote: true, CanKick: true},
})

var (
	owner     = Member{PlayerID: "p-owner", RoleID: LegacyOwner, IsOwner: true}
	coLeader  = Member{PlayerID: "p-co", RoleID: "co-leader"}
	coLeader2 = Member{PlayerID: "p-co2", RoleID: "co-leader"}
	elder     = Member{PlayerID: "p-elder", RoleID: "elder"}
	member    = Member{PlayerID: "p-member", RoleID: "member"}
)

func TestOf(t *testing.T) {
	cases := []struct {
		name string
		m    Member
		want string
	}{
		{"the owner has the last role, whatever is stored", owner, "leader"},
		{"the owner with a role id from an old config", Member{RoleID: "boss", IsOwner: true}, "leader"},
		{"a stored id", elder, "elder"},
		{"legacy member", Member{RoleID: LegacyMember}, "member"},
		{"an id no longer in the config is the first role", Member{RoleID: "veteran"}, "member"},
		{"empty", Member{}, "member"},
		{"a non-owner with the last role's id counts one below", Member{RoleID: "leader"}, "co-leader"},
		{"legacy owner text without being the owner", Member{RoleID: LegacyOwner}, "member"},
	}
	for _, c := range cases {
		if got := arena.Of(c.m).ID; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if arena.NewMemberRole() != "member" {
		t.Errorf("new members get %s, want member", arena.NewMemberRole())
	}
}

func TestDefaultLadderWithoutAConfig(t *testing.T) {
	for _, configured := range [][]Role{nil, {{ID: "solo"}}} {
		l := NewLadder(configured)
		if l.NewMemberRole() != LegacyMember || l.Of(owner).ID != LegacyOwner || l.Of(member).ID != LegacyMember {
			t.Fatalf("%v: want the default member/owner ladder", configured)
		}
		// as before roles: only the owner kicks, and there is nothing to promote to
		if !l.CanKick(owner, member) || l.CanKick(member, owner) {
			t.Fatal("default ladder: only the owner may kick")
		}
		if _, err := l.Promote(owner, member); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("default ladder promote: %v, want ErrNotAllowed (no role between member and owner)", err)
		}
	}
}

func TestCanKick(t *testing.T) {
	cases := []struct {
		name          string
		actor, target Member
		want          bool
	}{
		{"owner kicks anyone below", owner, coLeader, true},
		{"co-leader kicks an elder", coLeader, elder, true},
		{"co-leader kicks a member", coLeader, member, true},
		{"not an equal", coLeader, coLeader2, false},
		{"not someone higher", coLeader, owner, false},
		{"not themselves", coLeader, coLeader, false},
		{"owner not themselves", owner, owner, false},
		{"elder has no canKick", elder, member, false},
		{"member kicks nobody", member, member, false},
	}
	for _, c := range cases {
		if got := arena.CanKick(c.actor, c.target); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPromote(t *testing.T) {
	cases := []struct {
		name          string
		actor, target Member
		want          string // "" means ErrNotAllowed
	}{
		{"owner: member to elder", owner, member, "elder"},
		{"owner: elder to co-leader", owner, elder, "co-leader"},
		{"owner: never to owner", owner, coLeader, ""},
		{"co-leader: member to elder", coLeader, member, "elder"},
		{"co-leader: never to their own role", coLeader, elder, ""},
		{"co-leader: not an equal", coLeader, coLeader2, ""},
		{"co-leader: not the owner", coLeader, owner, ""},
		{"not themselves", coLeader, coLeader, ""},
		{"elder has no canPromote", elder, member, ""},
	}
	for _, c := range cases {
		got, err := arena.Promote(c.actor, c.target)
		switch {
		case c.want == "" && !errors.Is(err, ErrNotAllowed):
			t.Errorf("%s: got %v, %v; want ErrNotAllowed", c.name, got.ID, err)
		case c.want != "" && (err != nil || got.ID != c.want):
			t.Errorf("%s: got %v, %v; want %s", c.name, got.ID, err, c.want)
		}
	}
}

func TestDemote(t *testing.T) {
	cases := []struct {
		name          string
		actor, target Member
		want          string
		err           error
	}{
		{"owner: co-leader to elder", owner, coLeader, "elder", nil},
		{"owner: elder to member", owner, elder, "member", nil},
		{"owner: member is already lowest", owner, member, "", ErrAlreadyLowest},
		{"co-leader: elder to member", coLeader, elder, "member", nil},
		{"co-leader: not an equal", coLeader, coLeader2, "", ErrNotAllowed},
		{"co-leader: not the owner", coLeader, owner, "", ErrNotAllowed},
		{"elder has no canPromote", elder, member, "", ErrNotAllowed},
		{"an unknown id counts as lowest", owner, Member{PlayerID: "x", RoleID: "veteran"}, "", ErrAlreadyLowest},
	}
	for _, c := range cases {
		got, err := arena.Demote(c.actor, c.target)
		if !errors.Is(err, c.err) || got.ID != c.want {
			t.Errorf("%s: got %q, %v; want %q, %v", c.name, got.ID, err, c.want, c.err)
		}
	}
}

// The developer reorders or removes roles: nobody is removed and there is never a second owner.
func TestAConfigChangeKeepsEveryoneAndOneOwner(t *testing.T) {
	smaller := NewLadder([]Role{
		{ID: "member", Name: "Member"},
		{ID: "co-leader", Name: "Co-leader", CanPromote: true, CanKick: true},
	})
	// elder no longer exists: counts as member; co-leader is now the last id, but only the owner holds it
	if got := smaller.Of(elder).ID; got != "member" {
		t.Fatalf("removed role: %s, want member", got)
	}
	if got := smaller.Of(coLeader).ID; got != "member" {
		t.Fatalf("a non-owner holding the new last id: %s, want one below (member)", got)
	}
	if got := smaller.Of(owner).ID; got != "co-leader" {
		t.Fatalf("owner: %s, want the last role", got)
	}
	if smaller.CanKick(coLeader, elder) {
		t.Fatal("a former co-leader now ranks as member and may not kick an equal")
	}
}

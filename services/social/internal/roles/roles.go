// Package roles holds the clan role rules of config v1 (clan-frontend docs/config-v1/README.md,
// "Clan roles"). It only decides; it reads nothing and saves nothing.
//
// A game lists its roles lowest power first, 2 to 12 of them. The first is what a new member
// gets. The last is the owner's: exactly one holder, the clan's owner (clans.owner_id), never
// given by promoting, and it always has both powers. Power is the position in the list: a
// member acts only on someone lower, never on an equal or higher one, never on themselves.
package roles

import (
	"errors"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
)

// Role is one entry of clan.roles.
type Role = gameconfig.Role

// The ids Social stored before roles came from the config. They still mean the first role
// ("member") and the owner ("owner"), as the SDK already reads them.
const (
	LegacyMember = "member"
	LegacyOwner  = "owner"
)

// defaultRoles are used while a game has published no roles: the two Social always knew.
var defaultRoles = []Role{
	{ID: LegacyMember, Name: "Member"},
	{ID: LegacyOwner, Name: "Owner", CanPromote: true, CanKick: true},
}

var (
	// ErrNotAllowed means the actor may not do this to the target: no power for it, the
	// target is not lower, it is themselves, or a promote would reach the actor's own role.
	ErrNotAllowed = errors.New("not allowed")
	// ErrAlreadyLowest means a demote of someone already at the first role.
	ErrAlreadyLowest = errors.New("already the lowest role")
)

// Member is a clan member as the rules need to see them.
type Member struct {
	PlayerID string
	RoleID   string // as stored in clan_members.role
	IsOwner  bool   // the clan's owner (clans.owner_id); always the last role
}

// Ladder is one game's roles, lowest power first.
type Ladder struct {
	roles []Role
}

// NewLadder returns the ladder of a game's configured roles; fewer than two (no published
// config) gives the default two, member and owner.
func NewLadder(configured []Role) Ladder {
	if len(configured) < 2 {
		return Ladder{roles: defaultRoles}
	}
	return Ladder{roles: configured}
}

// NewMemberRole is the role id a player gets when they join a clan.
func (l Ladder) NewMemberRole() string { return l.roles[0].ID }

// Of is the role a member really has in this ladder:
//   - the owner always has the last role, whatever id is stored;
//   - an id that is not in the list (renamed or removed in Studio, or "member") is the first role;
//   - nobody but the owner holds the last role: a stored id that is now the last counts as one below.
//
// So a config change never removes anyone and never makes a second owner.
func (l Ladder) Of(m Member) Role {
	return l.roles[l.rank(m)]
}

func (l Ladder) rank(m Member) int {
	last := len(l.roles) - 1
	if m.IsOwner {
		return last
	}
	for i, r := range l.roles {
		if r.ID == m.RoleID {
			return min(i, last-1)
		}
	}
	return 0
}

// can reports whether actor has power over target: someone else, lower than the actor, and
// the actor's role has the power (the owner always has it).
func (l Ladder) can(actor, target Member, power func(Role) bool) bool {
	if actor.PlayerID == target.PlayerID {
		return false
	}
	a := l.rank(actor)
	if l.rank(target) >= a {
		return false
	}
	return actor.IsOwner || power(l.roles[a])
}

// CanKick reports whether actor may remove target from the clan.
func (l Ladder) CanKick(actor, target Member) bool {
	return l.can(actor, target, func(r Role) bool { return r.CanKick })
}

// CanHandleRequests reports whether the member may accept and decline the requests to join their clan, and
// see them: the owner always may, and so may a role that may remove members (canKick). A request is a
// decision about who belongs to the clan, the same kind of power as removing someone.
func (l Ladder) CanHandleRequests(m Member) bool {
	return m.IsOwner || l.Of(m).CanKick
}

// Promote returns the role target gets one step up. The new role must stay below the actor's
// own, so nobody can make someone their equal, and nobody is promoted to owner.
func (l Ladder) Promote(actor, target Member) (Role, error) {
	if !l.can(actor, target, func(r Role) bool { return r.CanPromote }) {
		return Role{}, ErrNotAllowed
	}
	up := l.rank(target) + 1
	if up >= l.rank(actor) {
		return Role{}, ErrNotAllowed
	}
	return l.roles[up], nil
}

// Demote returns the role target gets one step down.
func (l Ladder) Demote(actor, target Member) (Role, error) {
	if !l.can(actor, target, func(r Role) bool { return r.CanPromote }) {
		return Role{}, ErrNotAllowed
	}
	t := l.rank(target)
	if t == 0 {
		return Role{}, ErrAlreadyLowest
	}
	return l.roles[t-1], nil
}

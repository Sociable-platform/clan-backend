package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/roles"
)

// EventClanMemberRoleChanged is published after a promote or demote:
// {clan, player, role, previous_role, changed_by} (docs/event-messages.md). role and
// previous_role are role ids of the game's clan.roles.
const EventClanMemberRoleChanged = "clan.member_role_changed"

// clanRules returns the game's clan rules and its role ladder (default member/owner while the
// game has published no roles). One gameconfig call, kept for its TTL.
func (s *Service) clanRules(ctx context.Context, gameID string) (gameconfig.Rules, roles.Ladder) {
	rules := s.rules.Rules(ctx, gameID)
	return rules, roles.NewLadder(rules.Roles)
}

// storedRole is a member as only their stored role tells: the owner's row always holds
// "owner" (the repository never gives that mark to anyone else).
func storedRole(role string) roles.Member {
	return roles.Member{RoleID: role, IsOwner: role == roles.LegacyOwner}
}

// toRoleMember is a member read inside the clan lock, for the role rules.
func toRoleMember(m repository.MemberRole) roles.Member {
	return roles.Member{PlayerID: m.Player.ID, RoleID: m.RoleID, IsOwner: m.IsOwner}
}

func errNotAllowed() error {
	return httpx.NewError(http.StatusForbidden, "not_allowed", "Your clan role does not allow this")
}

// MemberRole is a member's role after a promote or demote.
type MemberRole struct {
	PlayerID    string
	DisplayName string
	Role        string // the new role id
}

// PromoteMember moves player playerID of clan clanID one role up. See changeMemberRole.
func (s *Service) PromoteMember(ctx context.Context, gameID, externalID, clanID, playerID string) (MemberRole, error) {
	return s.changeMemberRole(ctx, gameID, externalID, clanID, playerID, roles.Ladder.Promote)
}

// DemoteMember moves player playerID of clan clanID one role down. See changeMemberRole.
func (s *Service) DemoteMember(ctx context.Context, gameID, externalID, clanID, playerID string) (MemberRole, error) {
	return s.changeMemberRole(ctx, gameID, externalID, clanID, playerID, roles.Ladder.Demote)
}

// changeMemberRole is promote and demote: the game's role ladder decides inside the clan lock
// (package roles: the caller needs canPromote, the player must be lower, and a promote stays
// below the caller's own role), the repository saves it, and clan.member_role_changed is
// published after the commit.
//
// Errors the client can act on:
//   - 404 clan_not_found: no such clan in the caller's game
//   - 404 member_not_found: the player is not a member of this clan
//   - 403 not_allowed: the caller is not in the clan, names themselves, or their role does
//     not allow this step
//   - 409 already_lowest_role: a demote of someone at the first role
func (s *Service) changeMemberRole(ctx context.Context, gameID, externalID, clanID, playerID string,
	step func(roles.Ladder, roles.Member, roles.Member) (roles.Role, error)) (MemberRole, error) {
	if !uuidPattern.MatchString(clanID) {
		return MemberRole{}, errClanNotFound()
	}
	if !uuidPattern.MatchString(playerID) {
		return MemberRole{}, errMemberNotFound()
	}
	_, ladder := s.clanRules(ctx, gameID)
	var previous string // the role the player really had, as the ladder reads it
	change, err := s.store.ChangeRole(ctx, gameID, strings.ToLower(clanID), externalID, strings.ToLower(playerID),
		func(actor, target repository.MemberRole) (string, error) {
			t := toRoleMember(target)
			previous = ladder.Of(t).ID
			next, err := step(ladder, toRoleMember(actor), t)
			return next.ID, err
		})
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return MemberRole{}, errClanNotFound()
	case errors.Is(err, repository.ErrMemberNotFound):
		return MemberRole{}, errMemberNotFound()
	case errors.Is(err, repository.ErrNotAllowed), errors.Is(err, roles.ErrNotAllowed):
		return MemberRole{}, errNotAllowed()
	case errors.Is(err, roles.ErrAlreadyLowest):
		return MemberRole{}, httpx.NewError(http.StatusConflict, "already_lowest_role", "This member already has the lowest role")
	case err != nil:
		return MemberRole{}, fmt.Errorf("change role: %w", err)
	}

	_ = s.events.Publish(ctx, events.New(EventClanMemberRoleChanged, gameID, map[string]any{
		"clan":          ref(change.Clan.ID, change.Clan.Name),
		"player":        ref(change.Player.ID, change.Player.DisplayName),
		"role":          change.NewRoleID,
		"previous_role": previous,
		"changed_by":    ref(change.ChangedBy.ID, change.ChangedBy.DisplayName),
	}))
	return MemberRole{PlayerID: change.Player.ID, DisplayName: change.Player.DisplayName, Role: change.NewRoleID}, nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

func errMemberNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "member_not_found", "This developer is not on the game")
}

func errLastOwner() *httpx.Error {
	return httpx.NewError(http.StatusConflict, "last_owner", "A game needs at least one owner. Make another developer an owner first.")
}

func errAlreadyOwner() *httpx.Error {
	return httpx.NewError(http.StatusConflict, "already_owner", "This developer is already an owner of the game")
}

func errInvalidRole() *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, "invalid_role", `role must be "owner" or "member"`)
}

// teamError turns what the repository says about a change to the team into the answer for the client.
func teamError(err error) error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return errMemberNotFound()
	case errors.Is(err, repository.ErrLastOwner):
		return errLastOwner()
	case errors.Is(err, repository.ErrAlreadyOwner):
		return errAlreadyOwner()
	case errors.Is(err, repository.ErrOwnerRequired):
		return errOwnerRequired()
	}
	return err
}

// changeTeam runs a change to the team of a game and announces it when it changes whether the game is served.
// A game is served while its own switch is on and at least one owner has an active account (see GameAccessOf), so
// an owner leaving, being removed or demoted can turn a game off (the owners that are left are all suspended), and
// promoting somebody can turn it on again. The admin panel announces the same change when it suspends a developer;
// this does it for the team changes, in the same transaction, so the other services never serve a game they should
// not, nor refuse one they should serve. A game that is suspended on its own changes nothing for them.
func (s *Service) changeTeam(ctx context.Context, gameID string, fn func(ctx context.Context) error) error {
	return s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		before, err := s.store.GameAccessOf(ctx, gameID)
		if err != nil {
			return nil, fmt.Errorf("check access of game %s: %w", gameID, err)
		}
		if err := fn(ctx); err != nil {
			return nil, err
		}
		after, err := s.store.GameAccessOf(ctx, gameID)
		if err != nil {
			return nil, fmt.Errorf("check access of game %s: %w", gameID, err)
		}
		wasOn, isOn := before.DeveloperStatus == StatusActive, after.DeveloperStatus == StatusActive
		if before.GameStatus != StatusActive || wasOn == isOn {
			return nil, nil
		}
		return []events.Event{gameStatusEvent(gameID, after.DeveloperStatus)}, nil
	})
}

// RemoveGameMember takes a developer off the game, for an owner of it (a member gets 403 owner_required). Their access
// stops at once: the next request they make for the game is a 404. Removing an owner is allowed while another owner is
// left; the last owner is 409 last_owner. A developer who is not on the game is 404 member_not_found.
func (s *Service) RemoveGameMember(ctx context.Context, developerID, gameID, memberID string) error {
	if _, err := s.OwnerGame(ctx, developerID, gameID); err != nil {
		return err
	}
	if !uuidPattern.MatchString(memberID) {
		return errMemberNotFound()
	}
	// An owner removing themselves is leaving, and is recorded as that.
	action := repository.TeamMemberRemoved
	if memberID == developerID {
		action = repository.TeamMemberLeft
	}
	err := s.changeTeam(ctx, gameID, func(ctx context.Context) error {
		if err := s.store.RemoveGameMember(ctx, gameID, memberID); err != nil {
			return err
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: action, ActorID: developerID, SubjectID: memberID})
	})
	if err != nil {
		return wrapTeam(teamError(err), "remove member %s of game %s", memberID, gameID)
	}
	return nil
}

// LeaveGame takes the logged-in developer off the game, whatever their role. An owner can leave only while another
// owner is left (409 last_owner otherwise: hand the game over first).
func (s *Service) LeaveGame(ctx context.Context, developerID, gameID string) error {
	if _, err := s.MemberGame(ctx, developerID, gameID); err != nil {
		return err
	}
	err := s.changeTeam(ctx, gameID, func(ctx context.Context) error {
		if err := s.store.RemoveGameMember(ctx, gameID, developerID); err != nil {
			return err
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamMemberLeft, ActorID: developerID, SubjectID: developerID})
	})
	if errors.Is(err, repository.ErrNotFound) {
		return errGameNotFound() // they were removed a moment ago
	}
	if err != nil {
		return wrapTeam(teamError(err), "leave game %s", gameID)
	}
	return nil
}

// SetMemberRole makes a developer on the game an owner or a member, for an owner of it. Demoting the last owner is 409
// last_owner. A role they have already is fine and changes nothing. Somebody who is not on the game is 404
// member_not_found; a role that is neither "owner" nor "member" is 400 invalid_role.
func (s *Service) SetMemberRole(ctx context.Context, developerID, gameID, memberID, role string) (GameMember, error) {
	if _, err := s.OwnerGame(ctx, developerID, gameID); err != nil {
		return GameMember{}, err
	}
	if role != repository.RoleOwner && role != repository.RoleMember {
		return GameMember{}, errInvalidRole()
	}
	if !uuidPattern.MatchString(memberID) {
		return GameMember{}, errMemberNotFound()
	}
	var m repository.GameMember
	err := s.changeTeam(ctx, gameID, func(ctx context.Context) error {
		var changed bool
		var err error
		m, changed, err = s.store.SetGameMemberRole(ctx, gameID, memberID, role)
		if err != nil || !changed {
			return err // a role they have already is not a change, and is not recorded
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamRoleChanged, ActorID: developerID, SubjectID: memberID, Role: role})
	})
	if err != nil {
		return GameMember{}, wrapTeam(teamError(err), "set role of %s on game %s", memberID, gameID)
	}
	return toGameMember(m), nil
}

// TransferOwnership hands the game from the logged-in owner to another member in one step: they become an owner and
// the caller a member, so the game never has fewer owners. It returns the game as the caller now sees it (role member).
// The target has to be on the game (404 member_not_found) and not an owner already (409 already_owner).
func (s *Service) TransferOwnership(ctx context.Context, developerID, gameID, toID string) (Game, error) {
	if _, err := s.OwnerGame(ctx, developerID, gameID); err != nil {
		return Game{}, err
	}
	if !uuidPattern.MatchString(toID) {
		return Game{}, errMemberNotFound()
	}
	err := s.changeTeam(ctx, gameID, func(ctx context.Context) error {
		if err := s.store.TransferGameOwnership(ctx, gameID, developerID, toID); err != nil {
			return err
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamOwnershipTransferred, ActorID: developerID, SubjectID: toID})
	})
	if err != nil {
		return Game{}, wrapTeam(teamError(err), "transfer game %s to %s", gameID, toID)
	}
	return s.MemberGame(ctx, developerID, gameID)
}

// wrapTeam returns err unchanged when it is an answer for the client, and otherwise wraps it with what was being done.
func wrapTeam(err error, format string, args ...any) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

func toGameMember(m repository.GameMember) GameMember {
	return GameMember{DeveloperID: m.DeveloperID, Name: m.Name, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt}
}

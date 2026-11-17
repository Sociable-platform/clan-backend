package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// How many pending requests there may be: a clan cannot be flooded, and a player cannot ask everybody.
const (
	maxRequestsPerClan   = 50
	maxRequestsPerPlayer = 5
)

// JoinRequest is a player's pending request to join a closed clan.
type JoinRequest struct {
	ID          string
	PlayerID    string // social's own player ID
	DisplayName string
	CreatedAt   time.Time
}

func errClanClosed() error {
	return httpx.NewError(http.StatusForbidden, "clan_closed", "This clan is closed; ask to join instead")
}

func errRequestNotFound() error {
	return httpx.NewError(http.StatusNotFound, "request_not_found", "This join request does not exist any more")
}

// joinRequestError turns the errors of the request methods into the answers a client can act on; the
// ones it does not know come back unchanged.
func joinRequestError(err error) error {
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return errClanNotFound()
	case errors.Is(err, repository.ErrNotAllowed):
		return errNotAllowed()
	case errors.Is(err, repository.ErrRequestNotFound):
		return errRequestNotFound()
	case errors.Is(err, repository.ErrClanOpen):
		return httpx.NewError(http.StatusConflict, "clan_open", "This clan is open; join it directly")
	case errors.Is(err, repository.ErrAlreadyInClan):
		return httpx.NewError(http.StatusConflict, "already_in_clan", "You are already in a clan; leave it first")
	case errors.Is(err, repository.ErrAlreadyRequested):
		return httpx.NewError(http.StatusConflict, "already_requested", "You already asked to join this clan")
	case errors.Is(err, repository.ErrClanFull):
		return httpx.NewError(http.StatusConflict, "clan_full", "This clan has no free places")
	case errors.Is(err, repository.ErrClanRequestsFull):
		return httpx.NewError(http.StatusConflict, "clan_requests_full", "This clan has too many open requests; try again later")
	case errors.Is(err, repository.ErrTooManyRequests):
		return httpx.NewError(http.StatusConflict, "too_many_requests", "You have too many open requests; take one back first")
	}
	return err
}

// canHandle is the clan-role rule of "may decide on requests" for the repository: the owner, or a role
// with canHandleRequests (canKick when the config does not say; see roles.Ladder.CanHandleRequests).
func (s *Service) canHandle(ctx context.Context, gameID string) func(repository.MemberRole) bool {
	_, ladder := s.clanRules(ctx, gameID)
	return func(m repository.MemberRole) bool { return ladder.CanHandleRequests(toRoleMember(m)) }
}

// RequestJoin sends the calling player's request to join the closed clan clanID. A leader of the clan
// (its owner, or a role with canHandleRequests) accepts or declines it. The player record is created
// first if this is the player's first request.
//
// Errors the client can act on:
//   - 404 clan_not_found: no such clan in the player's game
//   - 409 clan_open: the clan is open; join it directly
//   - 409 already_in_clan, already_requested, clan_full
//   - 409 clan_requests_full: the clan has 50 pending requests; 409 too_many_requests: the player has 5
func (s *Service) RequestJoin(ctx context.Context, gameID, externalID, displayName, clanID string) (JoinRequest, error) {
	if !uuidPattern.MatchString(clanID) {
		return JoinRequest{}, errClanNotFound()
	}
	player, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return JoinRequest{}, fmt.Errorf("request join: %w", err)
	}
	rules, ladder := s.clanRules(ctx, gameID)

	var res repository.JoinRequestResult
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		var err error
		res, err = s.store.RequestJoin(ctx, gameID, strings.ToLower(clanID), player.ID, rules.MaxMembers, maxRequestsPerClan, maxRequestsPerPlayer)
		if err != nil {
			return nil, err
		}
		// The leaders are named in the event, so Realtime can tell exactly them; it does not know the roles.
		managers := make([]string, 0, len(res.Members))
		for _, m := range res.Members {
			if ladder.CanHandleRequests(toRoleMember(m)) {
				managers = append(managers, m.Player.ID)
			}
		}
		return []events.Event{events.New(EventClanJoinRequested, gameID, map[string]any{
			"clan":       ref(res.Clan.ID, res.Clan.Name),
			"player":     ref(player.ID, player.DisplayName),
			"request_id": res.Request.ID,
			"managers":   managers,
		})}, nil
	})
	if err != nil {
		if mapped := joinRequestError(err); mapped != err {
			return JoinRequest{}, mapped
		}
		return JoinRequest{}, fmt.Errorf("request join: %w", err)
	}
	return toJoinRequest(res.Request), nil
}

// CancelJoinRequest takes back the calling player's request to join clan clanID. It writes no event:
// the leaders' list is right the next time they open it, and accepting a request that is gone is a 404.
//
// Errors: 404 clan_not_found, 404 request_not_found.
func (s *Service) CancelJoinRequest(ctx context.Context, gameID, externalID, clanID string) error {
	if !uuidPattern.MatchString(clanID) {
		return errClanNotFound()
	}
	if err := s.store.CancelJoinRequest(ctx, gameID, strings.ToLower(clanID), externalID); err != nil {
		if mapped := joinRequestError(err); mapped != err {
			return mapped
		}
		return fmt.Errorf("cancel join request: %w", err)
	}
	return nil
}

// ListJoinRequests returns the pending requests of clan clanID, oldest first, to a leader of that clan.
//
// Errors: 404 clan_not_found, 403 not_allowed (the caller is not in the clan, or their role may not decide).
func (s *Service) ListJoinRequests(ctx context.Context, gameID, externalID, clanID string) ([]JoinRequest, error) {
	if !uuidPattern.MatchString(clanID) {
		return nil, errClanNotFound()
	}
	rows, err := s.store.ListJoinRequests(ctx, gameID, strings.ToLower(clanID), externalID, s.canHandle(ctx, gameID))
	if err != nil {
		if mapped := joinRequestError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("list join requests: %w", err)
	}
	out := make([]JoinRequest, 0, len(rows))
	for _, q := range rows {
		out = append(out, toJoinRequest(q))
	}
	return out, nil
}

// AcceptJoinRequest lets a leader accept request requestID: the player becomes a member (with the game's
// first role) and Realtime hears clan.member_joined, so they get the clan chat and the others a join line.
//
// Errors: 404 clan_not_found, 403 not_allowed, 404 request_not_found, 409 clan_full (the request stays),
// 409 already_in_clan (the player joined another clan meanwhile; the request is gone).
func (s *Service) AcceptJoinRequest(ctx context.Context, gameID, externalID, clanID, requestID string) error {
	if !uuidPattern.MatchString(clanID) {
		return errClanNotFound()
	}
	if !uuidPattern.MatchString(requestID) {
		return errRequestNotFound()
	}
	rules, ladder := s.clanRules(ctx, gameID)
	err := s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		d, err := s.store.AcceptJoinRequest(ctx, gameID, strings.ToLower(clanID), strings.ToLower(requestID), externalID,
			rules.MaxMembers, ladder.NewMemberRole(), s.canHandle(ctx, gameID))
		if err != nil {
			return nil, err
		}
		return []events.Event{events.New(EventClanMemberJoined, gameID, map[string]any{
			"clan":   ref(d.Clan.ID, d.Clan.Name),
			"player": ref(d.Player.ID, d.Player.DisplayName),
		})}, nil
	})
	if err != nil {
		if mapped := joinRequestError(err); mapped != err {
			return mapped
		}
		return fmt.Errorf("accept join request: %w", err)
	}
	return nil
}

// DeclineJoinRequest lets a leader decline request requestID. The player is told (clan.join_request_declined)
// and may ask again.
//
// Errors: 404 clan_not_found, 403 not_allowed, 404 request_not_found.
func (s *Service) DeclineJoinRequest(ctx context.Context, gameID, externalID, clanID, requestID string) error {
	if !uuidPattern.MatchString(clanID) {
		return errClanNotFound()
	}
	if !uuidPattern.MatchString(requestID) {
		return errRequestNotFound()
	}
	err := s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		d, err := s.store.DeclineJoinRequest(ctx, gameID, strings.ToLower(clanID), strings.ToLower(requestID), externalID, s.canHandle(ctx, gameID))
		if err != nil {
			return nil, err
		}
		return []events.Event{events.New(EventClanJoinRequestDeclined, gameID, map[string]any{
			"clan":        ref(d.Clan.ID, d.Clan.Name),
			"player":      ref(d.Player.ID, d.Player.DisplayName),
			"declined_by": ref(d.By.ID, d.By.DisplayName),
		})}, nil
	})
	if err != nil {
		if mapped := joinRequestError(err); mapped != err {
			return mapped
		}
		return fmt.Errorf("decline join request: %w", err)
	}
	return nil
}

// SetJoinPolicy opens or closes clan clanID; only its owner may.
//
// Errors: 400 invalid_join_policy, 404 clan_not_found, 403 not_clan_owner.
func (s *Service) SetJoinPolicy(ctx context.Context, gameID, externalID, clanID, policy string) (Clan, error) {
	clean, err := cleanJoinPolicy(policy)
	if err != nil {
		return Clan{}, err
	}
	if !uuidPattern.MatchString(clanID) {
		return Clan{}, errClanNotFound()
	}
	row, err := s.store.SetJoinPolicy(ctx, gameID, strings.ToLower(clanID), externalID, clean)
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return Clan{}, errClanNotFound()
	case errors.Is(err, repository.ErrNotClanOwner):
		return Clan{}, httpx.NewError(http.StatusForbidden, "not_clan_owner", "Only the clan owner can open or close the clan")
	case err != nil:
		return Clan{}, fmt.Errorf("set join policy: %w", err)
	}
	return toClan(row, s.maxMembers(ctx, gameID)), nil
}

func toJoinRequest(q repository.JoinRequest) JoinRequest {
	return JoinRequest{ID: q.ID, PlayerID: q.Player.ID, DisplayName: q.Player.DisplayName, CreatedAt: q.CreatedAt}
}

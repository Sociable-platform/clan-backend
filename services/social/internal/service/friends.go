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

// Event types published by the friends feature (routing keys on events.Exchange).
const (
	EventFriendRequestSent      = "friend.request_sent"
	EventFriendRequestCancelled = "friend.request_cancelled"
	EventFriendRequestDeclined  = "friend.request_declined"
	EventFriendAccepted         = "friend.accepted"
	EventFriendRemoved          = "friend.removed"
)

// FriendPlayer is another player as the friends feature shows them.
type FriendPlayer struct {
	ID          string
	DisplayName string
}

// FriendRequest is a pending request, seen from the calling player: Player is the other one.
type FriendRequest struct {
	ID        string
	Player    FriendPlayer
	CreatedAt time.Time
}

// FriendRequests are the calling player's pending requests, newest first.
type FriendRequests struct {
	Incoming []FriendRequest // sent to the player, who can accept them
	Outgoing []FriendRequest // sent by the player, waiting for the other one
}

// Friend is one of the calling player's friends.
type Friend struct {
	FriendPlayer
	Since time.Time
}

// SendFriendRequestResult tells what SendFriendRequest did. Exactly one field is set.
type SendFriendRequestResult struct {
	Request *FriendRequest // a new pending request (201)
	Friend  *Friend        // the other player had already asked, so they are friends now (200)
}

// SendFriendRequest sends a friend request from the calling player to playerID, a player of
// the same game. If playerID had already sent the caller a request, that request is accepted
// instead and the two become friends. The caller's player record is created first if needed.
//
// It writes friend.request_sent or friend.accepted to the outbox in the same transaction.
//
// Errors the client can act on:
//   - 400 invalid_player_id: playerID is empty
//   - 400 cannot_friend_yourself: playerID is the caller
//   - 404 player_not_found: no such player in the caller's game (a player of another game,
//     or an ID that is not a UUID, counts as not found)
//   - 409 player_blocked: the caller blocked this player (unblock first). A player who
//     blocked the caller answers 404 player_not_found, so the block stays hidden.
//   - 409 already_friends: the two are friends already
//   - 409 friend_request_exists: the caller already sent this player a request
func (s *Service) SendFriendRequest(ctx context.Context, gameID, externalID, displayName, playerID string) (SendFriendRequestResult, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return SendFriendRequestResult{}, httpx.NewError(http.StatusBadRequest, "invalid_player_id", "playerId is required")
	}
	if !uuidPattern.MatchString(playerID) {
		return SendFriendRequestResult{}, errPlayerNotFound()
	}

	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return SendFriendRequestResult{}, fmt.Errorf("send friend request: %w", err)
	}

	var row repository.SendFriendRequestResult
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		var err error
		if row, err = s.store.SendFriendRequest(ctx, gameID, me.ID, strings.ToLower(playerID)); err != nil {
			return nil, err
		}
		if row.Accepted != nil {
			return []events.Event{friendAccepted(gameID, *row.Accepted)}, nil
		}
		return []events.Event{events.New(EventFriendRequestSent, gameID, requestData(*row.Request))}, nil
	})
	switch {
	case errors.Is(err, repository.ErrFriendRequestToSelf):
		return SendFriendRequestResult{}, httpx.NewError(http.StatusBadRequest, "cannot_friend_yourself",
			"You cannot send a friend request to yourself")
	case errors.Is(err, repository.ErrPlayerNotFound):
		return SendFriendRequestResult{}, errPlayerNotFound()
	case errors.Is(err, repository.ErrPlayerBlocked):
		return SendFriendRequestResult{}, httpx.NewError(http.StatusConflict, "player_blocked",
			"You blocked this player; unblock them first")
	case errors.Is(err, repository.ErrAlreadyFriends):
		return SendFriendRequestResult{}, httpx.NewError(http.StatusConflict, "already_friends", "You are already friends")
	case errors.Is(err, repository.ErrFriendRequestExists):
		return SendFriendRequestResult{}, httpx.NewError(http.StatusConflict, "friend_request_exists",
			"You already sent this player a friend request")
	case err != nil:
		return SendFriendRequestResult{}, fmt.Errorf("send friend request: %w", err)
	}

	if row.Accepted != nil {
		friend := Friend{FriendPlayer: otherOf(*row.Accepted, me.ID), Since: row.Accepted.CreatedAt}
		return SendFriendRequestResult{Friend: &friend}, nil
	}
	req := row.Request
	out := FriendRequest{ID: req.ID, Player: FriendPlayer(req.Receiver), CreatedAt: req.CreatedAt}
	return SendFriendRequestResult{Request: &out}, nil
}

// AcceptFriendRequest accepts request requestID, which must have been sent to the calling
// player, and returns the new friend. It writes friend.accepted to the outbox in the same transaction.
//
// Errors the client can act on:
//   - 404 friend_request_not_found: no such pending request for this player (another game's
//     request, one between two other players, or an ID that is not a UUID count as not found)
//   - 403 not_request_receiver: the caller sent this request; only the other player can accept it
func (s *Service) AcceptFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) (Friend, error) {
	if !uuidPattern.MatchString(requestID) {
		return Friend{}, errFriendRequestNotFound()
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return Friend{}, fmt.Errorf("accept friend request: %w", err)
	}

	var f repository.Friendship
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		var err error
		if f, err = s.store.AcceptFriendRequest(ctx, gameID, strings.ToLower(requestID), me.ID); err != nil {
			return nil, err
		}
		return []events.Event{friendAccepted(gameID, f)}, nil
	})
	switch {
	case errors.Is(err, repository.ErrFriendRequestNotFound):
		return Friend{}, errFriendRequestNotFound()
	case errors.Is(err, repository.ErrNotFriendRequestReceiver):
		return Friend{}, httpx.NewError(http.StatusForbidden, "not_request_receiver",
			"Only the player who received this request can accept it")
	case err != nil:
		return Friend{}, fmt.Errorf("accept friend request: %w", err)
	}

	return Friend{FriendPlayer: otherOf(f, me.ID), Since: f.CreatedAt}, nil
}

// ListFriendRequests returns the calling player's pending requests, split into incoming and
// outgoing, newest first. Both lists are empty, never nil, when there are none.
func (s *Service) ListFriendRequests(ctx context.Context, gameID, externalID, displayName string) (FriendRequests, error) {
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return FriendRequests{}, fmt.Errorf("list friend requests: %w", err)
	}
	rows, err := s.store.ListFriendRequests(ctx, gameID, me.ID)
	if err != nil {
		return FriendRequests{}, fmt.Errorf("list friend requests: %w", err)
	}
	out := FriendRequests{Incoming: []FriendRequest{}, Outgoing: []FriendRequest{}}
	for _, r := range rows {
		if r.Receiver.ID == me.ID {
			out.Incoming = append(out.Incoming, FriendRequest{ID: r.ID, Player: FriendPlayer(r.Sender), CreatedAt: r.CreatedAt})
		} else {
			out.Outgoing = append(out.Outgoing, FriendRequest{ID: r.ID, Player: FriendPlayer(r.Receiver), CreatedAt: r.CreatedAt})
		}
	}
	return out, nil
}

// ListFriends returns the calling player's friends, sorted by name. The list is empty, never
// nil, when there are none.
func (s *Service) ListFriends(ctx context.Context, gameID, externalID, displayName string) ([]Friend, error) {
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	rows, err := s.store.ListFriends(ctx, gameID, me.ID)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	friends := make([]Friend, 0, len(rows))
	for _, f := range rows {
		friends = append(friends, Friend{FriendPlayer: FriendPlayer(f.FriendPlayer), Since: f.Since})
	}
	return friends, nil
}

// friendAccepted is the event that tells the other services that the two players are friends now.
func friendAccepted(gameID string, f repository.Friendship) events.Event {
	return events.New(EventFriendAccepted, gameID, map[string]any{
		"request_id": f.RequestID, // the request that was accepted, so every tab can drop its notification
		"players":    []map[string]any{playerData(f.PlayerA), playerData(f.PlayerB)},
	})
}

// friendRemoved is the event of an ended friendship, and who ended it.
func friendRemoved(gameID string, f repository.Friendship, removedBy string) events.Event {
	return events.New(EventFriendRemoved, gameID, map[string]any{
		"players":    []map[string]any{playerData(f.PlayerA), playerData(f.PlayerB)},
		"removed_by": removedBy,
	})
}

// requestData is the data of the three events about a friend request (docs/event-messages.md).
// The request ID lets the notification accept, or drop, this very request.
func requestData(req repository.FriendRequest) map[string]any {
	return map[string]any{
		"request_id": req.ID,
		"sender":     playerData(req.Sender),
		"receiver":   playerData(req.Receiver),
	}
}

// playerData is how a player appears in event data: social's own ID and the display name.
func playerData(p repository.FriendPlayer) map[string]any {
	return map[string]any{"id": p.ID, "name": p.DisplayName}
}

// otherOf returns the player of f who is not playerID.
func otherOf(f repository.Friendship, playerID string) FriendPlayer {
	if f.PlayerA.ID == playerID {
		return FriendPlayer(f.PlayerB)
	}
	return FriendPlayer(f.PlayerA)
}

func errPlayerNotFound() error {
	return httpx.NewError(http.StatusNotFound, "player_not_found", "Player not found")
}

func errFriendRequestNotFound() error {
	return httpx.NewError(http.StatusNotFound, "friend_request_not_found", "Friend request not found")
}

// DeclineFriendRequest deletes request requestID, which must have been sent to the calling
// player. It writes friend.request_declined to the outbox in the same transaction; Realtime tells
// only the receiver's own tabs, so the sender is not told.
//
// Errors the client can act on:
//   - 404 friend_request_not_found: no such pending request for this player
//   - 403 not_request_receiver: the caller sent this request (they can cancel it instead)
func (s *Service) DeclineFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) error {
	if !uuidPattern.MatchString(requestID) {
		return errFriendRequestNotFound()
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return fmt.Errorf("decline friend request: %w", err)
	}
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		req, err := s.store.DeclineFriendRequest(ctx, gameID, strings.ToLower(requestID), me.ID)
		if err != nil {
			return nil, err
		}
		return []events.Event{events.New(EventFriendRequestDeclined, gameID, requestData(req))}, nil
	})
	switch {
	case errors.Is(err, repository.ErrFriendRequestNotFound):
		return errFriendRequestNotFound()
	case errors.Is(err, repository.ErrNotFriendRequestReceiver):
		return httpx.NewError(http.StatusForbidden, "not_request_receiver",
			"Only the player who received this request can decline it")
	case err != nil:
		return fmt.Errorf("decline friend request: %w", err)
	}
	return nil
}

// CancelFriendRequest deletes request requestID, which the calling player must have sent.
// It writes friend.request_cancelled to the outbox in the same transaction.
//
// Errors the client can act on:
//   - 404 friend_request_not_found: no such pending request for this player
//   - 403 not_request_sender: the caller received this request (they can decline it instead)
func (s *Service) CancelFriendRequest(ctx context.Context, gameID, externalID, displayName, requestID string) error {
	if !uuidPattern.MatchString(requestID) {
		return errFriendRequestNotFound()
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return fmt.Errorf("cancel friend request: %w", err)
	}
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		req, err := s.store.CancelFriendRequest(ctx, gameID, strings.ToLower(requestID), me.ID)
		if err != nil {
			return nil, err
		}
		return []events.Event{events.New(EventFriendRequestCancelled, gameID, requestData(req))}, nil
	})
	switch {
	case errors.Is(err, repository.ErrFriendRequestNotFound):
		return errFriendRequestNotFound()
	case errors.Is(err, repository.ErrNotFriendRequestSender):
		return httpx.NewError(http.StatusForbidden, "not_request_sender",
			"Only the player who sent this request can cancel it")
	case err != nil:
		return fmt.Errorf("cancel friend request: %w", err)
	}
	return nil
}

// RemoveFriend ends the calling player's friendship with playerID, for both of them. Afterwards
// either may send a new request. It writes friend.removed to the outbox in the same transaction.
//
// Errors the client can act on:
//   - 404 not_friends: the two are not friends (also for an unknown player, a player of another
//     game, or an ID that is not a UUID)
func (s *Service) RemoveFriend(ctx context.Context, gameID, externalID, displayName, playerID string) error {
	if !uuidPattern.MatchString(playerID) {
		return errNotFriends()
	}
	me, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return fmt.Errorf("remove friend: %w", err)
	}
	err = s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		f, err := s.store.RemoveFriend(ctx, gameID, me.ID, strings.ToLower(playerID))
		if err != nil {
			return nil, err
		}
		return []events.Event{friendRemoved(gameID, f, me.ID)}, nil
	})
	switch {
	case errors.Is(err, repository.ErrNotFriends):
		return errNotFriends()
	case err != nil:
		return fmt.Errorf("remove friend: %w", err)
	}
	return nil
}

func errNotFriends() error {
	return httpx.NewError(http.StatusNotFound, "not_friends", "You are not friends with this player")
}

package service

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// DeletedPlayerName is the name a deleted player keeps in the places other services still show
// them, such as old chat messages.
const DeletedPlayerName = "Deleted player"

// DeletePlayer deletes the calling player's social profile; the game calls it when it deletes
// the player's account (TEAM-249). The player leaves their clan as if they had left it, their
// friendships, friend requests and blocks are removed, and they are renamed DeletedPlayerName
// and never found by the player search again. Their tokens issued before now stop working
// (PlayerDeletedSince).
//
// Without a live profile (a player social never saw) only a deleted record is written, so the
// tokens issued before now are refused all the same. The next token for the same game player ID,
// issued in a later second, starts a new, empty profile.
//
// One event goes to the outbox for each thing that ended, with the types Realtime already
// handles: clan.member_left (reason "left") or clan.disbanded, friend.removed (removed_by is the
// deleted player), friend.request_cancelled for a request the player sent,
// friend.request_declined for one they received, player.unblocked, and last player.renamed with
// the new name.
func (s *Service) DeletePlayer(ctx context.Context, gameID, externalID string) error {
	err := s.change(ctx, func(ctx context.Context) ([]events.Event, error) {
		res, err := s.store.DeletePlayer(ctx, gameID, externalID, DeletedPlayerName)
		if err != nil {
			return nil, err
		}
		return deletionEvents(gameID, res), nil
	})
	if err != nil {
		return fmt.Errorf("delete player: %w", err)
	}
	return nil
}

// deletionEvents are the events of a player deletion; none when there was no live player.
func deletionEvents(gameID string, res repository.PlayerDeletion) []events.Event {
	if res.Player.ID == "" {
		return nil
	}
	var evs []events.Event
	if res.Clan != nil {
		evs = append(evs, leaveEvent(gameID, *res.Clan))
	}
	for _, f := range res.Friendships {
		evs = append(evs, friendRemoved(gameID, f, res.Player.ID))
	}
	for _, req := range res.Requests {
		typ := EventFriendRequestDeclined // another player had asked the deleted one
		if req.Sender.ID == res.Player.ID {
			typ = EventFriendRequestCancelled
		}
		evs = append(evs, events.New(typ, gameID, requestData(req)))
	}
	for _, b := range res.Blocks {
		evs = append(evs, events.New(EventPlayerUnblocked, gameID, blockData(b.Blocker, b.Blocked)))
	}
	return append(evs, events.New(EventPlayerRenamed, gameID, map[string]any{
		"player": ref(res.Player.ID, DeletedPlayerName),
	}))
}

// PlayerDeletedSince reports whether the player (gameID, externalID) was deleted after
// issuedAt, the issue time of their token. A token issued before a deletion must be refused:
// it has not expired, but the account it was issued for is gone.
func (s *Service) PlayerDeletedSince(ctx context.Context, gameID, externalID string, issuedAt time.Time) (bool, error) {
	deleted, err := s.store.DeletedAfter(ctx, gameID, externalID, issuedAt)
	if err != nil {
		return false, fmt.Errorf("player deleted since: %w", err)
	}
	return deleted, nil
}

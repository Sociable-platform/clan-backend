package service

import (
	"context"
	"fmt"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// SyncFriendship makes sure two friends have their DM conversation and are both members of it. It is
// what the startup backfill calls for every friendship Social lists, and it has the same effect on the
// database as friend.accepted, but it sends no frames: nobody is told anything, because nothing
// happened to the players, Realtime only learned about it late.
//
// It is safe to repeat. A conversation that exists is reused with its messages, and a changed display
// name is stored. A wrong ID or a missing name gives an error that wraps ErrInvalidEvent, so the
// caller can skip that one friendship; a database error is returned as it is.
func (s *Service) SyncFriendship(ctx context.Context, gameID string, a, b Player) error {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return err
	}
	if a, err = cleanPlayer("a", a); err != nil {
		return err
	}
	if b, err = cleanPlayer("b", b); err != nil {
		return err
	}
	if a.ID == b.ID {
		return invalid("a player cannot be a friend of themselves")
	}
	_, err = s.store.UpsertDM(ctx, gameID, dmKey(a.ID, b.ID),
		repository.Member{PlayerID: a.ID, Name: a.Name}, repository.Member{PlayerID: b.ID, Name: b.Name})
	if err != nil {
		return fmt.Errorf("sync friendship %s and %s: %w", a.ID, b.ID, err)
	}
	return nil
}

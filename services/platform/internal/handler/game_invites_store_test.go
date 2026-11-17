package handler

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// memStore keeps no invitations: the end-to-end handler tests that use it do not invite anybody, and the
// service tests cover the invitations themselves. These satisfy service.Store.

func (m *memStore) PurgeGameInvites(context.Context, time.Time) error { return nil }

func (m *memStore) EmailIsMember(context.Context, string, string) (bool, error) { return false, nil }

func (m *memStore) InviteCountsSince(context.Context, string, string, time.Time) (repository.InviteCounts, error) {
	return repository.InviteCounts{}, nil
}

func (m *memStore) SaveGameInvite(context.Context, string, string, string, time.Time, time.Time) (repository.GameInvite, error) {
	return repository.GameInvite{}, repository.ErrNotFound
}

func (m *memStore) GameInvitesOfGame(context.Context, string, time.Time) ([]repository.GameInvite, error) {
	return []repository.GameInvite{}, nil
}

func (m *memStore) GameInvitesForEmail(context.Context, string, time.Time) ([]repository.GameInvite, error) {
	return []repository.GameInvite{}, nil
}

func (m *memStore) DeleteGameInvite(context.Context, string, string) (string, error) {
	return "", repository.ErrNotFound
}

func (m *memStore) DeclineGameInvite(context.Context, string, string) (string, error) {
	return "", repository.ErrNotFound
}

func (m *memStore) AcceptGameInvite(context.Context, string, string, string, time.Time) (repository.Game, error) {
	return repository.Game{}, repository.ErrNotFound
}

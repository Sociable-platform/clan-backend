package handler

import (
	"context"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// memStore has no team changes either: the service tests cover them. These satisfy service.Store.

func (m *memStore) RemoveGameMember(context.Context, string, string) error {
	return repository.ErrNotFound
}

func (m *memStore) SetGameMemberRole(context.Context, string, string, string) (repository.GameMember, bool, error) {
	return repository.GameMember{}, false, repository.ErrNotFound
}

func (m *memStore) AddTeamLog(context.Context, string, repository.TeamLogInput) error { return nil }

func (m *memStore) TeamLogOfGame(context.Context, string, *repository.Cursor, int) ([]repository.TeamLogEntry, error) {
	return []repository.TeamLogEntry{}, nil
}

func (m *memStore) TransferGameOwnership(context.Context, string, string, string) error {
	return repository.ErrNotFound
}

func (m *memStore) AdminGameByID(context.Context, string) (repository.AdminGame, error) {
	return repository.AdminGame{}, repository.ErrNotFound
}

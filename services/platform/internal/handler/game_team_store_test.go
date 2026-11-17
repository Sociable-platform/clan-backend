package handler

import (
	"context"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// memStore has no team changes either: the service tests cover them. These satisfy service.Store.

func (m *memStore) RemoveGameMember(context.Context, string, string) error {
	return repository.ErrNotFound
}

func (m *memStore) SetGameMemberRole(context.Context, string, string, string) (repository.GameMember, error) {
	return repository.GameMember{}, repository.ErrNotFound
}

func (m *memStore) TransferGameOwnership(context.Context, string, string, string) error {
	return repository.ErrNotFound
}

func (m *memStore) AdminGameByID(context.Context, string) (repository.AdminGame, error) {
	return repository.AdminGame{}, repository.ErrNotFound
}

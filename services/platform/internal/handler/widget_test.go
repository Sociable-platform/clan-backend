package handler

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// These methods let memStore keep one widget draft per game, like the widget_drafts table.

func (m *memStore) WidgetDraftOfGame(_ context.Context, gameID string) (repository.WidgetDraft, error) {
	d, ok := m.drafts[gameID]
	if !ok {
		return repository.WidgetDraft{}, repository.ErrNotFound
	}
	return d, nil
}

func (m *memStore) SaveWidgetDraft(_ context.Context, gameID string, config []byte) (repository.WidgetDraft, error) {
	if m.drafts == nil {
		m.drafts = map[string]repository.WidgetDraft{}
	}
	d := repository.WidgetDraft{GameID: gameID, Config: config, UpdatedAt: time.Now()}
	m.drafts[gameID] = d
	return d, nil
}

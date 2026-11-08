package handler

import (
	"context"
	"encoding/json"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
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

// fakeService answers the widget calls from fields, like its other calls.
// (Its fields, widgetDraft and saveWidgetDraft, are declared with the others in handler_test.go.)

func (f fakeService) WidgetDraft(_ context.Context, developerID, gameID string) (service.WidgetDraft, error) {
	return f.widgetDraft(developerID, gameID)
}

func (f fakeService) SaveWidgetDraft(_ context.Context, developerID, gameID string, config json.RawMessage) (service.WidgetDraft, error) {
	return f.saveWidgetDraft(developerID, gameID, config)
}

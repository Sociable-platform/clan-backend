package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// maxWidgetConfigBytes is the largest config a developer can save.
	maxWidgetConfigBytes = 64 * 1024

	// defaultWidgetConfig is what a game with no saved draft starts from.
	// Placeholder until the config contract (W1) defines the real default.
	defaultWidgetConfig = `{"schemaVersion":1}`
)

// WidgetDraft is the config a developer is editing. UpdatedAt is nil when nothing was
// saved yet and Config is the default.
type WidgetDraft struct {
	Config    json.RawMessage
	UpdatedAt *time.Time
}

// errInvalidWidgetConfig is the 400 for a config that cannot be saved.
func errInvalidWidgetConfig(message string) *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, "invalid_widget_config", message)
}

// checkWidgetConfig makes sure raw is a JSON object of reasonable size.
// The field-by-field rules of the config contract come later (W3).
func checkWidgetConfig(raw []byte) error {
	if len(raw) > maxWidgetConfigBytes {
		return errInvalidWidgetConfig(fmt.Sprintf("Config must be at most %d KB", maxWidgetConfigBytes/1024))
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errInvalidWidgetConfig("Config must be a JSON object")
	}
	return nil
}

// WidgetDraft returns the draft of a game the developer owns, or the default config
// when nothing was saved yet.
func (s *Service) WidgetDraft(ctx context.Context, developerID, gameID string) (WidgetDraft, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return WidgetDraft{}, err
	}
	d, err := s.store.WidgetDraftOfGame(ctx, gameID)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetDraft{Config: json.RawMessage(defaultWidgetConfig)}, nil
	}
	if err != nil {
		return WidgetDraft{}, fmt.Errorf("get widget draft of game %s: %w", gameID, err)
	}
	return toWidgetDraft(d), nil
}

// SaveWidgetDraft stores config as the draft of a game the developer owns.
func (s *Service) SaveWidgetDraft(ctx context.Context, developerID, gameID string, config json.RawMessage) (WidgetDraft, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return WidgetDraft{}, err
	}
	if err := checkWidgetConfig(config); err != nil {
		return WidgetDraft{}, err
	}
	d, err := s.store.SaveWidgetDraft(ctx, gameID, config)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetDraft{}, errGameNotFound() // the game was removed after the check above
	}
	if err != nil {
		return WidgetDraft{}, fmt.Errorf("save widget draft of game %s: %w", gameID, err)
	}
	return toWidgetDraft(d), nil
}

func toWidgetDraft(d repository.WidgetDraft) WidgetDraft {
	updated := d.UpdatedAt
	return WidgetDraft{Config: json.RawMessage(d.Config), UpdatedAt: &updated}
}

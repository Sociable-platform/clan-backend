package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/widgetconfig"
)

const (
	// maxWidgetConfigBytes is the largest config a developer can save (the contract's 64 KB).
	maxWidgetConfigBytes = widgetconfig.MaxBytes

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

// errInvalidWidgetConfig is the 400 for a config that cannot be saved. details lists every
// problem as { path, rule, message }, so Studio can mark the field that is wrong.
func errInvalidWidgetConfig(message string, details []widgetconfig.Issue) *httpx.Error {
	return httpx.NewErrorWithDetails(http.StatusBadRequest, "invalid_widget_config", message, details)
}

// checkWidgetConfig makes sure raw is a valid config v1 document: not over 64 KB, valid
// JSON, and following every rule of the contract (internal/widgetconfig). It is used when a
// draft is saved and again when it is published.
func checkWidgetConfig(raw []byte) error {
	issues := widgetconfig.Validate(raw)
	switch {
	case len(issues) == 0:
		return nil
	case len(issues) == 1 && (issues[0].Rule == widgetconfig.RuleSize || issues[0].Rule == widgetconfig.RuleJSON):
		return errInvalidWidgetConfig(issues[0].Message, issues)
	case len(issues) == 1:
		return errInvalidWidgetConfig("Config is not valid: 1 problem, see details", issues)
	}
	return errInvalidWidgetConfig(fmt.Sprintf("Config is not valid: %d problems, see details", len(issues)), issues)
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

// WidgetVersion is one published config. Config is nil in lists.
type WidgetVersion struct {
	Version     int
	Config      json.RawMessage
	PublishedAt time.Time
	PublishedBy string // developer ID
}

func errWidgetDraftMissing() *httpx.Error {
	return httpx.NewError(http.StatusConflict, "widget_draft_missing", "Save a draft before publishing")
}

func errWidgetVersionNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "widget_version_not_found", "Widget version not found")
}

func errWidgetNotPublished() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "widget_not_published", "This game has no published widget")
}

// PublicWidgetConfig returns the newest published version of a game's widget, for the players'
// page. It needs no developer: the game ID is public. A game that does not exist, and a
// game that was never published, both answer widget_not_published, so the route does not
// tell a stranger which game IDs exist. Drafts are never returned.
func (s *Service) PublicWidgetConfig(ctx context.Context, gameID string) (WidgetVersion, error) {
	if !uuidPattern.MatchString(gameID) {
		return WidgetVersion{}, errWidgetNotPublished()
	}
	v, err := s.store.LatestWidgetVersion(ctx, gameID)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetVersion{}, errWidgetNotPublished()
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("get latest widget version of game %s: %w", gameID, err)
	}
	return toWidgetVersion(v), nil
}

// PublishWidget publishes the saved draft of a game the developer owns as its next version.
// The draft stays as it is, so the developer can keep editing from it.
func (s *Service) PublishWidget(ctx context.Context, developerID, gameID string) (WidgetVersion, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return WidgetVersion{}, err
	}
	d, err := s.store.WidgetDraftOfGame(ctx, gameID)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetVersion{}, errWidgetDraftMissing()
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("get widget draft of game %s: %w", gameID, err)
	}
	// check again: the rules may be stricter now than when the draft was saved
	if err := checkWidgetConfig(d.Config); err != nil {
		return WidgetVersion{}, err
	}
	return s.addWidgetVersion(ctx, developerID, gameID, d.Config)
}

// WidgetVersions lists the published versions of a game the developer owns, newest first,
// without their configs. A game that was never published has an empty list.
func (s *Service) WidgetVersions(ctx context.Context, developerID, gameID string) ([]WidgetVersion, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return nil, err
	}
	rows, err := s.store.WidgetVersionsOfGame(ctx, gameID)
	if err != nil {
		return nil, fmt.Errorf("list widget versions of game %s: %w", gameID, err)
	}
	versions := make([]WidgetVersion, 0, len(rows))
	for _, v := range rows {
		versions = append(versions, toWidgetVersion(v))
	}
	return versions, nil
}

// WidgetVersion returns one published version, with its config.
func (s *Service) WidgetVersion(ctx context.Context, developerID, gameID string, version int) (WidgetVersion, error) {
	if _, err := s.OwnedGame(ctx, developerID, gameID); err != nil {
		return WidgetVersion{}, err
	}
	v, err := s.store.WidgetVersionOfGame(ctx, gameID, version)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetVersion{}, errWidgetVersionNotFound()
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("get widget version %d of game %s: %w", version, gameID, err)
	}
	return toWidgetVersion(v), nil
}

// RollbackWidget publishes an old version again, as a new version on top of the history.
// Nothing is deleted or changed, and the draft is left alone.
func (s *Service) RollbackWidget(ctx context.Context, developerID, gameID string, version int) (WidgetVersion, error) {
	old, err := s.WidgetVersion(ctx, developerID, gameID, version)
	if err != nil {
		return WidgetVersion{}, err
	}
	return s.addWidgetVersion(ctx, developerID, gameID, old.Config)
}

func (s *Service) addWidgetVersion(ctx context.Context, developerID, gameID string, config []byte) (WidgetVersion, error) {
	v, err := s.store.AddWidgetVersion(ctx, gameID, developerID, config)
	if errors.Is(err, repository.ErrNotFound) {
		return WidgetVersion{}, errGameNotFound() // the game was removed after the ownership check
	}
	if err != nil {
		return WidgetVersion{}, fmt.Errorf("publish widget of game %s: %w", gameID, err)
	}
	return toWidgetVersion(v), nil
}

func toWidgetVersion(v repository.WidgetVersion) WidgetVersion {
	out := WidgetVersion{Version: v.Version, PublishedAt: v.PublishedAt, PublishedBy: v.PublishedBy}
	if v.Config != nil {
		out.Config = json.RawMessage(v.Config)
	}
	return out
}

func toWidgetDraft(d repository.WidgetDraft) WidgetDraft {
	updated := d.UpdatedAt
	return WidgetDraft{Config: json.RawMessage(d.Config), UpdatedAt: &updated}
}

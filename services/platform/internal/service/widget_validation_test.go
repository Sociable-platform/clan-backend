package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/widgetconfig"
)

// The fixtures are the shared config v1 contract files, the same ones the SDK is tested with.
const fixturesDir = "../widgetconfig/testdata/examples"

type fixtureCase struct {
	File   string `json:"file"`
	Valid  bool   `json:"valid"`
	Errors []struct {
		Path string `json:"path"`
		Rule string `json:"rule"`
	} `json:"errors"`
}

func loadFixtureCases(t *testing.T) []fixtureCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturesDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Cases []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Cases) == 0 {
		t.Fatal("manifest has no cases")
	}
	return m.Cases
}

func issuesOf(t *testing.T, err error) []widgetconfig.Issue {
	t.Helper()
	var e *httpx.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *httpx.Error, got %v", err)
	}
	issues, ok := e.Details.([]widgetconfig.Issue)
	if !ok || len(issues) == 0 {
		t.Fatalf("want details with issues, got %#v", e.Details)
	}
	return issues
}

func TestSaveWidgetDraft_SharedFixtures(t *testing.T) {
	for _, c := range loadFixtureCases(t) {
		t.Run(c.File, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixturesDir, c.File))
			if err != nil {
				t.Fatal(err)
			}
			svc, alice, _, game := aliceGame(t)
			_, err = svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, raw)
			if c.Valid {
				if err != nil {
					t.Fatalf("valid fixture refused: %v", err)
				}
				return
			}
			wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
			got := issuesOf(t, err)
			for _, want := range c.Errors {
				found := false
				for _, g := range got {
					found = found || (g.Path == want.Path && g.Rule == want.Rule)
				}
				if !found {
					t.Errorf("missing field-level error %q %q, got %+v", want.Path, want.Rule, got)
				}
			}
			// a refused config changes nothing
			if _, derr := svc.store.(*fakeStore).WidgetDraftOfGame(context.Background(), game.ID); !errors.Is(derr, repository.ErrNotFound) {
				t.Fatalf("a refused config must not be stored, got %v", derr)
			}
		})
	}
}

func TestSaveWidgetDraft_OversizedIsOneSizeIssue(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	big := cfg("Big") + strings.Repeat(" ", maxWidgetConfigBytes)
	_, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(big))
	wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
	issues := issuesOf(t, err)
	if len(issues) != 1 || issues[0].Rule != widgetconfig.RuleSize || issues[0].Path != "" {
		t.Fatalf("want one size issue for the whole document, got %+v", issues)
	}
}

func TestSaveWidgetDraft_MalformedJSONIsOneJSONIssue(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	_, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(`{"schemaVersion":`))
	wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
	if issues := issuesOf(t, err); len(issues) != 1 || issues[0].Rule != widgetconfig.RuleJSON {
		t.Fatalf("want one json issue, got %+v", issues)
	}
}

func TestPublishWidget_OldInvalidDraftIsRefusedWithDetails(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	// a draft saved before validation existed (or edited in the database) must not go public
	svc.store.(*fakeStore).drafts = map[string]repository.WidgetDraft{
		game.ID: {GameID: game.ID, Config: []byte(`{"schemaVersion":1}`)},
	}
	_, err := svc.PublishWidget(context.Background(), alice.ID, game.ID)
	wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
	issuesOf(t, err)
	if _, perr := svc.PublicWidgetConfig(context.Background(), game.ID); perr == nil {
		t.Fatal("nothing must be published")
	}
}

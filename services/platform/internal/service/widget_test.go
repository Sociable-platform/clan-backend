package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// These methods make fakeStore keep one draft per game, like the widget_drafts table.

func (f *fakeStore) WidgetDraftOfGame(_ context.Context, gameID string) (repository.WidgetDraft, error) {
	if f.err != nil {
		return repository.WidgetDraft{}, f.err
	}
	d, ok := f.drafts[gameID]
	if !ok {
		return repository.WidgetDraft{}, repository.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) SaveWidgetDraft(_ context.Context, gameID string, config []byte) (repository.WidgetDraft, error) {
	if f.err != nil {
		return repository.WidgetDraft{}, f.err
	}
	exists := false
	for _, g := range f.games {
		exists = exists || g.ID == gameID
	}
	if !exists {
		return repository.WidgetDraft{}, repository.ErrNotFound
	}
	if f.drafts == nil {
		f.drafts = map[string]repository.WidgetDraft{}
	}
	d := repository.WidgetDraft{GameID: gameID, Config: config, UpdatedAt: time.Unix(1_800_000_000+int64(len(f.drafts)), 0)}
	f.drafts[gameID] = d
	return d, nil
}

// aliceGame returns a service with a game owned by alice, and bob as a second developer.
func aliceGame(t *testing.T) (svc *Service, alice, bob Developer, game Game) {
	t.Helper()
	svc, alice, bob = twoDevelopers(t)
	game, err := svc.RegisterGame(context.Background(), alice.ID, RegisterGameInput{Name: "Space Raiders"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, alice, bob, game
}

func TestWidgetDraft_DefaultWhenNothingSaved(t *testing.T) {
	svc, alice, _, game := aliceGame(t)

	d, err := svc.WidgetDraft(context.Background(), alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(d.Config) != defaultWidgetConfig || d.UpdatedAt != nil {
		t.Fatalf("want the default and no time, got %s / %v", d.Config, d.UpdatedAt)
	}
}

func TestSaveWidgetDraft_ThenRead(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	saved, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(`{"schemaVersion":1,"theme":{"accent":"#8B65EE"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpdatedAt == nil {
		t.Fatal("a saved draft has a time")
	}
	got, err := svc.WidgetDraft(ctx, alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Config) != `{"schemaVersion":1,"theme":{"accent":"#8B65EE"}}` {
		t.Fatalf("read back %s", got.Config)
	}
}

func TestSaveWidgetDraft_SecondSaveReplacesTheFirst(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()
	for _, cfg := range []string{`{"a":1}`, `{"a":2}`} {
		if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := svc.WidgetDraft(ctx, alice.ID, game.ID)
	if string(got.Config) != `{"a":2}` {
		t.Fatalf("want the last config, got %s", got.Config)
	}
}

func TestWidgetDraft_OnlyTheOwnerCanUseIt(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}

	_, err := svc.WidgetDraft(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.SaveWidgetDraft(ctx, bob.ID, game.ID, json.RawMessage(`{"a":1}`))
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.WidgetDraft(ctx, alice.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "game_not_found")

	got, _ := svc.WidgetDraft(ctx, alice.ID, game.ID)
	if string(got.Config) != `{"a":1}` {
		t.Fatalf("bob must not change alice's draft, got %s", got.Config)
	}
}

func TestSaveWidgetDraft_RejectsBadConfig(t *testing.T) {
	tests := []struct{ name, config string }{
		{"empty", ``},
		{"not json", `nope`},
		{"truncated json", `{"a":`},
		{"array", `[]`},
		{"string", `"x"`},
		{"number", `123`},
		{"null", `null`},
		{"too large", `{"a":"` + strings.Repeat("x", maxWidgetConfigBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, alice, _, game := aliceGame(t)
			_, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(tt.config))
			wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
		})
	}

	t.Run("exactly the size limit is fine", func(t *testing.T) {
		svc, alice, _, game := aliceGame(t)
		pad := maxWidgetConfigBytes - len(`{"a":""}`)
		cfg := `{"a":"` + strings.Repeat("x", pad) + `"}`
		if len(cfg) != maxWidgetConfigBytes {
			t.Fatalf("test setup: %d bytes", len(cfg))
		}
		if _, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(cfg)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWidgetDraft_StoreFailureIsAnError(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	svc.store.(*fakeStore).err = context.DeadlineExceeded // from now on every store call fails
	if _, err := svc.WidgetDraft(context.Background(), alice.ID, game.ID); err == nil {
		t.Fatal("want an error when the store fails")
	}
}

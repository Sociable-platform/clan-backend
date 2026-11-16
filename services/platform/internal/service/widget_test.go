package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// validConfigTemplate is the farm-minimal example from the config v1 contract with one
// player-facing word left open, so tests can make configs that are valid but different.
const validConfigTemplate = `{"schemaVersion":1,"game_id":"b21d94e7-3c0a-4f68-8e15-a9d7c4f02b61",` +
	`"theme":{"mode":"light","fonts":{"heading":{"family":"Fredoka","source":"google"},"body":{"family":"Nunito","source":"google"}},` +
	`"frame":{"style":"none","width":1,"color":"#D9DBD5","corners":"round"},"buttons":{"style":"outline","case":"normal"},` +
	`"panel":{"opacity":0.9,"blur":true},"colors":{"brand":"#2E8B57","background":"#FFFFFF","surface":"#F2F3F1","text":"#000000","muted":"#5B5D56"},"textures":{}},` +
	`"layout":{"position":"bottom-left","startCollapsed":false},"friends":{"enabled":true},` +
	`"clan":{"enabled":false,"maxMembers":10,"descriptionMax":140,"roles":[{"id":"member","name":"Member","canPromote":false,"canKick":false},{"id":"leader","name":"Leader","canPromote":true,"canKick":true}],"badges":{"source":"developer","allowed":[]},` +
	`"screens":{"discover":{"enabled":false},"create":{"enabled":false},"home":{"enabled":false,"header":"compact","showStats":false},"members":{"enabled":false}}},` +
	`"chat":{"enabled":true,"layout":"bubbles","directMessages":true,"clanChat":false,"maxMessageLength":500,"slowModeSeconds":0,"profanityFilter":false,"historyLimit":50},` +
	`"texts":{"terms":{"clan":"%s","clans":"%ss"},"labels":{}}}`

// cfg returns a valid config; different words give different (but equally valid) configs.
func cfg(word string) string { return fmt.Sprintf(validConfigTemplate, word, word) }

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

func (f *fakeStore) AddWidgetVersion(_ context.Context, gameID, developerID string, config []byte) (repository.WidgetVersion, error) {
	if f.err != nil {
		return repository.WidgetVersion{}, f.err
	}
	exists := false
	for _, g := range f.games {
		exists = exists || g.ID == gameID
	}
	if !exists || !f.developerExists(developerID) {
		return repository.WidgetVersion{}, repository.ErrNotFound
	}
	if f.versions == nil {
		f.versions = map[string][]repository.WidgetVersion{}
	}
	v := repository.WidgetVersion{
		GameID: gameID, Version: len(f.versions[gameID]) + 1, Config: config,
		PublishedAt: time.Unix(1_800_000_000+int64(len(f.versions[gameID])), 0), PublishedBy: developerID,
	}
	f.versions[gameID] = append(f.versions[gameID], v)
	return v, nil
}

func (f *fakeStore) WidgetVersionsOfGame(_ context.Context, gameID string) ([]repository.WidgetVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.WidgetVersion{}
	all := f.versions[gameID]
	for i := len(all) - 1; i >= 0; i-- { // newest first, without configs, like the real query
		v := all[i]
		v.Config = nil
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeStore) WidgetVersionOfGame(_ context.Context, gameID string, version int) (repository.WidgetVersion, error) {
	if f.err != nil {
		return repository.WidgetVersion{}, f.err
	}
	if version < 1 || version > len(f.versions[gameID]) {
		return repository.WidgetVersion{}, repository.ErrNotFound
	}
	return f.versions[gameID][version-1], nil
}

func (f *fakeStore) LatestWidgetVersion(_ context.Context, gameID string) (repository.WidgetVersion, error) {
	if f.err != nil {
		return repository.WidgetVersion{}, f.err
	}
	all := f.versions[gameID]
	if len(all) == 0 {
		return repository.WidgetVersion{}, repository.ErrNotFound
	}
	return all[len(all)-1], nil
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

	saved, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("Accent")))
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
	if string(got.Config) != cfg("Accent") {
		t.Fatalf("read back %s", got.Config)
	}
}

func TestSaveWidgetDraft_SecondSaveReplacesTheFirst(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()
	for _, word := range []string{"A1", "A2"} {
		if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg(word))); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := svc.WidgetDraft(ctx, alice.ID, game.ID)
	if string(got.Config) != cfg("A2") {
		t.Fatalf("want the last config, got %s", got.Config)
	}
}

func TestWidgetDraft_OnlyTheOwnerCanUseIt(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("A1"))); err != nil {
		t.Fatal(err)
	}

	_, err := svc.WidgetDraft(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.SaveWidgetDraft(ctx, bob.ID, game.ID, json.RawMessage(cfg("A1")))
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.WidgetDraft(ctx, alice.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "game_not_found")

	got, _ := svc.WidgetDraft(ctx, alice.ID, game.ID)
	if string(got.Config) != cfg("A1") {
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
		// trailing spaces are legal JSON, so a valid config can be padded to the exact limit
		padded := cfg("Exact")
		padded += strings.Repeat(" ", maxWidgetConfigBytes-len(padded))
		if len(padded) != maxWidgetConfigBytes {
			t.Fatalf("test setup: %d bytes", len(padded))
		}
		if _, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(padded)); err != nil {
			t.Fatal(err)
		}
		// one byte more is refused
		_, err := svc.SaveWidgetDraft(context.Background(), alice.ID, game.ID, json.RawMessage(padded+" "))
		wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
	})
}

func TestWidgetDraft_StoreFailureIsAnError(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	svc.store.(*fakeStore).err = context.DeadlineExceeded // from now on every store call fails
	if _, err := svc.WidgetDraft(context.Background(), alice.ID, game.ID); err == nil {
		t.Fatal("want an error when the store fails")
	}
}

// saveDraft saves config as alice's draft, failing the test if it is refused.
func saveDraft(t *testing.T, svc *Service, devID, gameID, config string) {
	t.Helper()
	if _, err := svc.SaveWidgetDraft(context.Background(), devID, gameID, json.RawMessage(config)); err != nil {
		t.Fatal(err)
	}
}

func TestPublishWidget_NumbersCountUpAndKeepTheirConfig(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	saveDraft(t, svc, alice.ID, game.ID, cfg("One"))
	v1, err := svc.PublishWidget(ctx, alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || string(v1.Config) != cfg("One") || v1.PublishedBy != alice.ID {
		t.Fatalf("first publish: %+v", v1)
	}

	// publishing again without changes is allowed and makes version 2
	if v2, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil || v2.Version != 2 {
		t.Fatalf("second publish: %+v, %v", v2, err)
	}

	// editing the draft afterwards does not touch what was published
	saveDraft(t, svc, alice.ID, game.ID, cfg("Three"))
	v3, err := svc.PublishWidget(ctx, alice.ID, game.ID)
	if err != nil || v3.Version != 3 || string(v3.Config) != cfg("Three") {
		t.Fatalf("third publish: %+v, %v", v3, err)
	}
	old, err := svc.WidgetVersion(ctx, alice.ID, game.ID, 1)
	if err != nil || string(old.Config) != cfg("One") {
		t.Fatalf("version 1 must keep its config: %+v, %v", old, err)
	}

	// publishing does not consume or change the draft
	if d, _ := svc.WidgetDraft(ctx, alice.ID, game.ID); string(d.Config) != cfg("Three") {
		t.Fatalf("draft changed by publishing: %s", d.Config)
	}
}

func TestPublishWidget_NeedsASavedDraft(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	_, err := svc.PublishWidget(context.Background(), alice.ID, game.ID)
	wantError(t, err, http.StatusConflict, "widget_draft_missing")
	if list, _ := svc.WidgetVersions(context.Background(), alice.ID, game.ID); len(list) != 0 {
		t.Fatalf("a refused publish must add nothing, got %+v", list)
	}
}

func TestPublishWidget_ChecksTheDraftAgain(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	// a draft that would be refused today, as if it was saved under looser rules
	svc.store.(*fakeStore).drafts = map[string]repository.WidgetDraft{
		game.ID: {GameID: game.ID, Config: []byte(`[]`), UpdatedAt: time.Now()},
	}
	_, err := svc.PublishWidget(context.Background(), alice.ID, game.ID)
	wantError(t, err, http.StatusBadRequest, "invalid_widget_config")
	if list, _ := svc.WidgetVersions(context.Background(), alice.ID, game.ID); len(list) != 0 {
		t.Fatalf("a refused publish must add nothing, got %+v", list)
	}
}

func TestWidgetVersions_NewestFirstWithoutConfigs(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	empty, err := svc.WidgetVersions(ctx, alice.ID, game.ID)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("never published: want an empty non-nil list, got %#v, %v", empty, err)
	}

	for _, word := range []string{"N1", "N2", "N3"} {
		saveDraft(t, svc, alice.ID, game.ID, cfg(word))
		if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.WidgetVersions(ctx, alice.ID, game.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("got %+v, %v", list, err)
	}
	if list[0].Version != 3 || list[1].Version != 2 || list[2].Version != 1 {
		t.Fatalf("want newest first, got %+v", list)
	}
	for _, v := range list {
		if v.Config != nil {
			t.Fatalf("lists must leave the config out, got %s", v.Config)
		}
	}
}

func TestWidgetVersion_UnknownNumbersAreNotFound(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	saveDraft(t, svc, alice.ID, game.ID, cfg("N1"))
	if _, err := svc.PublishWidget(context.Background(), alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{-1, 0, 2, 99} {
		_, err := svc.WidgetVersion(context.Background(), alice.ID, game.ID, n)
		wantError(t, err, http.StatusNotFound, "widget_version_not_found")
	}
}

func TestRollbackWidget_PublishesTheOldConfigAsANewVersion(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	saveDraft(t, svc, alice.ID, game.ID, cfg("LookA"))
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil { // version 1
		t.Fatal(err)
	}
	saveDraft(t, svc, alice.ID, game.ID, cfg("LookB"))
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil { // version 2
		t.Fatal(err)
	}

	back, err := svc.RollbackWidget(ctx, alice.ID, game.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != 3 || string(back.Config) != cfg("LookA") {
		t.Fatalf("rollback should make version 3 with version 1's config, got %+v", back)
	}

	// nothing was rewritten: version 2 is still B, and the history grew by one
	if v2, _ := svc.WidgetVersion(ctx, alice.ID, game.ID, 2); string(v2.Config) != cfg("LookB") {
		t.Fatalf("version 2 changed: %s", v2.Config)
	}
	if list, _ := svc.WidgetVersions(ctx, alice.ID, game.ID); len(list) != 3 {
		t.Fatalf("want 3 versions, got %d", len(list))
	}
	// the draft is left as the developer had it
	if d, _ := svc.WidgetDraft(ctx, alice.ID, game.ID); string(d.Config) != cfg("LookB") {
		t.Fatalf("rollback must not touch the draft, got %s", d.Config)
	}
}

func TestRollbackWidget_UnknownVersion(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	for _, n := range []int{0, 1, 5} {
		_, err := svc.RollbackWidget(context.Background(), alice.ID, game.ID, n)
		wantError(t, err, http.StatusNotFound, "widget_version_not_found")
	}
	if list, _ := svc.WidgetVersions(context.Background(), alice.ID, game.ID); len(list) != 0 {
		t.Fatalf("a refused rollback must add nothing, got %+v", list)
	}
}

func TestWidgetVersions_OnlyTheOwnerCanUseThem(t *testing.T) {
	svc, alice, bob, game := aliceGame(t)
	ctx := context.Background()
	saveDraft(t, svc, alice.ID, game.ID, cfg("N1"))
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}

	_, err := svc.PublishWidget(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.WidgetVersions(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.WidgetVersion(ctx, bob.ID, game.ID, 1)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.RollbackWidget(ctx, bob.ID, game.ID, 1)
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.PublishWidget(ctx, alice.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "game_not_found")

	if list, _ := svc.WidgetVersions(ctx, alice.ID, game.ID); len(list) != 1 {
		t.Fatalf("bob must not add versions to alice's game, got %d", len(list))
	}
}

func TestWidgetVersions_StoreFailureIsAnError(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	saveDraft(t, svc, alice.ID, game.ID, cfg("N1"))
	svc.store.(*fakeStore).err = context.DeadlineExceeded // from now on every store call fails
	// every store call fails, so none of these may report success
	if _, err := svc.PublishWidget(context.Background(), alice.ID, game.ID); err == nil {
		t.Fatal("publish: want an error when the store fails")
	}
	if _, err := svc.WidgetVersions(context.Background(), alice.ID, game.ID); err == nil {
		t.Fatal("list: want an error when the store fails")
	}
}

func TestPublicWidgetConfig_ReturnsTheLatestPublishedVersionNotTheDraft(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	_, err := svc.PublicWidgetConfig(ctx, game.ID)
	wantError(t, err, http.StatusNotFound, "widget_not_published")

	// a draft alone is not published
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("A1"))); err != nil {
		t.Fatal(err)
	}
	_, err = svc.PublicWidgetConfig(ctx, game.ID)
	wantError(t, err, http.StatusNotFound, "widget_not_published")

	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("A2"))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	// work in progress after the last publish must stay hidden from players
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("Secret"))); err != nil {
		t.Fatal(err)
	}

	got, err := svc.PublicWidgetConfig(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || string(got.Config) != cfg("A2") {
		t.Fatalf("want version 2 with {\"a\":2}, got %d %s", got.Version, got.Config)
	}

	// a rollback is a new version, and players get it
	if _, err := svc.RollbackWidget(ctx, alice.ID, game.ID, 1); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.PublicWidgetConfig(ctx, game.ID)
	if got.Version != 3 || string(got.Config) != cfg("A1") {
		t.Fatalf("after rollback want version 3 with {\"a\":1}, got %d %s", got.Version, got.Config)
	}
}

func TestPublicWidgetConfig_UnknownOrMalformedGameLooksLikeNotPublished(t *testing.T) {
	svc, _, _, _ := aliceGame(t)
	for _, id := range []string{"00000000-0000-4000-8000-00000000dead", "not-a-uuid", ""} {
		_, err := svc.PublicWidgetConfig(context.Background(), id)
		wantError(t, err, http.StatusNotFound, "widget_not_published")
	}
}

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// recordingService is a service with one game owned by alice, and the events its transactions
// committed to the outbox.
func recordingService(t *testing.T) (*Service, *events.Recorder, *fakeStore, string, string) {
	t.Helper()
	svc, alice, _, game := aliceGame(t)
	store := svc.store.(*fakeStore)
	return svc, &store.committed, store, alice.ID, game.ID
}

// wantConfigPublished fails unless the recorder holds exactly one config.published per
// version in want, in that order, each for gameID.
func wantConfigPublished(t *testing.T, rec *events.Recorder, gameID string, want ...int) {
	t.Helper()
	evs := rec.Events()
	if len(evs) != len(want) {
		t.Fatalf("published %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, e := range evs {
		if e.Type != EventConfigPublished || e.GameID != gameID {
			t.Fatalf("event %d = %s for game %q, want %s for %q", i, e.Type, e.GameID, EventConfigPublished, gameID)
		}
		if !reflect.DeepEqual(e.Data, map[string]any{"version": want[i]}) {
			t.Fatalf("event %d data = %v, want version %d", i, e.Data, want[i])
		}
	}
}

func TestPublishWidget_PublishesConfigPublishedWithTheNewVersion(t *testing.T) {
	svc, rec, _, alice, game := recordingService(t)
	ctx := context.Background()

	saveDraft(t, svc, alice, game, cfg("LookA"))
	if _, err := svc.PublishWidget(ctx, alice, game); err != nil {
		t.Fatal(err)
	}
	saveDraft(t, svc, alice, game, cfg("LookB"))
	if _, err := svc.PublishWidget(ctx, alice, game); err != nil {
		t.Fatal(err)
	}
	wantConfigPublished(t, rec, game, 1, 2)
}

func TestRollbackWidget_PublishesConfigPublishedWithTheNewVersion(t *testing.T) {
	svc, rec, _, alice, game := recordingService(t)
	ctx := context.Background()

	saveDraft(t, svc, alice, game, cfg("LookA"))
	if _, err := svc.PublishWidget(ctx, alice, game); err != nil { // version 1
		t.Fatal(err)
	}
	saveDraft(t, svc, alice, game, cfg("LookB"))
	if _, err := svc.PublishWidget(ctx, alice, game); err != nil { // version 2
		t.Fatal(err)
	}
	// a rollback to 1 saves version 3, and that is the number Realtime must load
	if _, err := svc.RollbackWidget(ctx, alice, game, 1); err != nil {
		t.Fatal(err)
	}
	wantConfigPublished(t, rec, game, 1, 2, 3)
}

func TestPublishAndRollback_PublishNothingWhenTheyFail(t *testing.T) {
	svc, rec, store, alice, game := recordingService(t)
	ctx := context.Background()

	if _, err := svc.PublishWidget(ctx, alice, game); err == nil { // no draft saved yet
		t.Fatal("publish without a draft should fail")
	}
	if _, err := svc.RollbackWidget(ctx, alice, game, 7); err == nil { // no such version
		t.Fatal("rollback to an unknown version should fail")
	}
	if _, err := svc.PublishWidget(ctx, "someone-else", game); err == nil { // not the owner
		t.Fatal("publish by another developer should fail")
	}
	saveDraft(t, svc, alice, game, cfg("LookA"))
	store.err = errors.New("database is down")
	if _, err := svc.PublishWidget(ctx, alice, game); err == nil {
		t.Fatal("publish should fail when the database fails")
	}
	wantConfigPublished(t, rec, game) // none
}

// The event is part of the publish: when it cannot be written to the outbox, the publish fails and
// its version rolls back with it (the broker is never asked during a request).
func TestPublishWidget_FailsWhenTheEventCannotBeWritten(t *testing.T) {
	svc, rec, store, alice, game := recordingService(t)
	saveDraft(t, svc, alice, game, cfg("LookA"))
	store.addErr = errors.New("outbox insert failed")

	if _, err := svc.PublishWidget(context.Background(), alice, game); !errors.Is(err, store.addErr) {
		t.Fatalf("publish = %v, want the outbox error", err)
	}
	wantConfigPublished(t, rec, game) // none
}

// config.published must have exactly the fields of the same message in docs/event-messages.md.
// Realtime reads that file in its own tests, so a change on either side fails a test there.
func TestConfigPublishedHasTheFieldsOfTheEventMessagesFile(t *testing.T) {
	doc, err := os.ReadFile("../../../../docs/event-messages.md")
	if err != nil {
		t.Fatal(err)
	}
	// On Windows git may check the file out with CRLF line endings.
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	var want map[string]any
	for _, b := range regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllSubmatch(doc, -1) {
		var msg struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(b[1], &msg) == nil && msg.Type == EventConfigPublished {
			want = msg.Data
			break
		}
	}
	if want == nil {
		t.Fatalf("%s is not in docs/event-messages.md", EventConfigPublished)
	}

	svc, rec, _, alice, game := recordingService(t)
	saveDraft(t, svc, alice, game, cfg("LookA"))
	if _, err := svc.PublishWidget(context.Background(), alice, game); err != nil {
		t.Fatal(err)
	}
	// send it through JSON, as the publisher does
	body, err := json.Marshal(rec.Events()[0])
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		GameID string         `json:"game_id"`
		Data   map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.GameID != game {
		t.Errorf("game_id = %q, want %q", got.GameID, game)
	}
	if len(got.Data) != len(want) {
		t.Fatalf("data = %v, file has %v", got.Data, want)
	}
	for k, v := range want {
		g, ok := got.Data[k]
		if !ok || reflect.TypeOf(g) != reflect.TypeOf(v) {
			t.Errorf("field %q: published %v (%T), file has %v (%T)", k, g, g, v, v)
		}
	}
}

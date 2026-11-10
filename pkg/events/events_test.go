package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewFillsTheEnvelope(t *testing.T) {
	before := time.Now().UTC()
	e := New("friend.accepted", "game-1", map[string]any{"k": "v"})

	if !uuidV4.MatchString(e.ID) {
		t.Errorf("ID = %q, want a UUID v4", e.ID)
	}
	if e.Type != "friend.accepted" || e.GameID != "game-1" || e.Data["k"] != "v" {
		t.Errorf("event = %+v", e)
	}
	if e.OccurredAt.Before(before) || e.OccurredAt.Location() != time.UTC {
		t.Errorf("OccurredAt = %v, want now in UTC", e.OccurredAt)
	}
}

func TestNewGivesEveryEventItsOwnID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New("t", "g", nil).ID
		if seen[id] {
			t.Fatalf("duplicate ID %s", id)
		}
		seen[id] = true
	}
}

// The JSON field names are the contract with Realtime: a rename here breaks it.
func TestEventJSONFieldNames(t *testing.T) {
	e := Event{ID: "id-1", Type: "friend.accepted", OccurredAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		GameID: "game-1", Data: map[string]any{"k": "v"}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event_id":"id-1","type":"friend.accepted","occurred_at":"2026-11-10T08:00:00Z","game_id":"game-1","data":{"k":"v"}}`
	if string(b) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", b, want)
	}
}

func TestEventJSONLeavesOutEmptyData(t *testing.T) {
	b, _ := json.Marshal(Event{ID: "x"})
	if strings.Contains(string(b), `"data"`) {
		t.Errorf("JSON %s has data, want it left out", b)
	}
}

func TestSafeLogsAndSwallowsTheError(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	p := Safe(&Recorder{Err: errors.New("broker down")}, log)

	e := New("friend.accepted", "g", nil)
	if err := p.Publish(context.Background(), e); err != nil {
		t.Fatalf("Publish = %v, want nil", err)
	}
	for _, want := range []string{"publish event failed", e.ID, "friend.accepted", "broker down"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q does not contain %q", logs.String(), want)
		}
	}
}

func TestSafePassesTheEventOn(t *testing.T) {
	var logs bytes.Buffer
	rec := &Recorder{}
	e := New("clan.member_joined", "g", nil)
	if err := Safe(rec, slog.New(slog.NewJSONHandler(&logs, nil))).Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if got := rec.Events(); len(got) != 1 || got[0].ID != e.ID {
		t.Errorf("recorded %+v, want the one event", got)
	}
	if logs.Len() != 0 {
		t.Errorf("logged %q on success, want nothing", logs.String())
	}
}

func TestRecorderKeepsOrderAndReturnsACopy(t *testing.T) {
	rec := &Recorder{}
	for _, typ := range []string{"a", "b", "c"} {
		_ = rec.Publish(context.Background(), Event{Type: typ})
	}
	got := rec.Events()
	if len(got) != 3 || got[0].Type != "a" || got[1].Type != "b" || got[2].Type != "c" {
		t.Fatalf("Events = %+v, want a, b, c", got)
	}
	got[0].Type = "changed"
	if rec.Events()[0].Type != "a" {
		t.Error("changing the returned slice changed the recorder")
	}
}

func TestRecorderErrKeepsNothing(t *testing.T) {
	rec := &Recorder{Err: errors.New("boom")}
	if err := rec.Publish(context.Background(), Event{}); err == nil {
		t.Fatal("Publish = nil, want the error")
	}
	if len(rec.Events()) != 0 {
		t.Error("kept an event although Publish failed")
	}
}

func TestNoopNeverFails(t *testing.T) {
	if err := (Noop{}).Publish(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestNewIDIsARandomUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if !uuidV4.MatchString(id) || seen[id] {
			t.Fatalf("NewID() = %q: not a UUID v4, or made twice", id)
		}
		seen[id] = true
	}
}

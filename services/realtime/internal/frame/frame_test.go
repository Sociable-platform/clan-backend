package frame

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEncodeBuildsTheEnvelopeOfAnEvent(t *testing.T) {
	at := time.Date(2026, 11, 10, 13, 41, 7, 512_345_678, time.FixedZone("x", 4*3600)) // 09:41:07.512 UTC
	b, err := Encode("11111111-1111-4111-8111-111111111111", "message.new", at, map[string]any{"conversationId": "c-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, b)
	if got["type"] != "message.new" || got["game_id"] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("type/game_id = %v / %v", got["type"], got["game_id"])
	}
	if got["occurred_at"] != "2026-11-10T09:41:07.512Z" {
		t.Errorf("occurred_at = %v, want UTC with milliseconds (the offset converted, the rest cut off)", got["occurred_at"])
	}
	if id, _ := got["event_id"].(string); !uuidV4.MatchString(id) {
		t.Errorf("event_id = %q, want a UUID v4", got["event_id"])
	}
	if !reflect.DeepEqual(got["data"], map[string]any{"conversationId": "c-1"}) {
		t.Errorf("data = %v", got["data"])
	}
}

// The frame envelope IS the event envelope: the same field names, so one SDK parser reads both.
func TestFrameHasTheFieldsOfAnEvent(t *testing.T) {
	ev, err := json.Marshal(events.New("x", "g", map[string]any{"a": 1}))
	if err != nil {
		t.Fatal(err)
	}
	fr, err := Encode("g", "x", time.Now(), map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if want, got := keys(decode(t, ev)), keys(decode(t, fr)); !reflect.DeepEqual(got, want) {
		t.Errorf("frame fields %v, event fields %v: they must be the same", got, want)
	}
}

func TestOccurredAtAlwaysHasThreeDecimals(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 11, 10, 9, 41, 7, 0, time.UTC),         // no fraction at all
		time.Date(2026, 11, 10, 9, 41, 7, 5_000_000, time.UTC), // 5 ms
		time.Date(2026, 11, 10, 9, 41, 7, 999_999_999, time.UTC),
	} {
		got := New("g", "x", at, nil).OccurredAt
		if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(got) {
			t.Errorf("occurred_at for %v = %q, want exactly three decimals and a Z", at, got)
		}
		if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
			t.Errorf("occurred_at %q is not RFC 3339: %v", got, err)
		}
	}
}

func TestZeroTimeMeansNow(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Millisecond)
	got, err := time.Parse(time.RFC3339Nano, New("g", "x", time.Time{}, nil).OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Errorf("occurred_at = %v, want about now (%v)", got, before)
	}
}

func TestNoDataIsAnEmptyObjectNotNull(t *testing.T) {
	b, err := Encode("g", "pong", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := decode(t, b)["data"].(map[string]any); !ok || len(d) != 0 {
		t.Errorf("data = %v, want {}", decode(t, b)["data"])
	}
}

func TestEveryFrameGetsANewEventID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := New("g", "x", time.Now(), nil).EventID
		if seen[id] {
			t.Fatalf("event_id %s was made twice", id)
		}
		seen[id] = true
	}
}

func TestEncodeFailsOnDataThatCannotBeEncoded(t *testing.T) {
	if _, err := Encode("g", "x", time.Now(), make(chan int)); err == nil {
		t.Error("a channel was encoded")
	}
}

package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

type fakeEvents struct {
	calls   []string
	pushes  []service.Push
	err     error
	gotGame string
}

func (f *fakeEvents) record(gameID, call string) ([]service.Push, error) {
	f.calls = append(f.calls, call)
	f.gotGame = gameID
	return f.pushes, f.err
}

func (f *fakeEvents) FriendRequestSent(_ context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error) {
	return f.record(gameID, "request_sent "+requestID+" "+sender.ID+"/"+sender.Name+" -> "+receiverID)
}

func (f *fakeEvents) FriendRequestCancelled(_ context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error) {
	return f.record(gameID, "request_cancelled "+requestID+" "+sender.ID+"/"+sender.Name+" -> "+receiverID)
}

func (f *fakeEvents) FriendRequestDeclined(_ context.Context, gameID, requestID string, sender service.Player, receiverID string) ([]service.Push, error) {
	return f.record(gameID, "request_declined "+requestID+" "+sender.ID+"/"+sender.Name+" -> "+receiverID)
}

func (f *fakeEvents) FriendAccepted(_ context.Context, gameID, requestID string, a, b service.Player) ([]service.Push, error) {
	return f.record(gameID, "accepted "+requestID+" "+a.ID+"/"+a.Name+" "+b.ID+"/"+b.Name)
}

func (f *fakeEvents) FriendRemoved(_ context.Context, gameID, aID, bID, removedBy string) ([]service.Push, error) {
	return f.record(gameID, "removed "+aID+" "+bID+" by "+removedBy)
}

func (f *fakeEvents) PlayerRenamed(_ context.Context, gameID string, p service.Player) ([]service.Push, error) {
	return f.record(gameID, "renamed "+p.ID+"/"+p.Name)
}

func (f *fakeEvents) PlayerBlocked(_ context.Context, gameID, blockerID, blockedID string) ([]service.Push, error) {
	return f.record(gameID, "blocked "+blockerID+" -> "+blockedID)
}

func (f *fakeEvents) PlayerUnblocked(_ context.Context, gameID, blockerID, blockedID string) ([]service.Push, error) {
	return f.record(gameID, "unblocked "+blockerID+" -> "+blockedID)
}

type sent struct {
	gameID string
	ids    []string
	frame  string
}

type fakeSender struct{ got []sent }

func (f *fakeSender) SendToPlayers(gameID string, ids []string, frame []byte) {
	f.got = append(f.got, sent{gameID, ids, string(frame)})
}

func TestDispatchRoutesEachEventToItsHandler(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"friend.request_sent",
			`{"event_id":"e1","type":"friend.request_sent","occurred_at":"2026-11-10T08:00:00Z","game_id":"g-1","data":{"request_id":"r-1","sender":{"id":"u-1","name":"Alice"},"receiver":{"id":"u-2","name":"Bob"}}}`,
			"request_sent r-1 u-1/Alice -> u-2"},
		{"friend.request_sent without request_id (what Social sends today)",
			`{"event_id":"e1","type":"friend.request_sent","game_id":"g-1","data":{"sender":{"id":"u-1","name":"Alice"},"receiver":{"id":"u-2","name":"Bob"}}}`,
			"request_sent  u-1/Alice -> u-2"},
		{"friend.request_cancelled",
			`{"event_id":"e1","type":"friend.request_cancelled","game_id":"g-1","data":{"request_id":"r-1","sender":{"id":"u-1","name":"Alice"},"receiver":{"id":"u-2","name":"Bob"}}}`,
			"request_cancelled r-1 u-1/Alice -> u-2"},
		{"friend.request_declined",
			`{"event_id":"e1","type":"friend.request_declined","game_id":"g-1","data":{"request_id":"r-1","sender":{"id":"u-1","name":"Alice"},"receiver":{"id":"u-2","name":"Bob"}}}`,
			"request_declined r-1 u-1/Alice -> u-2"},
		{"friend.accepted",
			`{"event_id":"e2","type":"friend.accepted","game_id":"g-1","data":{"request_id":"r-1","players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}]}}`,
			"accepted r-1 u-1/Emma u-2/Oliver"},
		{"friend.accepted without request_id (what Social sends today)",
			`{"event_id":"e2","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}]}}`,
			"accepted  u-1/Emma u-2/Oliver"},
		{"friend.removed",
			`{"event_id":"e3","type":"friend.removed","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}],"removed_by":"u-2"}}`,
			"removed u-1 u-2 by u-2"},
		{"player.renamed",
			`{"event_id":"e4","type":"player.renamed","game_id":"g-1","data":{"player":{"id":"u-1","name":"Alice Brown"}}}`,
			"renamed u-1/Alice Brown"},
		{"player.blocked",
			`{"event_id":"e5","type":"player.blocked","game_id":"g-1","data":{"blocker_id":"u-1","blocked_id":"u-2"}}`,
			"blocked u-1 -> u-2"},
		{"player.unblocked",
			`{"event_id":"e6","type":"player.unblocked","game_id":"g-1","data":{"blocker_id":"u-1","blocked_id":"u-2"}}`,
			"unblocked u-1 -> u-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &fakeEvents{}
			err := NewDispatcher(ev, &fakeSender{}, slog.Default()).Handle(context.Background(), []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ev.calls, []string{tt.want}) {
				t.Errorf("calls = %v, want [%s]", ev.calls, tt.want)
			}
			if ev.gotGame != "g-1" {
				t.Errorf("game = %q, want the envelope's game_id", ev.gotGame)
			}
		})
	}
}

// Frames sent to browsers have the envelope of an event: a new event_id, the type, the time of the queue
// event as occurred_at, and the game. The part inside data is what the service said.
func TestDispatchSendsEveryFrameWithTheEventEnvelope(t *testing.T) {
	ev := &fakeEvents{pushes: []service.Push{
		{GameID: "g-1", PlayerIDs: []string{"u-1"}, Type: "conversation.added", Data: map[string]any{"conversation": map[string]any{"id": "c-1"}}},
		{GameID: "g-1", PlayerIDs: []string{"u-1", "u-2"}, Type: "conversation.removed", Data: map[string]any{"conversationId": "c-1"}},
	}}
	send := &fakeSender{}
	body := `{"event_id":"e1","type":"friend.removed","occurred_at":"2026-11-10T08:00:00.123Z","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}],"removed_by":"u-1"}}`
	if err := NewDispatcher(ev, send, slog.Default()).Handle(context.Background(), []byte(body)); err != nil {
		t.Fatal(err)
	}
	if len(send.got) != 2 {
		t.Fatalf("sent %d frames, want 2: %+v", len(send.got), send.got)
	}
	wantData := []string{`{"conversation":{"id":"c-1"}}`, `{"conversationId":"c-1"}`}
	wantType := []string{"conversation.added", "conversation.removed"}
	wantTo := [][]string{{"u-1"}, {"u-1", "u-2"}}
	ids := map[string]bool{}
	for i, s := range send.got {
		var f struct {
			EventID    string          `json:"event_id"`
			Type       string          `json:"type"`
			OccurredAt string          `json:"occurred_at"`
			GameID     string          `json:"game_id"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(s.frame), &f); err != nil {
			t.Fatalf("frame %d is not JSON: %v", i, err)
		}
		if s.gameID != "g-1" || !reflect.DeepEqual(s.ids, wantTo[i]) {
			t.Errorf("frame %d sent to %s %v, want g-1 %v", i, s.gameID, s.ids, wantTo[i])
		}
		if f.Type != wantType[i] || f.GameID != "g-1" || string(f.Data) != wantData[i] {
			t.Errorf("frame %d = %+v, want type %s, game g-1, data %s", i, f, wantType[i], wantData[i])
		}
		if f.OccurredAt != "2026-11-10T08:00:00.123Z" {
			t.Errorf("frame %d occurred_at = %q, want the time of the event", i, f.OccurredAt)
		}
		if len(f.EventID) != 36 || f.EventID == "e1" || ids[f.EventID] {
			t.Errorf("frame %d event_id = %q, want a new unique UUID (not the queue event's)", i, f.EventID)
		}
		ids[f.EventID] = true
	}
}

// An event without a time (older publishers) still makes frames with a valid occurred_at: now.
func TestDispatchFrameOfAnEventWithoutATimeGetsNow(t *testing.T) {
	ev := &fakeEvents{pushes: []service.Push{{GameID: "g-1", PlayerIDs: []string{"u-1"}, Type: "conversation.removed", Data: map[string]any{}}}}
	send := &fakeSender{}
	body := `{"event_id":"e1","type":"friend.removed","game_id":"g-1","data":{"players":[{"id":"u-1","name":"E"},{"id":"u-2","name":"O"}],"removed_by":"u-1"}}`
	before := time.Now().Add(-time.Second)
	if err := NewDispatcher(ev, send, slog.Default()).Handle(context.Background(), []byte(body)); err != nil {
		t.Fatal(err)
	}
	var f struct {
		OccurredAt string `json:"occurred_at"`
	}
	if err := json.Unmarshal([]byte(send.got[0].frame), &f); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, f.OccurredAt)
	if err != nil || at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("occurred_at = %q (%v), want about now", f.OccurredAt, err)
	}
}

func TestDispatchIgnoresEventsNobodyHandlesYet(t *testing.T) {
	for _, typ := range []string{"clan.created", "clan.renamed", "config.published", "something.else", ""} {
		ev, send := &fakeEvents{}, &fakeSender{}
		body, _ := json.Marshal(map[string]any{"event_id": "e", "type": typ, "game_id": "g-1", "data": map[string]any{}})
		if err := NewDispatcher(ev, send, slog.Default()).Handle(context.Background(), body); err != nil {
			t.Errorf("type %q: error %v, want it acknowledged", typ, err)
		}
		if len(ev.calls)+len(send.got) != 0 {
			t.Errorf("type %q: handled although nobody handles it", typ)
		}
	}
}

func TestDispatchDropsUnusableEvents(t *testing.T) {
	tests := []struct{ name, body string }{
		{"not json", `hello`},
		{"json but not an object", `[1,2]`},
		{"no data", `{"event_id":"e","type":"friend.accepted","game_id":"g-1"}`},
		{"data of the wrong shape", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"players":"u-1"}}`},
		{"accepted with one player", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Alice"}]}}`},
		{"accepted with three players", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"1"},{"id":"2"},{"id":"3"}]}}`},
		{"accepted in the old a/b shape", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"a":{"id":"u-1","name":"Alice"},"b":{"id":"u-2","name":"Bob"}}}`},
		{"data is a string", `{"event_id":"e","type":"friend.removed","game_id":"g-1","data":"x"}`},
		{"removed in the old a_id/b_id shape", `{"event_id":"e","type":"friend.removed","game_id":"g-1","data":{"a_id":"u-1","b_id":"u-2"}}`},
		{"removed with one player", `{"event_id":"e","type":"friend.removed","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"}],"removed_by":"u-1"}}`},
		{"blocked with no data", `{"event_id":"e","type":"player.blocked","game_id":"g-1"}`},
		{"unblocked with ids that are not strings", `{"event_id":"e","type":"player.unblocked","game_id":"g-1","data":{"blocker_id":1,"blocked_id":2}}`},
		{"cancelled with a request_id that is not a string", `{"event_id":"e","type":"friend.request_cancelled","game_id":"g-1","data":{"request_id":7,"sender":{"id":"u-1","name":"Alice"},"receiver":{"id":"u-2","name":"Bob"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, send := &fakeEvents{}, &fakeSender{}
			err := NewDispatcher(ev, send, slog.Default()).Handle(context.Background(), []byte(tt.body))
			if !errors.Is(err, ErrDrop) {
				t.Fatalf("error = %v, want ErrDrop", err)
			}
			if len(ev.calls)+len(send.got) != 0 {
				t.Error("something was handled or sent")
			}
		})
	}
}

func TestDispatchTellsInvalidFromTransientServiceErrors(t *testing.T) {
	body := []byte(`{"event_id":"e","type":"friend.removed","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}],"removed_by":"u-1"}}`)

	invalid := &fakeEvents{err: errors.Join(service.ErrInvalidEvent)}
	if err := NewDispatcher(invalid, &fakeSender{}, slog.Default()).Handle(context.Background(), body); !errors.Is(err, ErrDrop) {
		t.Errorf("invalid event: error = %v, want ErrDrop", err)
	}

	errDB := errors.New("database down")
	transient := &fakeEvents{err: errDB, pushes: []service.Push{{GameID: "g-1", PlayerIDs: []string{"u-1"}, Type: "x"}}}
	send := &fakeSender{}
	err := NewDispatcher(transient, send, slog.Default()).Handle(context.Background(), body)
	if errors.Is(err, ErrDrop) || !errors.Is(err, errDB) {
		t.Errorf("transient failure: error = %v, want the database error and no ErrDrop", err)
	}
	if len(send.got) != 0 {
		t.Error("frames were sent although the event failed")
	}
}

// These are the events exactly as Social publishes them today (services/social/internal/service/friends.go),
// built with the same pkg/events code and the same data maps.
// If Social changes a field name, this test is where Realtime finds out.
func TestDispatchReadsEventsAsSocialPublishesThem(t *testing.T) {
	alice := map[string]any{"id": "u-1", "name": "Alice"}
	bob := map[string]any{"id": "u-2", "name": "Bob"}
	tests := []struct {
		name string
		e    events.Event
		want string
	}{
		{"friend.request_sent", events.New("friend.request_sent", "g-1", map[string]any{"request_id": "r-1", "sender": alice, "receiver": bob}),
			"request_sent r-1 u-1/Alice -> u-2"},
		{"friend.request_cancelled", events.New("friend.request_cancelled", "g-1", map[string]any{"request_id": "r-1", "sender": alice, "receiver": bob}),
			"request_cancelled r-1 u-1/Alice -> u-2"},
		{"friend.request_declined", events.New("friend.request_declined", "g-1", map[string]any{"request_id": "r-1", "sender": alice, "receiver": bob}),
			"request_declined r-1 u-1/Alice -> u-2"},
		{"friend.accepted", events.New("friend.accepted", "g-1", map[string]any{"request_id": "r-1", "players": []map[string]any{alice, bob}}),
			"accepted r-1 u-1/Alice u-2/Bob"},
		{"friend.removed", events.New("friend.removed", "g-1", map[string]any{"players": []map[string]any{alice, bob}, "removed_by": "u-1"}),
			"removed u-1 u-2 by u-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.e)
			if err != nil {
				t.Fatal(err)
			}
			ev := &fakeEvents{}
			if err := NewDispatcher(ev, &fakeSender{}, slog.Default()).Handle(context.Background(), body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ev.calls, []string{tt.want}) || ev.gotGame != "g-1" {
				t.Errorf("calls = %v, game = %q, want [%s] for g-1", ev.calls, ev.gotGame, tt.want)
			}
		})
	}
}

// docs/event-messages.md is the agreed format of every event. Each message in it must be read by
// the dispatcher: the ones Realtime handles reach the service with the right fields, the others
// (clans, blocks, config) are acknowledged and ignored. Change the file and this test together.
func TestDispatchReadsEveryMessageInTheEventMessagesFile(t *testing.T) {
	doc, err := os.ReadFile("../../../../docs/event-messages.md")
	if err != nil {
		t.Fatal(err)
	}
	// On Windows git may check the file out with CRLF line endings.
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	// The part after "# WebSocket frames" holds frames of the WebSocket, not events.
	if i := bytes.Index(doc, []byte("\n# WebSocket frames")); i >= 0 {
		doc = doc[:i]
	}
	const (
		alice  = "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607"
		bob    = "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6"
		emma   = "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7"
		oliver = "9f4bc0d1-e2f3-4a45-8b56-67788990aabb"
		req    = "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	)
	want := map[string]string{ // type -> the call the service must get; "" = ignored
		"friend.request_sent":      "request_sent " + req + " " + alice + "/Alice -> " + bob,
		"friend.request_cancelled": "request_cancelled " + req + " " + alice + "/Alice -> " + bob,
		"friend.request_declined":  "request_declined " + req + " " + alice + "/Alice -> " + bob,
		"friend.accepted":          "accepted " + req + " " + emma + "/Emma " + oliver + "/Oliver",
		"friend.removed":           "removed " + emma + " " + oliver + " by " + emma,
		"player.renamed":           "renamed " + alice + "/Alice Brown",
		"clan.created":             "", "clan.member_joined": "", "clan.member_left": "", "clan.renamed": "", "clan.disbanded": "",
		"clan.member_role_changed": "", // published by social (TEAM-169); realtime does not handle it yet
		"player.blocked": "blocked " + alice + " -> " + bob, "player.unblocked": "unblocked " + alice + " -> " + bob, "config.published": "",
	}

	blocks := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllSubmatch(doc, -1)
	seen := map[string]bool{}
	for _, b := range blocks {
		var env events.Event
		if err := json.Unmarshal(b[1], &env); err != nil {
			t.Fatalf("a message in the file is not an event: %v\n%s", err, b[1])
		}
		seen[env.Type] = true
		t.Run(env.Type, func(t *testing.T) {
			wantCall, known := want[env.Type]
			if !known {
				t.Fatalf("event type %q is in the file but not in this test", env.Type)
			}
			ev := &fakeEvents{}
			if err := NewDispatcher(ev, &fakeSender{}, slog.Default()).Handle(context.Background(), b[1]); err != nil {
				t.Fatalf("the dispatcher rejects the message of the file: %v", err)
			}
			var calls []string
			if wantCall != "" {
				calls = []string{wantCall}
			}
			if !reflect.DeepEqual(ev.calls, calls) {
				t.Errorf("calls = %v, want %v", ev.calls, calls)
			}
		})
	}
	for typ := range want {
		if !seen[typ] {
			t.Errorf("event type %q is expected but missing in docs/event-messages.md", typ)
		}
	}
}

// fakeConfig stands in for the config cache: it answers Published from fields and records the calls.
type fakeConfig struct {
	current int
	send    bool
	err     error
	calls   []string
}

func (f *fakeConfig) Published(_ context.Context, gameID string, version int) (int, bool, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s %d", gameID, version))
	return f.current, f.send, f.err
}

type toGame struct{ gameID, frame string }

type fakeBroadcaster struct{ got []toGame }

func (f *fakeBroadcaster) SendToGame(gameID string, frame []byte) {
	f.got = append(f.got, toGame{gameID, string(frame)})
}

const gameUUID = "11111111-1111-4111-8111-111111111111"

func configPublished(version any) []byte {
	b, _ := json.Marshal(map[string]any{"event_id": "e9", "type": "config.published", "occurred_at": "2026-11-10T09:42:45.030Z", "game_id": gameUUID, "data": map[string]any{"version": version}})
	return b
}

func TestConfigPublishedTellsEveryConnectionOfTheGame(t *testing.T) {
	cache, to, send := &fakeConfig{current: 5, send: true}, &fakeBroadcaster{}, &fakeSender{}
	d := NewDispatcher(&fakeEvents{}, send, slog.Default()).WithConfig(cache, to)
	if err := d.Handle(context.Background(), configPublished(5)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cache.calls, []string{gameUUID + " 5"}) {
		t.Errorf("the cache was asked %v, want the game and version of the event", cache.calls)
	}
	if len(to.got) != 1 || to.got[0].gameID != gameUUID || len(send.got) != 0 {
		t.Fatalf("broadcast = %+v, to players = %+v, want one frame to the whole game", to.got, send.got)
	}
	var f struct {
		EventID    string         `json:"event_id"`
		Type       string         `json:"type"`
		OccurredAt string         `json:"occurred_at"`
		GameID     string         `json:"game_id"`
		Data       map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(to.got[0].frame), &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != "config.updated" || f.GameID != gameUUID || f.OccurredAt != "2026-11-10T09:42:45.030Z" || len(f.EventID) != 36 ||
		len(f.Data) != 1 || f.Data["version"] != float64(5) {
		t.Errorf("frame = %+v, want config.updated {version: 5} with the envelope of the event", f)
	}
}

// The frame names the version the cache holds, which can be newer than the one in the event.
func TestConfigPublishedTellsTheVersionTheCacheHolds(t *testing.T) {
	to := &fakeBroadcaster{}
	d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(&fakeConfig{current: 6, send: true}, to)
	if err := d.Handle(context.Background(), configPublished(5)); err != nil {
		t.Fatal(err)
	}
	if len(to.got) != 1 || !strings.Contains(to.got[0].frame, `"data":{"version":6}`) {
		t.Errorf("broadcast = %+v, want version 6", to.got)
	}
}

func TestConfigPublishedThatIsOutOfOrderSendsNothing(t *testing.T) {
	to := &fakeBroadcaster{}
	d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(&fakeConfig{current: 6, send: false}, to)
	if err := d.Handle(context.Background(), configPublished(5)); err != nil {
		t.Fatalf("error %v, want the event acknowledged", err)
	}
	if len(to.got) != 0 {
		t.Errorf("sent %+v for a version the players were told about already", to.got)
	}
}

// When Platform cannot give the config the event is tried again (an error that is not ErrDrop), nobody is told
// about a config that was not loaded.
func TestConfigPublishedKeepsTheEventForARetryWhenTheFetchFails(t *testing.T) {
	for _, cause := range []error{gameconfig.ErrUnavailable, gameconfig.ErrStale, gameconfig.ErrNotPublished, errors.New("boom")} {
		to := &fakeBroadcaster{}
		d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(&fakeConfig{err: cause}, to)
		err := d.Handle(context.Background(), configPublished(5))
		if err == nil || errors.Is(err, ErrDrop) {
			t.Errorf("cause %v: error = %v, want a retryable error", cause, err)
		}
		if len(to.got) != 0 {
			t.Errorf("cause %v: told the players although the config was not loaded", cause)
		}
	}
}

func TestConfigPublishedDropsUnusableEvents(t *testing.T) {
	bad := map[string][]byte{
		"no version":      configPublished(nil),
		"version 0":       configPublished(0),
		"negative":        configPublished(-1),
		"a string":        configPublished("5"),
		"a decimal":       configPublished(1.5),
		"not an event":    []byte(`{"type":"config.published"`),
		"no data at all":  []byte(`{"event_id":"e","type":"config.published","game_id":"` + gameUUID + `"}`),
		"empty data":      []byte(`{"event_id":"e","type":"config.published","game_id":"` + gameUUID + `","data":{}}`),
		"invalid game id": nil,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			cache, to := &fakeConfig{current: 1, send: true}, &fakeBroadcaster{}
			if body == nil { // the cache refuses the game ID
				cache.err = gameconfig.ErrInvalidGame
				body = configPublished(5)
			}
			err := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(cache, to).Handle(context.Background(), body)
			if !errors.Is(err, ErrDrop) {
				t.Errorf("error = %v, want ErrDrop", err)
			}
			if len(to.got) != 0 {
				t.Errorf("sent %+v for an unusable event", to.got)
			}
		})
	}
}

func TestConfigPublishedIsIgnoredWithoutACache(t *testing.T) {
	to := &fakeBroadcaster{}
	if err := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).Handle(context.Background(), configPublished(5)); err != nil {
		t.Errorf("error %v, want the event acknowledged", err)
	}
	if len(to.got) != 0 {
		t.Error("sent a frame without a cache")
	}
}

// The example in docs/event-messages.md is what Platform will publish; the dispatcher must take it as it is.
func TestConfigPublishedReadsTheMessageOfTheEventMessagesFile(t *testing.T) {
	doc, err := os.ReadFile("../../../../docs/event-messages.md")
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	i := bytes.Index(doc, []byte("## config.published"))
	m := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindSubmatch(doc[i:])
	if i < 0 || m == nil {
		t.Fatal("no config.published message in the file")
	}
	cache, to := &fakeConfig{current: 5, send: true}, &fakeBroadcaster{}
	d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(cache, to)
	if err := d.Handle(context.Background(), m[1]); err != nil {
		t.Fatal(err)
	}
	if len(cache.calls) != 1 || !strings.HasSuffix(cache.calls[0], " 5") || len(to.got) != 1 {
		t.Errorf("cache calls %v, frames %v, want one refresh of version 5 and one frame", cache.calls, to.got)
	}
}

package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
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

func TestDispatchSendsEveryFrameEncodedAsAnEnvelope(t *testing.T) {
	ev := &fakeEvents{pushes: []service.Push{
		{GameID: "g-1", PlayerIDs: []string{"u-1"}, Type: "conversation.added", Data: map[string]any{"conversation": map[string]any{"id": "c-1"}}},
		{GameID: "g-1", PlayerIDs: []string{"u-1", "u-2"}, Type: "conversation.removed", Data: map[string]any{"conversationId": "c-1"}},
	}}
	send := &fakeSender{}
	body := `{"event_id":"e1","type":"friend.removed","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}],"removed_by":"u-1"}}`
	if err := NewDispatcher(ev, send, slog.Default()).Handle(context.Background(), []byte(body)); err != nil {
		t.Fatal(err)
	}
	want := []sent{
		{"g-1", []string{"u-1"}, `{"type":"conversation.added","data":{"conversation":{"id":"c-1"}}}`},
		{"g-1", []string{"u-1", "u-2"}, `{"type":"conversation.removed","data":{"conversationId":"c-1"}}`},
	}
	if !reflect.DeepEqual(send.got, want) {
		t.Errorf("sent = %+v\nwant %+v", send.got, want)
	}
}

func TestDispatchIgnoresEventsNobodyHandlesYet(t *testing.T) {
	for _, typ := range []string{"clan.created", "clan.renamed", "player.blocked", "player.unblocked", "config.published", "something.else", ""} {
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
		"player.blocked": "", "player.unblocked": "", "config.published": "",
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

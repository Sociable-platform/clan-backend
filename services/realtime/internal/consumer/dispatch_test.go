package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
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

func (f *fakeEvents) FriendRequestSent(_ context.Context, gameID, requestID string, from service.Player, toID string) ([]service.Push, error) {
	f.calls = append(f.calls, "request_sent "+requestID+" "+from.ID+"/"+from.Name+" -> "+toID)
	f.gotGame = gameID
	return f.pushes, f.err
}

func (f *fakeEvents) FriendAccepted(_ context.Context, gameID string, a, b service.Player) ([]service.Push, error) {
	f.calls = append(f.calls, "accepted "+a.ID+"/"+a.Name+" "+b.ID+"/"+b.Name)
	f.gotGame = gameID
	return f.pushes, f.err
}

func (f *fakeEvents) FriendRemoved(_ context.Context, gameID, aID, bID string) ([]service.Push, error) {
	f.calls = append(f.calls, "removed "+aID+" "+bID)
	f.gotGame = gameID
	return f.pushes, f.err
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
			`{"event_id":"e1","type":"friend.request_sent","occurred_at":"2026-11-10T08:00:00Z","game_id":"g-1","data":{"request_id":"r-1","sender":{"id":"u-1","name":"Ali"},"receiver":{"id":"u-2","name":"Rashad"}}}`,
			"request_sent r-1 u-1/Ali -> u-2"},
		{"friend.request_sent without request_id (what Social sends today)",
			`{"event_id":"e1","type":"friend.request_sent","game_id":"g-1","data":{"sender":{"id":"u-1","name":"Ali"},"receiver":{"id":"u-2","name":"Rashad"}}}`,
			"request_sent  u-1/Ali -> u-2"},
		{"friend.accepted",
			`{"event_id":"e2","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Ali"},{"id":"u-2","name":"Rashad"}]}}`,
			"accepted u-1/Ali u-2/Rashad"},
		{"friend.removed",
			`{"event_id":"e3","type":"friend.removed","game_id":"g-1","data":{"a_id":"u-1","b_id":"u-2"}}`,
			"removed u-1 u-2"},
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
	body := `{"event_id":"e1","type":"friend.removed","game_id":"g-1","data":{"a_id":"u-1","b_id":"u-2"}}`
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
	for _, typ := range []string{"clan.created", "player.blocked", "config.published", "something.else", ""} {
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
		{"accepted with one player", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Ali"}]}}`},
		{"accepted with three players", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"1"},{"id":"2"},{"id":"3"}]}}`},
		{"accepted in the old a/b shape", `{"event_id":"e","type":"friend.accepted","game_id":"g-1","data":{"a":{"id":"u-1","name":"Ali"},"b":{"id":"u-2","name":"Rashad"}}}`},
		{"data is a string", `{"event_id":"e","type":"friend.removed","game_id":"g-1","data":"x"}`},
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
	body := []byte(`{"event_id":"e","type":"friend.removed","game_id":"g-1","data":{"a_id":"u-1","b_id":"u-2"}}`)

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

// These are the events exactly as Social publishes them today (services/social/internal/service/friends.go,
// README "Event format"), built with the same pkg/events code and the same data maps. If Social changes a
// field name, this test is where Realtime finds out.
func TestDispatchReadsEventsAsSocialPublishesThem(t *testing.T) {
	ali := map[string]any{"id": "u-1", "name": "Ali"}
	rashad := map[string]any{"id": "u-2", "name": "Rashad"}
	tests := []struct {
		name string
		e    events.Event
		want string
	}{
		{"friend.request_sent", events.New("friend.request_sent", "g-1", map[string]any{"sender": ali, "receiver": rashad}),
			"request_sent  u-1/Ali -> u-2"},
		{"friend.accepted", events.New("friend.accepted", "g-1", map[string]any{"players": []map[string]any{ali, rashad}}),
			"accepted u-1/Ali u-2/Rashad"},
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

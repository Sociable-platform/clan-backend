package handler

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var (
	uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	timePattern   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
)

// readRaw reads the next frame as the whole JSON object, envelope included.
func readRaw(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("server sent a frame that is not JSON: %q", b)
	}
	return f
}

// checkEnvelope fails unless f is a frame of type typ with exactly the fields of an event
// (event_id, type, occurred_at, game_id, data) and valid values. It returns the event_id.
func checkEnvelope(t *testing.T, f map[string]any, typ string) string {
	t.Helper()
	got := make([]string, 0, len(f))
	for k := range f {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := []string{"data", "event_id", "game_id", "occurred_at", "type"}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s frame has the fields %v, want %v", typ, got, want)
	}
	if f["type"] != typ {
		t.Errorf("type = %v, want %s", f["type"], typ)
	}
	id, _ := f["event_id"].(string)
	if !uuidV4Pattern.MatchString(id) {
		t.Errorf("%s event_id = %q, want a UUID v4", typ, f["event_id"])
	}
	at, _ := f["occurred_at"].(string)
	if !timePattern.MatchString(at) {
		t.Errorf("%s occurred_at = %q, want UTC with three decimals", typ, f["occurred_at"])
	}
	if f["game_id"] != testGameID {
		t.Errorf("%s game_id = %v, want the game of the token (%s)", typ, f["game_id"], testGameID)
	}
	if _, ok := f["data"].(map[string]any); !ok {
		t.Errorf("%s data = %v, want an object", typ, f["data"])
	}
	return id
}

func occurredAt(t *testing.T, f map[string]any) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, f["occurred_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// Every frame the server sends has the envelope of an event: hello, pong, error, and the chat frames.
func TestWSEveryServerFrameHasTheEventEnvelope(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.storing(u42)
	start := time.Now().Add(-time.Second)

	c, resp, err := env.dial(t, env.playerToken(t), "")
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	ids := map[string]string{}

	hello := readRaw(t, c)
	ids["hello"] = checkEnvelope(t, hello, "hello")
	if at := occurredAt(t, hello); at.Before(start) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("hello occurred_at = %v, want about now", at)
	}
	if hello["data"].(map[string]any)["playerId"] != u42 {
		t.Errorf("hello data = %v", hello["data"])
	}

	write(t, c, `{"type":"ping","id":"p-1","data":{}}`)
	pong := readRaw(t, c)
	ids["pong"] = checkEnvelope(t, pong, "pong")
	if pong["data"].(map[string]any)["replyTo"] != "p-1" {
		t.Errorf("pong data = %v, want replyTo p-1 (the reply id stays in data)", pong["data"])
	}

	write(t, c, `{"type":"nope","id":"x-1"}`)
	errFrame := readRaw(t, c)
	ids["error"] = checkEnvelope(t, errFrame, "error")
	if d := errFrame["data"].(map[string]any); d["code"] != "bad_frame" || d["replyTo"] != "x-1" {
		t.Errorf("error data = %v", d)
	}

	write(t, c, sendFrame("c-1", convID, "gg"))
	ack := readRaw(t, c)
	ids["ack"] = checkEnvelope(t, ack, "message.ack")
	msgNew := readRaw(t, c)
	ids["new"] = checkEnvelope(t, msgNew, "message.new")

	// A message.ack and the message.new for it happened when the message was stored: its time.
	const stored = "2026-11-10T08:00:00.000Z"
	if ack["occurred_at"] != stored || msgNew["occurred_at"] != stored {
		t.Errorf("ack/new occurred_at = %v / %v, want the time the message was stored (%s)", ack["occurred_at"], msgNew["occurred_at"], stored)
	}
	if ack["data"].(map[string]any)["replyTo"] != "c-1" {
		t.Errorf("ack data = %v", ack["data"])
	}

	seen := map[string]string{}
	for name, id := range ids {
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s share the event_id %s: every frame needs its own", name, other, id)
		}
		seen[id] = name
	}
}

// One message.new is encoded once and sent to every tab of every member: they all get the same event_id.
func TestWSMessageNewHasOneEventIDForEveryRecipient(t *testing.T) {
	env := newWSEnv(t, nil)
	aliceTab1, aliceHello := env.connect(t, env.tokenFor(t, "alice"))
	aliceTab2, _ := env.connect(t, env.tokenFor(t, "alice"))
	bob, bobHello := env.connect(t, env.tokenFor(t, "bob"))
	env.chat.storing(aliceHello["playerId"].(string), bobHello["playerId"].(string))

	write(t, aliceTab1, sendFrame("c-1", convID, "hi"))
	readRaw(t, aliceTab1) // the ack
	var ids []string
	for _, c := range []*websocket.Conn{aliceTab1, aliceTab2, bob} {
		f := readRaw(t, c)
		ids = append(ids, checkEnvelope(t, f, "message.new"))
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Errorf("event_ids of the same message.new = %v, want one for all recipients", ids)
	}
}

// A game_id in a frame the client sends is ignored: the frames carry the token's game, always.
func TestWSFramesCarryTheGameOfTheTokenNotOfTheClientFrame(t *testing.T) {
	env := newWSEnv(t, nil)
	c := env.mustDial(t)
	write(t, c, `{"type":"ping","id":"p-1","game_id":"22222222-2222-4222-8222-222222222222","data":{"game_id":"22222222-2222-4222-8222-222222222222"}}`)
	checkEnvelope(t, readRaw(t, c), "pong") // checkEnvelope requires game_id == testGameID
}

// The envelope is added to frames, not to the replies of the REST endpoints.
func TestRESTResponsesDoNotGetTheFrameEnvelope(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.list = nil
	_, body := env.get(t, "/health", "", "")
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["event_id"]; has {
		t.Errorf("/health = %s: REST responses are not frames", body)
	}
}

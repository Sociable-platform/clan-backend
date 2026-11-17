package handler

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// jsonKeys returns the JSON field names of a struct, from its tags, sorted.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return keysOf(m)
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func object(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object: %v", what, v)
	}
	return m
}

func wantKeys(t *testing.T, got map[string]any, want []string, what string) {
	t.Helper()
	sort.Strings(want)
	if !reflect.DeepEqual(keysOf(got), want) {
		t.Errorf("%s has the fields %v, want %v", what, keysOf(got), want)
	}
}

// The second part of docs/event-messages.md ("# WebSocket frames") holds the frames of the clan
// chat rooms, of the live re-skin (config.updated) and of presence (presence.update). Each must be well formed and have the fields the frames have in the code (the same
// Go types the chat and conversation frames are made from), and the clan notifications the fields
// of the table in openapi/realtime-ws.md. Change the file and this test together.
func TestFrameTemplatesInTheEventMessagesFile(t *testing.T) {
	doc, err := os.ReadFile("../../../../docs/event-messages.md")
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n")) // Windows checkouts
	i := bytes.Index(doc, []byte("\n# WebSocket frames"))
	if i < 0 {
		t.Fatal(`docs/event-messages.md has no "# WebSocket frames" part`)
	}
	blocks := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllSubmatch(doc[i:], -1)
	if len(blocks) == 0 {
		t.Fatal("no frames in the WebSocket frames part")
	}

	envelopeKeys := jsonKeys(t, frame.New("g", "x", time.Now(), nil)) // the same as events.Event, see frame_test.go
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	conversationKeys := jsonKeys(t, service.Conversation{})
	memberKeys := jsonKeys(t, service.ConversationMember{})
	messageKeys := jsonKeys(t, service.Message{})
	seen := map[string]bool{}

	for _, b := range blocks {
		var f map[string]any
		if err := json.Unmarshal(b[1], &f); err != nil {
			t.Fatalf("a frame is not JSON: %v\n%s", err, b[1])
		}
		typ, _ := f["type"].(string)
		data := object(t, f["data"], typ+" data")
		seen[typ] = true

		if typ == "message.send" {
			wantKeys(t, f, []string{"type", "id", "data"}, typ+" (a client frame has no envelope)")
		} else {
			// Every frame the server sends has the envelope of an event.
			wantKeys(t, f, append([]string(nil), envelopeKeys...), typ+" (a server frame has the event envelope)")
			if id, _ := f["event_id"].(string); !uuid.MatchString(id) {
				t.Errorf("%s event_id = %v, want a UUID", typ, f["event_id"])
			}
			if gid, _ := f["game_id"].(string); !uuid.MatchString(gid) {
				t.Errorf("%s game_id = %v, want a UUID", typ, f["game_id"])
			}
			if at, _ := f["occurred_at"].(string); !timePattern.MatchString(at) {
				t.Errorf("%s occurred_at = %v, want UTC with three decimals", typ, f["occurred_at"])
			}
		}

		switch typ {
		case "conversation.added":
			wantKeys(t, data, []string{"conversation"}, typ+" data")
			conv := object(t, data["conversation"], "conversation")
			wantKeys(t, conv, conversationKeys, "conversation")
			if conv["kind"] != "clan" || conv["clanId"] == nil {
				t.Errorf("a clan room has kind clan and a clanId: %v %v", conv["kind"], conv["clanId"])
			}
			members, _ := conv["members"].([]any)
			if len(members) == 0 {
				t.Error("a clan room has at least one member")
			}
			for _, m := range members {
				wantKeys(t, object(t, m, "member"), memberKeys, "member")
			}
			if last := conv["lastMessage"]; last != nil {
				wantKeys(t, object(t, last, "lastMessage"), messageKeys, "lastMessage")
			}
		case "conversation.removed":
			wantKeys(t, data, []string{"conversationId"}, typ+" data")
		case "notification":
			wantKeys(t, data, []string{"kind", "payload"}, "notification data")
			payload := object(t, data["payload"], "payload")
			switch kind := data["kind"]; kind {
			case "clan_joined":
				wantKeys(t, payload, []string{"clanId", "player"}, "clan_joined payload")
				wantKeys(t, object(t, payload["player"], "player"), []string{"id", "name"}, "clan_joined player")
			case "clan_kicked", "clan_disbanded":
				wantKeys(t, payload, []string{"clanId"}, kind.(string)+" payload")
			case "clan_join_request":
				wantKeys(t, payload, []string{"clanId", "clanName", "requestId", "player"}, "clan_join_request payload")
				wantKeys(t, object(t, payload["player"], "player"), []string{"id", "name"}, "clan_join_request player")
			case "clan_join_declined":
				wantKeys(t, payload, []string{"clanId", "clanName"}, "clan_join_declined payload")
			default:
				t.Errorf("notification kind %v is not a clan notification", kind)
			}
			seen["notification/"+data["kind"].(string)] = true
		case "config.updated":
			wantKeys(t, data, []string{"version"}, typ+" data")
			if v, _ := data["version"].(float64); v < 1 || v != float64(int(v)) {
				t.Errorf("config.updated version = %v, want a whole number from 1", data["version"])
			}
		case "presence.update":
			wantKeys(t, data, []string{"playerId", "status"}, typ+" data")
			if id, _ := data["playerId"].(string); !uuid.MatchString(id) {
				t.Errorf("presence.update playerId = %v, want a UUID", data["playerId"])
			}
			if data["status"] != "online" && data["status"] != "offline" {
				t.Errorf("presence.update status = %v, want online or offline", data["status"])
			}
		case "message.send":
			wantKeys(t, data, jsonKeys(t, messageSendData{ConversationID: new(string), Body: new(string)}), typ+" data")
		case "message.ack":
			wantKeys(t, data, jsonKeys(t, messageAckData{}), typ+" data")
		case "message.new":
			wantKeys(t, data, jsonKeys(t, messageNewData{}), typ+" data")
			msg := object(t, data["message"], "message")
			keys := append([]string(nil), messageKeys...)
			if by, ok := msg["kickedBy"]; ok {
				// Only a member_kicked line has kickedBy: who removed the player.
				if msg["kind"] != service.MessageMemberKicked {
					t.Errorf("a %v message has kickedBy, only member_kicked does", msg["kind"])
				}
				wantKeys(t, object(t, by, "kickedBy"), []string{"id", "name"}, "kickedBy")
				keys = append(keys, "kickedBy")
			}
			wantKeys(t, msg, keys, "message")
			switch msg["kind"] {
			case service.MessageText:
			case service.MessageMemberJoined, service.MessageMemberLeft, service.MessageMemberKicked:
				if msg["body"] != "" {
					t.Errorf("a %v line has the body %q, want empty", msg["kind"], msg["body"])
				}
			default:
				t.Errorf("message kind = %v, want text or a member line", msg["kind"])
			}
		default:
			t.Errorf("frame type %q is in the file but not in this test", typ)
		}
	}
	for _, want := range []string{"conversation.added", "conversation.removed", "notification/clan_joined", "notification/clan_kicked",
		"notification/clan_disbanded", "config.updated", "presence.update", "message.send", "message.ack", "message.new"} {
		if !seen[want] {
			t.Errorf("the frame %q is missing from the WebSocket frames part", want)
		}
	}
}

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// shape describes the fields of a JSON value and how they nest, ignoring the values:
// {"a":1,"b":{"c":2}} is "{a,b{c}}". An array is described by its first element, "[...]".
func shape(v any) string {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + strings.TrimPrefix(shape(v[k]), "-")
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		return "[" + shape(v[0]) + "]"
	default:
		return "-"
	}
}

// docShapes returns, for every message of docs/event-messages.md, the shape of its data.
func docShapes(t *testing.T) map[string]string {
	t.Helper()
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
	out := map[string]string{}
	for _, b := range regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllSubmatch(doc, -1) {
		var msg struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b[1], &msg); err != nil {
			t.Fatal(err)
		}
		out[msg.Type] = shape(msg.Data)
	}
	return out
}

// publishedShape sends the event through JSON, as the publisher does, and returns the shape of its data.
func publishedShape(t *testing.T, e events.Event) string {
	t.Helper()
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	return shape(back.Data)
}

// Every friend event Social publishes must have exactly the fields of the same event in
// docs/event-messages.md: no field missing, none extra, nested the same way. Realtime reads
// that file in its own tests, so a change to either side fails a test on that side.
func TestFriendEventsHaveTheFieldsOfTheEventMessagesFile(t *testing.T) {
	const reqID = "aaaaaaaa-0000-4000-8000-000000000001"
	ali := repository.FriendPlayer{ID: me, DisplayName: "Ali"}
	aysel := repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"}
	pair := repository.Friendship{PlayerA: aysel, PlayerB: ali, RequestID: reqID}
	ended := repository.FriendRequest{ID: reqID, Sender: aysel, Receiver: ali}
	ctx := context.Background()

	actions := map[string]func(*friendStore, *Service) error{
		EventFriendRequestSent: func(st *friendStore, s *Service) error {
			st.send = repository.SendFriendRequestResult{Request: &repository.FriendRequest{ID: reqID, Sender: ali, Receiver: aysel}}
			_, err := s.SendFriendRequest(ctx, gameA, "u_1", "Ali", otherID)
			return err
		},
		EventFriendAccepted: func(st *friendStore, s *Service) error {
			st.accept = pair
			_, err := s.AcceptFriendRequest(ctx, gameA, "u_1", "Ali", reqID)
			return err
		},
		EventFriendRequestCancelled: func(st *friendStore, s *Service) error {
			st.ended = ended
			return s.CancelFriendRequest(ctx, gameA, "u_1", "Ali", reqID)
		},
		EventFriendRequestDeclined: func(st *friendStore, s *Service) error {
			st.ended = ended
			return s.DeclineFriendRequest(ctx, gameA, "u_1", "Ali", reqID)
		},
		EventFriendRemoved: func(st *friendStore, s *Service) error {
			st.removed = pair
			return s.RemoveFriend(ctx, gameA, "u_1", "Ali", otherID)
		},
	}

	want := docShapes(t)
	for typ, act := range actions {
		t.Run(typ, func(t *testing.T) {
			store := newFriendStore()
			svc, rec := friendService(store)
			if err := act(store, svc); err != nil {
				t.Fatal(err)
			}
			evs := rec.Events()
			if len(evs) != 1 || evs[0].Type != typ {
				t.Fatalf("events = %+v, want one %s", evs, typ)
			}
			wantShape, ok := want[typ]
			if !ok {
				t.Fatalf("%s is not in docs/event-messages.md", typ)
			}
			if got := publishedShape(t, evs[0]); got != wantShape {
				t.Errorf("fields of %s:\n  published %s\n  file      %s", typ, got, wantShape)
			}
		})
	}
}

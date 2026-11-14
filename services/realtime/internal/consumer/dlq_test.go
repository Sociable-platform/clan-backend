package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const rejectedMsg = "event rejected to the dead-letter queue"

// jsonConsumer is a consumer whose log lines can be read back as JSON objects.
func jsonConsumer(t *testing.T, h Handler) (*Consumer, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	c, err := New(Config{URL: "amqp://clan:secret-pw@broker:5672/", RetryDelay: time.Millisecond}, h,
		slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	return c, &logs
}

func logLines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEveryRejectionIsLoggedOnceWithTheEventID(t *testing.T) {
	errDB := errors.New("database down")
	event := `{"event_id":"0f8fad5b-d9cb-469f-a165-70867728950e","type":"friend.accepted","game_id":"g"}`
	tests := []struct {
		name        string
		err         error
		redelivered bool
		delivery    amqp.Delivery
		wantID      string
		wantType    string
		wantReason  string
		wantLevel   string
	}{
		{"unusable event", fmt.Errorf("%w: no data", ErrDrop), false,
			amqp.Delivery{RoutingKey: "friend.accepted", Body: []byte(event)},
			"0f8fad5b-d9cb-469f-a165-70867728950e", "friend.accepted", reasonUnusable, "WARN"},
		{"failed twice", errDB, true,
			amqp.Delivery{RoutingKey: "friend.accepted", Body: []byte(event)},
			"0f8fad5b-d9cb-469f-a165-70867728950e", "friend.accepted", reasonFailedTwice, "ERROR"},
		{"a body that is not JSON: the ID and type come from the message properties", fmt.Errorf("%w: the body is not an event", ErrDrop), false,
			amqp.Delivery{RoutingKey: "clan.created", MessageId: "6ba7b810-9dad-41d1-80b4-00c04fd430c8", Type: "clan.created", Body: []byte(`not json`)},
			"6ba7b810-9dad-41d1-80b4-00c04fd430c8", "clan.created", reasonUnusable, "WARN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, logs := jsonConsumer(t, handlerFunc(func(context.Context, []byte) error { return tt.err }))
			d := tt.delivery
			d.Acknowledger, d.DeliveryTag, d.Redelivered = &acker{}, 1, tt.redelivered
			c.handle(context.Background(), d)

			var rejected []map[string]any
			for _, l := range logLines(t, logs) {
				if l["msg"] == rejectedMsg {
					rejected = append(rejected, l)
				}
			}
			if len(rejected) != 1 {
				t.Fatalf("%d rejection lines, want exactly 1; logs:\n%s", len(rejected), logs.String())
			}
			l := rejected[0]
			if l["event_id"] != tt.wantID || l["type"] != tt.wantType || l["routing_key"] != d.RoutingKey ||
				l["reason"] != tt.wantReason || l["level"] != tt.wantLevel || l["dead_letter_queue"] != "realtime.events.dead" {
				t.Errorf("line = %v; want event_id %s, type %s, routing_key %s, reason %s, level %s and the DLQ",
					l, tt.wantID, tt.wantType, d.RoutingKey, tt.wantReason, tt.wantLevel)
			}
			if l["error"] == nil || l["error"] == "" {
				t.Errorf("line = %v, want the error that caused it", l)
			}
		})
	}
}

func TestARetryIsNotLoggedAsARejection(t *testing.T) {
	c, logs := jsonConsumer(t, handlerFunc(func(context.Context, []byte) error { return errors.New("database down") }))
	c.handle(context.Background(), amqp.Delivery{Acknowledger: &acker{}, DeliveryTag: 1, MessageId: "e-1", Body: []byte(`{}`)})
	for _, l := range logLines(t, logs) {
		if l["msg"] == rejectedMsg {
			t.Fatalf("a first failure (requeued) was logged as a rejection: %v", l)
		}
		if l["msg"] == "event failed, will try once more" && l["event_id"] != "e-1" {
			t.Errorf("retry line = %v, want the event_id", l)
		}
	}
}

// fakeCounter answers count with fixed values and records the queue it was asked about.
type fakeCounter struct {
	n     int
	err   error
	asked []string
}

func (f *fakeCounter) count(queue string) (int, error) {
	f.asked = append(f.asked, queue)
	return f.n, f.err
}

func TestCheckDeadLettersWarnsOnlyWhenThereIsSomethingToSee(t *testing.T) {
	tests := []struct {
		name     string
		counter  *fakeCounter
		wantMsg  string // "" means nothing is logged
		wantMore string // another field the line must have
	}{
		{"empty queue logs nothing", &fakeCounter{n: 0}, "", ""},
		{"events in the queue", &fakeCounter{n: 3}, "dead-letter queue holds rejected events", `"count":3`},
		{"missing queue", &fakeCounter{err: &amqp.Error{Code: amqp.NotFound, Reason: "NOT_FOUND - no queue"}},
			"dead-letter queue not found, rejected events are deleted; run rabbitmq-setup", ""},
		{"broker error", &fakeCounter{err: errors.New("channel closed")}, "could not count the dead-letter queue", "channel closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, logs := jsonConsumer(t, nil)
			c.checkDeadLetters(tt.counter)

			if len(tt.counter.asked) != 1 || tt.counter.asked[0] != "realtime.events.dead" {
				t.Errorf("counted %v, want realtime.events.dead", tt.counter.asked)
			}
			lines := logLines(t, logs)
			if tt.wantMsg == "" {
				if len(lines) != 0 {
					t.Errorf("logged %v, want nothing", lines)
				}
				return
			}
			if len(lines) != 1 || lines[0]["msg"] != tt.wantMsg || lines[0]["level"] != "WARN" {
				t.Fatalf("logged %v, want one warning %q", lines, tt.wantMsg)
			}
			if tt.wantMore != "" && !strings.Contains(logs.String(), tt.wantMore) {
				t.Errorf("log %s does not contain %s", logs.String(), tt.wantMore)
			}
		})
	}
}

func TestDeadLetterDefaults(t *testing.T) {
	got := Config{}.withDefaults()
	if got.DeadLetterQueue != "realtime.events.dead" || got.DeadLetterCheck != DefaultDeadLetterCheck {
		t.Errorf("defaults = %q, %s; want realtime.events.dead and %s", got.DeadLetterQueue, got.DeadLetterCheck, DefaultDeadLetterCheck)
	}
	got = Config{Queue: "other", DeadLetterCheck: time.Minute}.withDefaults()
	if got.DeadLetterQueue != "other.dead" || got.DeadLetterCheck != time.Minute {
		t.Errorf("custom = %q, %s; want other.dead and 1m", got.DeadLetterQueue, got.DeadLetterCheck)
	}
}

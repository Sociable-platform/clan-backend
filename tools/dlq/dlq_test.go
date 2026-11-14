package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakeBroker holds queues of messages. A message taken with get waits until it is acked (gone)
// or nacked with requeue (back at the head of its queue), like RabbitMQ.
type fakeBroker struct {
	queues    map[string][]amqp.Delivery
	published []published
	failOn    string // a message ID whose publish fails
	countErr  map[string]error
	nextTag   uint64
	outcome   map[uint64]string // delivery tag -> "ack" or "requeue"
}

type published struct {
	exchange, key string
	msg           amqp.Publishing
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{queues: map[string][]amqp.Delivery{}, outcome: map[uint64]string{}, countErr: map[string]error{}}
}

// add puts a dead event in queue, the way RabbitMQ dead-letters it from realtime.events.
func (f *fakeBroker) add(queue, id, typ string) {
	f.queues[queue] = append(f.queues[queue], amqp.Delivery{
		MessageId: id, Type: typ, ContentType: "application/json", RoutingKey: "realtime.events",
		Body: []byte(`{"event_id":"` + id + `","type":"` + typ + `","game_id":"g-1"}`),
		Headers: amqp.Table{"x-death": []any{amqp.Table{
			"queue": "realtime.events", "reason": "rejected", "count": int64(2),
			"time": time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), "routing-keys": []any{typ}, "exchange": "socialkit.events",
		}}},
	})
}

func (f *fakeBroker) count(queue string) (int, error) {
	if err := f.countErr[queue]; err != nil {
		return 0, err
	}
	return len(f.queues[queue]), nil
}

func (f *fakeBroker) get(queue string) (amqp.Delivery, bool, error) {
	q := f.queues[queue]
	if len(q) == 0 {
		return amqp.Delivery{}, false, nil
	}
	d := q[0]
	f.queues[queue] = q[1:]
	f.nextTag++
	d.DeliveryTag = f.nextTag
	d.Acknowledger = &fakeAck{f: f, queue: queue, d: d}
	return d, true, nil
}

func (f *fakeBroker) publish(_ context.Context, exchange, key string, msg amqp.Publishing) error {
	if msg.MessageId == f.failOn {
		return errors.New("broker refused")
	}
	f.published = append(f.published, published{exchange, key, msg})
	return nil
}

type fakeAck struct {
	f     *fakeBroker
	queue string
	d     amqp.Delivery
}

func (a *fakeAck) Ack(tag uint64, _ bool) error { a.f.outcome[tag] = "ack"; return nil }
func (a *fakeAck) Reject(tag uint64, requeue bool) error {
	return a.Nack(tag, false, requeue)
}
func (a *fakeAck) Nack(tag uint64, _ bool, requeue bool) error {
	if requeue {
		a.f.outcome[tag] = "requeue"
		d := a.d
		d.Acknowledger, d.DeliveryTag, d.Redelivered = nil, 0, true
		a.f.queues[a.queue] = append([]amqp.Delivery{d}, a.f.queues[a.queue]...) // back at the head
	} else {
		a.f.outcome[tag] = "drop"
	}
	return nil
}

func ids(ds []amqp.Delivery) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.MessageId)
	}
	return out
}

func TestListShowsTheDeadEventsAndLeavesThemInTheQueue(t *testing.T) {
	b := newFakeBroker()
	b.add(defaultQueue, "e-1", "friend.accepted")
	b.add(defaultQueue, "e-2", "clan.member_left")
	var out bytes.Buffer

	if err := list(b, defaultQueue, 10, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 dead event(s)", "e-1", "friend.accepted", "e-2", "clan.member_left", "g-1", "realtime.events", "rejected", "2026-11-14T10:00:00Z"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
	if got := ids(b.queues[defaultQueue]); len(got) != 2 {
		t.Errorf("queue holds %v after list, want both events still there", got)
	}
	for tag, o := range b.outcome {
		if o != "requeue" {
			t.Errorf("message %d was %s, want put back", tag, o)
		}
	}
}

func TestListRespectsTheLimit(t *testing.T) {
	b := newFakeBroker()
	for _, id := range []string{"e-1", "e-2", "e-3"} {
		b.add(defaultQueue, id, "friend.accepted")
	}
	var out bytes.Buffer
	if err := list(b, defaultQueue, 2, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3 dead event(s), showing the first 2") || strings.Contains(out.String(), "e-3") {
		t.Errorf("output:\n%s", out.String())
	}
	if len(b.queues[defaultQueue]) != 3 {
		t.Errorf("queue holds %d, want 3", len(b.queues[defaultQueue]))
	}
}

func TestReplaySendsEachEventBackToTheQueueItCameFrom(t *testing.T) {
	b := newFakeBroker()
	b.add(defaultQueue, "e-1", "friend.accepted")
	b.add(defaultQueue, "e-2", "clan.member_left")
	var out bytes.Buffer

	n, err := replay(context.Background(), b, defaultQueue, nil, 10, &out)
	if err != nil || n != 2 {
		t.Fatalf("replay = %d, %v; want 2, nil", n, err)
	}
	for i, p := range b.published {
		if p.exchange != "" || p.key != "realtime.events" {
			t.Errorf("copy %d went to exchange %q key %q, want the default exchange and realtime.events", i, p.exchange, p.key)
		}
		if p.msg.DeliveryMode != amqp.Persistent || p.msg.MessageId == "" || p.msg.Type == "" || len(p.msg.Body) == 0 {
			t.Errorf("copy %d = %+v, want the message as it was, persistent", i, p.msg)
		}
	}
	if got := []string{b.published[0].msg.MessageId, b.published[1].msg.MessageId}; !slices.Equal(got, []string{"e-1", "e-2"}) {
		t.Errorf("replayed %v, want e-1, e-2 in queue order", got)
	}
	if len(b.queues[defaultQueue]) != 0 {
		t.Errorf("DLQ still holds %v, want it empty", ids(b.queues[defaultQueue]))
	}
}

func TestReplayOnlyTheChosenEvents(t *testing.T) {
	b := newFakeBroker()
	for _, id := range []string{"e-1", "E-2", "e-3"} {
		b.add(defaultQueue, id, "friend.accepted")
	}
	var out bytes.Buffer

	n, err := replay(context.Background(), b, defaultQueue, []string{"e-2", "e-9"}, 10, &out)
	if err != nil || n != 1 {
		t.Fatalf("replay = %d, %v; want 1, nil", n, err)
	}
	if len(b.published) != 1 || b.published[0].msg.MessageId != "E-2" {
		t.Errorf("published %+v, want only E-2 (IDs match whatever their case)", b.published)
	}
	if got := ids(b.queues[defaultQueue]); !slices.Equal(got, []string{"e-1", "e-3"}) {
		t.Errorf("DLQ holds %v, want e-1 and e-3 in their order", got)
	}
	if !strings.Contains(out.String(), "1 of the 2 requested event(s) were not found") {
		t.Errorf("output does not report e-9 as not found:\n%s", out.String())
	}
}

// A failure halfway loses nothing: what was confirmed is gone, the rest stays.
func TestReplayStopsAtAFailureAndKeepsTheRest(t *testing.T) {
	b := newFakeBroker()
	for _, id := range []string{"e-1", "e-2", "e-3"} {
		b.add(defaultQueue, id, "friend.accepted")
	}
	b.failOn = "e-2"
	var out bytes.Buffer

	n, err := replay(context.Background(), b, defaultQueue, nil, 10, &out)
	if err == nil || !strings.Contains(err.Error(), "e-2") {
		t.Fatalf("err = %v, want the failure of e-2", err)
	}
	if n != 1 {
		t.Errorf("replayed %d, want 1", n)
	}
	if got := ids(b.queues[defaultQueue]); !slices.Equal(got, []string{"e-2", "e-3"}) {
		t.Errorf("DLQ holds %v, want e-2 and e-3 still there", got)
	}
}

func TestReplayKeepsEverythingWhenTheTargetQueueIsMissing(t *testing.T) {
	b := newFakeBroker()
	b.add(defaultQueue, "e-1", "friend.accepted")
	b.countErr["realtime.events"] = errors.New("queue realtime.events does not exist")
	var out bytes.Buffer

	if _, err := replay(context.Background(), b, defaultQueue, nil, 10, &out); err == nil {
		t.Fatal("replay into a missing queue succeeded; the broker would have dropped the copy")
	}
	if len(b.published) != 0 || len(b.queues[defaultQueue]) != 1 {
		t.Errorf("published %d, DLQ holds %d; want nothing sent and the event kept", len(b.published), len(b.queues[defaultQueue]))
	}
}

func TestParseFallsBackWhenTheMessageIsNotAnEvent(t *testing.T) {
	e := parse(amqp.Delivery{MessageId: "m-1", Type: "clan.created", Body: []byte("not json")}, "realtime.events")
	if e.id != "m-1" || e.typ != "clan.created" || e.from != "realtime.events" || e.gameID != "" {
		t.Errorf("parse = %+v, want the message's own ID and type, and the queue from the DLQ's name", e)
	}
	if got := fromQueue("realtime.events.dead"); got != "realtime.events" {
		t.Errorf("fromQueue = %q", got)
	}
}

func TestParseFlags(t *testing.T) {
	t.Setenv("REALTIME_RABBITMQ_URL", "amqp://clan:pw@broker:5672/")
	opts, err := parseFlags("replay", []string{"-id", " e-1, e-2 ,", "-limit", "5"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.url != "amqp://clan:pw@broker:5672/" || opts.queue != defaultQueue || opts.limit != 5 || !slices.Equal(opts.ids, []string{"e-1", "e-2"}) {
		t.Errorf("opts = %+v", opts)
	}

	for _, args := range [][]string{{"-limit", "0"}, {"extra"}} {
		if _, err := parseFlags("list", args, &bytes.Buffer{}); err == nil {
			t.Errorf("parseFlags(list, %v) succeeded, want an error", args)
		}
	}
	if _, err := parseFlags("list", []string{"-id", "e-1"}, &bytes.Buffer{}); err == nil {
		t.Error("list accepted -id, want an error: only replay has it")
	}
	t.Setenv("REALTIME_RABBITMQ_URL", "")
	if _, err := parseFlags("list", nil, &bytes.Buffer{}); err == nil {
		t.Error("no URL was accepted")
	}
}

func TestRunNeverPrintsThePassword(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"list", "-url", "amqp://clan:s3cret@127.0.0.1:1/"}, &out, &errOut)
	if err == nil {
		t.Fatal("connecting to a closed port succeeded")
	}
	if strings.Contains(err.Error()+out.String()+errOut.String(), "s3cret") {
		t.Errorf("the password was printed: %v %s %s", err, out.String(), errOut.String())
	}
	if err := run([]string{"list", "-url", "http://clan:s3cret@x/"}, &out, &errOut); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad URL: err = %v, want an error without the password", err)
	}
	if err := run(nil, &out, &errOut); err == nil {
		t.Error("no command was accepted")
	}
}

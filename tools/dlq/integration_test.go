package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// TestListAndReplayOnARealBroker needs TEST_RABBITMQ_URL (README "Integration tests"). It makes two
// temporary queues of its own, test.dlq.<uuid> and its .dead queue (dead-lettered with queue
// arguments, only here), so realtime.events and its DLQ are not touched.
func TestListAndReplayOnARealBroker(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL is not set")
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	queue := "test.dlq." + events.NewID()
	dead := queue + ".dead"
	if _, err := ch.QueueDeclare(dead, false, true, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueueDeclare(queue, false, true, false, false, amqp.Table{
		"x-dead-letter-exchange": "", "x-dead-letter-routing-key": dead,
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = ch.QueueDelete(queue, false, false, false)
		_, _ = ch.QueueDelete(dead, false, false, false)
	}()

	// rejectAll takes every message of queue and rejects it without requeue, as Realtime does.
	rejectAll := func(want int) {
		t.Helper()
		for i := 0; i < want; i++ {
			d, ok, err := getWithin(ch, queue)
			if err != nil || !ok {
				t.Fatalf("message %d of %s: ok %v, err %v", i+1, queue, ok, err)
			}
			if err := d.Nack(false, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	id1, id2 := events.NewID(), events.NewID()
	for _, id := range []string{id1, id2} {
		body := `{"event_id":"` + id + `","type":"friend.accepted","game_id":"11111111-1111-4111-8111-111111111111"}`
		if err := ch.PublishWithContext(context.Background(), "", queue, false, false, amqp.Publishing{
			MessageId: id, Type: "friend.accepted", ContentType: "application/json", Body: []byte(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rejectAll(2)
	if n := waitCount(t, ch, dead, 2); n != 2 {
		t.Fatalf("%s holds %d, want the 2 rejected events", dead, n)
	}

	b, err := dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	var out bytes.Buffer
	if err := list(b, dead, 10, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 dead event(s)", id1, id2, queue, "rejected"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output does not contain %q:\n%s", want, out.String())
		}
	}
	if n := waitCount(t, ch, dead, 2); n != 2 {
		t.Fatalf("after list %s holds %d, want both still there", dead, n)
	}

	// Replay one: it goes back to the queue it came from, the other stays dead.
	out.Reset()
	if n, err := replay(context.Background(), b, dead, []string{id2}, 10, &out); err != nil || n != 1 {
		t.Fatalf("replay -id = %d, %v; output:\n%s", n, err, out.String())
	}
	d, ok, err := getWithin(ch, queue)
	if err != nil || !ok || d.MessageId != id2 {
		t.Fatalf("replayed message = %q, ok %v, err %v; want %s back in %s", d.MessageId, ok, err, id2, queue)
	}
	// Still unusable: rejected again, it goes back to the dead-letter queue.
	if err := d.Nack(false, false); err != nil {
		t.Fatal(err)
	}
	if n := waitCount(t, ch, dead, 2); n != 2 {
		t.Fatalf("%s holds %d, want the first one and the one rejected again", dead, n)
	}

	// Replay all.
	out.Reset()
	if n, err := replay(context.Background(), b, dead, nil, 10, &out); err != nil || n != 2 {
		t.Fatalf("replay = %d, %v; output:\n%s", n, err, out.String())
	}
	if n := waitCount(t, ch, queue, 2); n != 2 {
		t.Errorf("%s holds %d, want both events back", queue, n)
	}
	if n := waitCount(t, ch, dead, 0); n != 0 {
		t.Errorf("%s holds %d, want it empty", dead, n)
	}
}

// getWithin takes the next message of queue, waiting up to 3 s for one to arrive.
func getWithin(ch *amqp.Channel, queue string) (amqp.Delivery, bool, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		d, ok, err := ch.Get(queue, false)
		if err != nil || ok || time.Now().After(deadline) {
			return d, ok, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitCount waits up to 3 s for queue to hold want messages and returns what it holds then.
func waitCount(t *testing.T, ch *amqp.Channel, queue string, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := ch.QueueDeclarePassive(queue, false, true, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if info.Messages == want || time.Now().After(deadline) {
			return info.Messages
		}
		time.Sleep(20 * time.Millisecond)
	}
}

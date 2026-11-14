package consumer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// syncBuffer is a bytes.Buffer the consumer goroutine can write to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestARejectedEventReachesTheDeadLetterQueue runs the real consumer against a real broker. It
// needs TEST_RABBITMQ_URL (README "Integration tests"), and uses two temporary queues of its own,
// dead-lettered with queue arguments (only here: the real queues use the policies of
// deploy/rabbitmq/definitions.json), so nothing touches realtime.events.
func TestARejectedEventReachesTheDeadLetterQueue(t *testing.T) {
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
		"x-dead-letter-exchange":    "", // the default exchange routes by queue name
		"x-dead-letter-routing-key": dead,
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = ch.QueueDelete(queue, false, false, false)
		_, _ = ch.QueueDelete(dead, false, false, false)
	}()

	var logs syncBuffer
	c, err := New(Config{URL: url, Queue: queue, DeadLetterCheck: 50 * time.Millisecond},
		handlerFunc(func(context.Context, []byte) error { return ErrDrop }), // every event is unusable
		slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	id := events.NewID()
	if err := ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
		MessageId: id, Type: "friend.accepted", Body: []byte(`{"event_id":"` + id + `","type":"friend.accepted"}`),
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		q, err := ch.QueueDeclarePassive(dead, false, true, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.Messages == 1 && strings.Contains(logs.String(), `"count":1`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dead-letter queue holds %d messages, want 1; logs:\n%s", q.Messages, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{rejectedMsg, `"event_id":"` + id + `"`, `"reason":"unusable"`, "dead-letter queue holds rejected events"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %s:\n%s", want, logs.String())
		}
	}
}

// A missing dead-letter queue is only logged: the check uses a channel of its own, so the broker
// closing it (404) never stops the consumer.
func TestAMissingDeadLetterQueueDoesNotStopTheConsumer(t *testing.T) {
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
	if _, err := ch.QueueDeclare(queue, false, true, false, false, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = ch.QueueDelete(queue, false, false, false) }()

	var logs syncBuffer
	handled := make(chan struct{}, 10)
	c, err := New(Config{URL: url, Queue: queue, DeadLetterQueue: queue + ".missing", DeadLetterCheck: 20 * time.Millisecond},
		handlerFunc(func(context.Context, []byte) error { handled <- struct{}{}; return nil }),
		slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Let a few checks fail first, then the consumer must still handle an event.
	time.Sleep(200 * time.Millisecond)
	if err := ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatalf("the consumer stopped handling events; logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "dead-letter queue not found") {
		t.Errorf("no warning about the missing queue; logs:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "event consumer stopped") {
		t.Errorf("the consumer's session ended; logs:\n%s", logs.String())
	}
}

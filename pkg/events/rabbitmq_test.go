package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeBroker replaces the real connection with one that always fails, and a clock the test moves.
type fakeBroker struct {
	attempts int
	clock    time.Time
}

func (f *fakeBroker) install(r *RabbitMQ) {
	f.clock = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return f.clock }
	r.open = func() (*amqp.Connection, *amqp.Channel, error) {
		f.attempts++
		return nil, nil, errors.New("connection refused")
	}
}

func TestNewRabbitMQDoesNotConnect(t *testing.T) {
	r := NewRabbitMQ("amqp://nobody@127.0.0.1:1/", quietLog)
	f := &fakeBroker{}
	f.install(r)
	if f.attempts != 0 {
		t.Fatalf("connected %d times on creation, want 0", f.attempts)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close before any publish = %v, want nil", err)
	}
}

func TestPublishWhenTheBrokerIsDown(t *testing.T) {
	r := NewRabbitMQ("amqp://nobody@127.0.0.1:1/", quietLog)
	f := &fakeBroker{}
	f.install(r)

	err := r.Publish(context.Background(), New("friend.accepted", "g", nil))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Publish = %v, want ErrUnavailable", err)
	}
	if f.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", f.attempts)
	}

	// Within retryAfter: fail at once, without another attempt.
	f.clock = f.clock.Add(retryAfter - time.Second)
	if err := r.Publish(context.Background(), New("friend.accepted", "g", nil)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second Publish = %v, want ErrUnavailable", err)
	}
	if f.attempts != 1 {
		t.Fatalf("attempts = %d, want still 1 (no retry before %s)", f.attempts, retryAfter)
	}

	// After retryAfter: try again.
	f.clock = f.clock.Add(2 * time.Second)
	_ = r.Publish(context.Background(), New("friend.accepted", "g", nil))
	if f.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 after the wait", f.attempts)
	}
}

func TestPublishWithAnUnencodableEventDoesNotConnect(t *testing.T) {
	r := NewRabbitMQ("amqp://nobody@127.0.0.1:1/", quietLog)
	f := &fakeBroker{}
	f.install(r)
	err := r.Publish(context.Background(), New("x", "g", map[string]any{"bad": make(chan int)}))
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("Publish = %v, want an encoding error", err)
	}
	if f.attempts != 0 {
		t.Fatalf("attempts = %d, want 0", f.attempts)
	}
}

// The real dial against a closed port fails quickly with ErrUnavailable.
func TestPublishToAClosedPort(t *testing.T) {
	r := NewRabbitMQ("amqp://guest:guest@127.0.0.1:1/", quietLog)
	start := time.Now()
	err := r.Publish(context.Background(), New("friend.accepted", "g", nil))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Publish = %v, want ErrUnavailable", err)
	}
	if d := time.Since(start); d > dialTimeout+time.Second {
		t.Fatalf("took %s, want at most about %s", d, dialTimeout)
	}
}

// TestPublishToRealBroker runs only when TEST_RABBITMQ_URL is set, for example with docker compose up:
//
//	TEST_RABBITMQ_URL=amqp://clan:PASSWORD@localhost:5672/ go test ./pkg/events/
//
// It uses the routing key "test.published", which no service queue is bound to, so it does not
// leave messages in realtime.events.
func TestPublishToRealBroker(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A private queue that receives the test events, deleted when the test disconnects.
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind(q.Name, "test.#", Exchange, false, nil); err != nil {
		t.Fatal(err)
	}
	deliveries, err := ch.ConsumeWithContext(ctx, q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	r := NewRabbitMQ(url, quietLog)
	defer r.Close()
	sent := New("test.published", "game-1", map[string]any{"n": 1})
	if err := r.Publish(ctx, sent); err != nil {
		t.Fatalf("Publish = %v", err)
	}

	select {
	case d := <-deliveries:
		if d.RoutingKey != "test.published" || d.Exchange != Exchange {
			t.Errorf("got %s on %s, want test.published on %s", d.RoutingKey, d.Exchange, Exchange)
		}
		if d.DeliveryMode != amqp.Persistent || d.ContentType != "application/json" || d.MessageId != sent.ID {
			t.Errorf("delivery mode %d, content type %q, message id %q", d.DeliveryMode, d.ContentType, d.MessageId)
		}
		var got Event
		if err := json.Unmarshal(d.Body, &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != sent.ID || got.Type != sent.Type || got.GameID != "game-1" || got.Data["n"] != float64(1) {
			t.Errorf("body = %s", d.Body)
		}
	case <-ctx.Done():
		t.Fatal("the event did not arrive")
	}

	// After Close, Publish connects again.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, New("test.published", "game-1", nil)); err != nil {
		t.Fatalf("Publish after Close = %v", err)
	}
}

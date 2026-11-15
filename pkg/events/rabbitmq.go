package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	dialTimeout    = 3 * time.Second
	publishTimeout = 5 * time.Second
	// After a failed connection attempt, Publish fails at once for this long instead of
	// making every request wait for another attempt.
	retryAfter = 5 * time.Second
)

// ErrUnavailable means the broker could not be reached, so the event was not sent.
var ErrUnavailable = errors.New("events: broker unavailable")

// RabbitMQ publishes events to RabbitMQ. It connects on the first Publish, not when it is
// created, so the service starts and works even when the broker is down, and it reconnects
// by itself after a failure. It is safe to use from many goroutines.
type RabbitMQ struct {
	url string
	log *slog.Logger

	mu      sync.Mutex
	conn    *amqp.Connection
	ch      *amqp.Channel
	retryAt time.Time // no new connection attempt before this time

	// open and now are replaced in tests.
	open func() (*amqp.Connection, *amqp.Channel, error)
	now  func() time.Time
}

// NewRabbitMQ returns a publisher for the broker at url, for example
// amqp://clan:PASSWORD@rabbitmq:5672/. It does not connect yet.
func NewRabbitMQ(url string, log *slog.Logger) *RabbitMQ {
	r := &RabbitMQ{url: url, log: log, now: time.Now}
	r.open = r.dial
	return r
}

// Publish sends e to Exchange with e.Type as the routing key and waits until the broker
// confirms it. The message is persistent, so it survives a broker restart once confirmed.
// An event that belongs to a trace is published in a span of that trace, and carries the trace
// in its message headers for the consumer (trace.go).
func (r *RabbitMQ) Publish(ctx context.Context, e Event) error {
	ctx, end := startPublish(ctx, e)
	err := r.publish(ctx, e)
	end(err)
	return err
}

func (r *RabbitMQ) publish(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("events: encode %s: %w", e.Type, err)
	}

	// One publish at a time: the channel is shared, and it keeps the code simple.
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.connect(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	confirm, err := r.ch.PublishWithDeferredConfirmWithContext(ctx, Exchange, e.Type, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    e.ID,
		Type:         e.Type,
		Timestamp:    e.OccurredAt,
		Headers:      traceHeaders(ctx),
		Body:         body,
	})
	if err != nil {
		r.drop()
		return fmt.Errorf("events: publish %s: %w", e.Type, err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		r.drop() // no answer in time: start from a fresh connection next time
		return fmt.Errorf("events: wait for the broker to confirm %s: %w", e.Type, err)
	}
	if !acked {
		return fmt.Errorf("events: broker refused %s", e.Type)
	}
	return nil
}

// Close closes the connection. The publisher can still be used afterwards: it reconnects.
func (r *RabbitMQ) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn := r.conn
	r.conn, r.ch = nil, nil
	if conn == nil || conn.IsClosed() {
		return nil
	}
	return conn.Close()
}

// connect makes sure there is an open channel. The caller holds r.mu.
func (r *RabbitMQ) connect() error {
	if r.ch != nil && !r.ch.IsClosed() {
		return nil
	}
	r.drop()
	if r.now().Before(r.retryAt) {
		return ErrUnavailable
	}
	conn, ch, err := r.open()
	if err != nil {
		r.retryAt = r.now().Add(retryAfter)
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	r.conn, r.ch = conn, ch
	r.log.Info("connected to rabbitmq")
	return nil
}

// dial opens a connection and a channel in confirm mode, and declares Exchange.
func (r *RabbitMQ) dial() (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.DialConfig(r.url, amqp.Config{Dial: amqp.DefaultDial(dialTimeout)})
	if err != nil {
		return nil, nil, err
	}
	ch, err := conn.Channel()
	if err == nil {
		err = ch.Confirm(false)
	}
	if err == nil {
		// Declaring an exchange that already exists with the same settings changes nothing,
		// so this also works before rabbitmq-setup has run.
		err = ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil)
	}
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, ch, nil
}

// drop forgets the current connection, closing it if it is still open. The caller holds r.mu.
func (r *RabbitMQ) drop() {
	if r.conn != nil && !r.conn.IsClosed() {
		_ = r.conn.Close()
	}
	r.conn, r.ch = nil, nil
}

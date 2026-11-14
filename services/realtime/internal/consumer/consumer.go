// Package consumer reads the realtime.events queue and turns each event into database changes
// and WebSocket frames.
//
// There is exactly one consumer goroutine, so events are handled in the order they arrive: a
// friend.removed never overtakes the friend.accepted before it. The queue and its bindings are
// declared by the rabbitmq-setup job (deploy/rabbitmq/definitions.json), not here.
//
// Delivery rules (events arrive at least once):
//   - the message is acknowledged only after the handler succeeded;
//   - an event the handler cannot make sense of (ErrDrop) is rejected without requeue at once;
//   - any other failure puts the message back once, after RetryDelay; if it fails again it is
//     rejected without requeue and logged.
//
// A rejected message is not deleted: a broker policy (deploy/rabbitmq/definitions.json) dead-letters
// it to realtime.events.dead, where it is kept for 14 days with RabbitMQ's x-death header (queue,
// reason, count, original routing key). This package does nothing for that; it only rejects.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrDrop is wrapped by a Handler's error when the event is unusable and handling it again would
// not help (broken JSON, a missing field). The message is dropped without a retry.
var ErrDrop = errors.New("drop event")

// Handler handles the body of one message. A nil result acknowledges it.
type Handler interface {
	Handle(ctx context.Context, body []byte) error
}

// Config says where to read from and how.
type Config struct {
	URL        string        // amqp://user:password@host:5672/ ; never logged, only its host
	Queue      string        // the durable queue to read, "realtime.events"
	Prefetch   int           // unacknowledged messages the broker may send ahead; 20
	RetryDelay time.Duration // wait before a failed message is put back; 1s
	// HandleTimeout limits one Handle call (database work and pushing frames); 10s.
	HandleTimeout time.Duration
	// MinBackoff and MaxBackoff bound the wait between connection attempts (it doubles); 1s and 30s.
	MinBackoff, MaxBackoff time.Duration
}

func (c Config) withDefaults() Config {
	if c.Queue == "" {
		c.Queue = "realtime.events"
	}
	if c.Prefetch <= 0 {
		c.Prefetch = 20
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = time.Second
	}
	if c.HandleTimeout <= 0 {
		c.HandleTimeout = 10 * time.Second
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = time.Second
	}
	if c.MaxBackoff < c.MinBackoff {
		c.MaxBackoff = 30 * time.Second
	}
	return c
}

// Consumer reads the queue until its context ends.
type Consumer struct {
	cfg     Config
	handler Handler
	log     *slog.Logger
	host    string // the broker's address without the password, for logs
}

// New returns a Consumer. It does not connect: Run does, and keeps trying while the broker is down.
func New(cfg Config, handler Handler, log *slog.Logger) (*Consumer, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Host == "" {
		// Do not put the URL in the error: it holds the password.
		return nil, errors.New("the RabbitMQ URL must look like amqp://user:password@host:5672/")
	}
	return &Consumer{cfg: cfg.withDefaults(), handler: handler, log: log, host: u.Host}, nil
}

const consumerTag = "realtime"

// Run connects and consumes until ctx ends, then returns. When the connection breaks or the broker is
// down it waits (1s, 2s, 4s ... up to 30s) and connects again; it never gives up. The wait starts
// again from 1s after a connection worked.
func (c *Consumer) Run(ctx context.Context) {
	backoff := c.cfg.MinBackoff
	for ctx.Err() == nil {
		connected, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = c.cfg.MinBackoff
		}
		c.log.Warn("event consumer stopped, will reconnect", "broker", c.host, "queue", c.cfg.Queue, "error", err, "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.cfg.MaxBackoff)
	}
}

// session is one connection: dial, open a channel, consume until something breaks or ctx ends.
// connected tells whether the broker accepted the consumer (the backoff then starts over).
func (c *Consumer) session(ctx context.Context) (connected bool, err error) {
	conn, err := amqp.DialConfig(c.cfg.URL, amqp.Config{
		Heartbeat: 10 * time.Second,
		Dial:      amqp.DefaultDial(10 * time.Second),
		Properties: amqp.Table{
			"connection_name": "realtime",
		},
	})
	if err != nil {
		return false, fmt.Errorf("connect to RabbitMQ: %w", redact(err))
	}
	// Closing the connection hands every unacknowledged message back to the queue.
	defer func() { _ = conn.Close() }()

	ch, err := conn.Channel()
	if err != nil {
		return false, fmt.Errorf("open channel: %w", err)
	}
	if err := ch.Qos(c.cfg.Prefetch, 0, false); err != nil {
		return false, fmt.Errorf("set prefetch: %w", err)
	}
	deliveries, err := ch.Consume(c.cfg.Queue, consumerTag, false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("consume %s (is the queue declared? run rabbitmq-setup): %w", c.cfg.Queue, err)
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	c.log.Info("event consumer started", "broker", c.host, "queue", c.cfg.Queue, "prefetch", c.cfg.Prefetch)

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case e := <-closed:
			if e == nil {
				return true, errors.New("connection closed")
			}
			return true, fmt.Errorf("connection lost: %w", e)
		case d, ok := <-deliveries:
			if !ok {
				return true, errors.New("delivery channel closed")
			}
			c.handle(ctx, d)
		}
	}
}

// handle runs the handler for one message and acknowledges it the way the rules in the package
// comment say.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	err := c.callHandler(ctx, d.Body)
	switch {
	case err == nil:
		c.done(d.Ack(false), "ack", d)
	case errors.Is(err, ErrDrop):
		c.log.Warn("dropping unusable event", "routing_key", d.RoutingKey, "error", err)
		c.done(d.Nack(false, false), "reject", d)
	case !d.Redelivered:
		c.log.Warn("event failed, will try once more", "routing_key", d.RoutingKey, "error", err)
		// A short pause, so a database hiccup is over before the second try.
		select {
		case <-ctx.Done():
		case <-time.After(c.cfg.RetryDelay):
		}
		c.done(d.Nack(false, true), "requeue", d)
	default:
		c.log.Error("event failed twice, dropping it", "routing_key", d.RoutingKey, "error", err)
		c.done(d.Nack(false, false), "reject", d)
	}
}

// callHandler calls the handler with a time limit and turns a panic into an error, so one bad
// event cannot take the whole service down.
func (c *Consumer) callHandler(ctx context.Context, body []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.HandleTimeout)
	defer cancel()
	return c.handler.Handle(ctx, body)
}

// done logs a failed ack. The usual cause is a closed channel; the session ends on its own and the
// broker delivers the message again.
func (c *Consumer) done(err error, what string, d amqp.Delivery) {
	if err != nil {
		c.log.Warn("could not "+what+" event", "routing_key", d.RoutingKey, "error", err)
	}
}

// redact keeps the broker's password out of an error: amqp091 may quote the URL.
func redact(err error) error {
	var ne *url.Error
	if errors.As(err, &ne) {
		return errors.New(ne.Err.Error())
	}
	return err
}

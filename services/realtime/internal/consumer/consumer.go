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
// reason, count, original routing key). The broker keeps no reason of ours, so every rejection is
// logged once, with the event's ID, as "event rejected to the dead-letter queue". While the consumer
// is connected it also counts the dead-letter queue now and then and warns while it holds events.
package consumer

import (
	"context"
	"encoding/json"
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
	// DeadLetterQueue is where the broker moves what this consumer rejects; Queue + ".dead".
	DeadLetterQueue string
	// DeadLetterCheck is how often the dead-letter queue is counted while connected; 5m.
	DeadLetterCheck time.Duration
}

// DefaultDeadLetterCheck is how often the dead-letter queue is counted when Config does not say.
const DefaultDeadLetterCheck = 5 * time.Minute

// Reasons of a rejection, in the "reason" field of its log line.
const (
	reasonUnusable    = "unusable"     // the handler cannot make sense of the event (ErrDrop)
	reasonFailedTwice = "failed_twice" // handling failed, and failed again after the one retry
)

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
	if c.DeadLetterQueue == "" {
		c.DeadLetterQueue = c.Queue + ".dead"
	}
	if c.DeadLetterCheck <= 0 {
		c.DeadLetterCheck = DefaultDeadLetterCheck
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

	// Count the dead-letter queue now and every DeadLetterCheck, for as long as this session lasts.
	dead := connCounter{conn: conn}
	c.checkDeadLetters(dead)
	check := time.NewTicker(c.cfg.DeadLetterCheck)
	defer check.Stop()

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-check.C:
			c.checkDeadLetters(dead)
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
		c.logRejected(slog.LevelWarn, d, reasonUnusable, err)
		c.done(d.Nack(false, false), "reject", d)
	case !d.Redelivered:
		id, typ := identify(d)
		c.log.Warn("event failed, will try once more", "event_id", id, "type", typ, "routing_key", d.RoutingKey, "error", err)
		// A short pause, so a database hiccup is over before the second try.
		select {
		case <-ctx.Done():
		case <-time.After(c.cfg.RetryDelay):
		}
		c.done(d.Nack(false, true), "requeue", d)
	default:
		c.logRejected(slog.LevelError, d, reasonFailedTwice, err)
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

// logRejected writes the one log line of a rejected event. The broker keeps the event in the
// dead-letter queue but not our reason, so this line is where it is found again, by event_id.
func (c *Consumer) logRejected(level slog.Level, d amqp.Delivery, reason string, err error) {
	id, typ := identify(d)
	c.log.Log(context.Background(), level, "event rejected to the dead-letter queue",
		"event_id", id, "type", typ, "routing_key", d.RoutingKey, "reason", reason, "error", err,
		"dead_letter_queue", c.cfg.DeadLetterQueue)
}

// identify returns the event ID and type of a delivery: from its body when that is an event, else
// from the message's properties (publishers set message_id to the event ID and type to its type).
// Either can be empty for a message that is neither.
func identify(d amqp.Delivery) (id, typ string) {
	var env struct {
		ID   string `json:"event_id"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(d.Body, &env) // a body that is not JSON leaves both empty
	id, typ = env.ID, env.Type
	if id == "" {
		id = d.MessageId
	}
	if typ == "" {
		typ = d.Type
	}
	return id, typ
}

// queueCounter tells how many messages a queue holds.
type queueCounter interface {
	count(queue string) (int, error)
}

// connCounter counts with a passive declare on a channel of its own: a missing queue makes the
// broker close that channel, never the one the consumer reads from.
type connCounter struct{ conn *amqp.Connection }

func (c connCounter) count(queue string) (int, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return 0, err
	}
	defer func() { _ = ch.Close() }()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		return 0, err
	}
	return q.Messages, nil
}

// checkDeadLetters warns while the dead-letter queue holds events, and when it is missing (then a
// rejected event is deleted). An empty queue logs nothing. A failure is only logged: it never
// stops the consumer.
func (c *Consumer) checkDeadLetters(counter queueCounter) {
	queue := c.cfg.DeadLetterQueue
	n, err := counter.count(queue)
	var amqpErr *amqp.Error
	switch {
	case errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound:
		c.log.Warn("dead-letter queue not found, rejected events are deleted; run rabbitmq-setup", "dead_letter_queue", queue)
	case err != nil:
		c.log.Warn("could not count the dead-letter queue", "dead_letter_queue", queue, "error", err)
	case n > 0:
		c.log.Warn("dead-letter queue holds rejected events", "dead_letter_queue", queue, "count", n)
	}
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

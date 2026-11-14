package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// broker is what list and replay need from RabbitMQ. amqpBroker is the real one; tests use a fake.
type broker interface {
	// count returns how many messages queue holds that nobody has taken.
	count(queue string) (int, error)
	// get takes the next message of queue without acknowledging it; ok is false when the queue is
	// empty. The message stays with us until it is acked (removed) or nacked with requeue (put back).
	get(queue string) (d amqp.Delivery, ok bool, err error)
	// publish sends msg and returns once the broker has confirmed it.
	publish(ctx context.Context, exchange, key string, msg amqp.Publishing) error
}

// deadEvent is one message of the dead-letter queue, as the tool shows it.
type deadEvent struct {
	d          amqp.Delivery
	id         string    // event_id from the body, else the message_id
	typ        string    // type from the body, else the message's type
	gameID     string    // game_id from the body; empty when the body cannot be read
	from       string    // the queue it was dead-lettered from (x-death), where replay sends it
	routingKey string    // its original routing key (x-death), for information
	reason     string    // why the broker dead-lettered it: rejected, expired, ...
	since      time.Time // when it was first dead-lettered from that queue
	times      int64     // how often it was dead-lettered from that queue
}

// parse reads what the tool needs from a dead message. fallbackFrom is the queue a message
// without x-death came from (the dead queue's name without ".dead").
func parse(d amqp.Delivery, fallbackFrom string) deadEvent {
	e := deadEvent{d: d, id: d.MessageId, typ: d.Type, from: fallbackFrom}
	var env struct {
		ID     string `json:"event_id"`
		Type   string `json:"type"`
		GameID string `json:"game_id"`
	}
	if json.Unmarshal(d.Body, &env) == nil {
		if env.ID != "" {
			e.id = env.ID
		}
		if env.Type != "" {
			e.typ = env.Type
		}
		e.gameID = env.GameID
	}
	// x-death holds one entry per (queue, reason), the newest first.
	if deaths, ok := d.Headers["x-death"].([]any); ok && len(deaths) > 0 {
		if death, ok := deaths[0].(amqp.Table); ok {
			if q, ok := death["queue"].(string); ok && q != "" {
				e.from = q
			}
			e.reason, _ = death["reason"].(string)
			e.since, _ = death["time"].(time.Time)
			e.times, _ = death["count"].(int64)
			if keys, ok := death["routing-keys"].([]any); ok && len(keys) > 0 {
				e.routingKey, _ = keys[0].(string)
			}
		}
	}
	return e
}

// take gets up to limit messages of queue and keeps them all unacknowledged. A message put back
// with requeue returns to the head of the queue, so it must not be put back before we have read
// past it: that would read it again.
func take(b broker, queue string, limit int) ([]amqp.Delivery, error) {
	var out []amqp.Delivery
	for len(out) < limit {
		d, ok, err := b.get(queue)
		if err != nil {
			return out, fmt.Errorf("read %s: %w", queue, err)
		}
		if !ok {
			break
		}
		out = append(out, d)
	}
	return out, nil
}

// putBack returns messages to their queue unchanged. It is used for everything not removed. It
// goes from the last to the first: RabbitMQ puts a requeued message back where it was when it can,
// and at the head otherwise, and in both cases the queue keeps its order this way.
func putBack(ds []amqp.Delivery) error {
	var errs []error
	for i := len(ds) - 1; i >= 0; i-- {
		if err := ds[i].Nack(false, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fromQueue is the queue a message of dead came from when it has no x-death: "x.dead" -> "x".
func fromQueue(dead string) string {
	return strings.TrimSuffix(dead, ".dead")
}

// list prints up to limit dead events of queue and leaves every one of them in the queue.
func list(b broker, queue string, limit int, out io.Writer) error {
	total, err := b.count(queue)
	if err != nil {
		return fmt.Errorf("count %s: %w", queue, err)
	}
	ds, err := take(b, queue, limit)
	defer func() { _ = putBack(ds) }()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s: %d dead event(s)", queue, total)
	if len(ds) < total {
		fmt.Fprintf(out, ", showing the first %d", len(ds))
	}
	fmt.Fprintln(out)
	if len(ds) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EVENT_ID\tTYPE\tGAME_ID\tFROM\tROUTING_KEY\tREASON\tTIMES\tSINCE")
	for _, d := range ds {
		e := parse(d, fromQueue(queue))
		since := ""
		if !e.since.IsZero() {
			since = e.since.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", dash(e.id), dash(e.typ), dash(e.gameID), e.from,
			dash(e.routingKey), dash(e.reason), e.times, dash(since))
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// replay sends dead events of queue back to the queue each one came from, and removes each from
// queue only after the broker confirmed its copy, so a failure halfway loses nothing: what was not
// confirmed stays in queue. With ids, only those events are replayed and the others stay. It
// returns how many were replayed. A copy is safe: Realtime skips an event it handled before.
func replay(ctx context.Context, b broker, queue string, ids []string, limit int, out io.Writer) (int, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.ToLower(id)] = true
	}
	ds, err := take(b, queue, limit)
	if err != nil {
		_ = putBack(ds)
		return 0, err
	}

	replayed := 0
	var keep []amqp.Delivery // put back at the end: skipped, failed, or after a failure
	defer func() { _ = putBack(keep) }()
	exists := map[string]bool{} // target queues seen to exist
	for i, d := range ds {
		e := parse(d, fromQueue(queue))
		if len(want) > 0 && !want[strings.ToLower(e.id)] {
			keep = append(keep, d)
			continue
		}
		// The broker confirms a message no queue took, so a missing target would lose the event:
		// make sure it exists before the first copy goes to it.
		if !exists[e.from] {
			if _, err := b.count(e.from); err != nil {
				keep = append(keep, ds[i:]...)
				return replayed, fmt.Errorf("replay %s (%s): target queue %s: %w; it and the events after it stay in %s", e.id, e.typ, e.from, err, queue)
			}
			exists[e.from] = true
		}
		// The default exchange routes by queue name, so only the queue that rejected it gets it again.
		err := b.publish(ctx, "", e.from, amqp.Publishing{
			ContentType:  d.ContentType,
			DeliveryMode: amqp.Persistent,
			MessageId:    d.MessageId,
			Type:         d.Type,
			Timestamp:    d.Timestamp,
			Body:         d.Body,
		})
		if err != nil {
			keep = append(keep, ds[i:]...)
			return replayed, fmt.Errorf("replay %s (%s): %w; it and the events after it stay in %s", e.id, e.typ, err, queue)
		}
		if err := d.Ack(false); err != nil {
			// The copy is out but the dead one stays: replaying it again later is harmless.
			fmt.Fprintf(out, "replayed %s (%s) to %s, but could not remove it from %s: %v\n", e.id, e.typ, e.from, queue, err)
		} else {
			fmt.Fprintf(out, "replayed %s (%s) to %s\n", e.id, e.typ, e.from)
		}
		replayed++
	}
	if len(want) > 0 && replayed < len(want) {
		fmt.Fprintf(out, "%d of the %d requested event(s) were not found in %s (looked at %d)\n", len(want)-replayed, len(want), queue, len(ds))
	}
	return replayed, nil
}

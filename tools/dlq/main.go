// Command dlq lists and replays the events in Realtime's dead-letter queue, realtime.events.dead.
//
// An event Realtime rejects (unusable, or failed twice) is moved there by the broker instead of
// being deleted (see docs/events.md, section 9). This tool shows them, and sends them back to the
// queue they came from once the cause is fixed:
//
//	go run ./tools/dlq list                      # every dead event, left in the queue
//	go run ./tools/dlq replay                    # replay all of them
//	go run ./tools/dlq replay -id <event_id>,... # replay only these
//
// The broker address comes from -url, or from REALTIME_RABBITMQ_URL. Its password is never printed.
// Replaying is safe: Realtime skips an event it has already handled, and an event that is still
// unusable goes back to the dead-letter queue.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultQueue = "realtime.events.dead"
	defaultLimit = 1000
	runLimit     = 2 * time.Minute
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dlq:", err)
		os.Exit(1)
	}
}

// options are the flags of one run.
type options struct {
	url   string
	queue string
	limit int
	ids   []string
}

func run(args []string, out, errOut io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "replay") {
		usage(errOut)
		return errors.New("say list or replay")
	}
	cmd := args[0]
	opts, err := parseFlags(cmd, args[1:], errOut)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, runLimit)
	defer cancel()

	b, err := dial(opts.url)
	if err != nil {
		return err
	}
	defer b.close()

	if cmd == "list" {
		return list(b, opts.queue, opts.limit, out)
	}
	n, err := replay(ctx, b, opts.queue, opts.ids, opts.limit, out)
	fmt.Fprintf(out, "replayed %d event(s)\n", n)
	return err
}

func parseFlags(cmd string, args []string, errOut io.Writer) (options, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var opts options
	var ids string
	fs.StringVar(&opts.url, "url", os.Getenv("REALTIME_RABBITMQ_URL"), "RabbitMQ address, amqp://user:password@host:5672/ (default $REALTIME_RABBITMQ_URL)")
	fs.StringVar(&opts.queue, "queue", defaultQueue, "the dead-letter queue")
	fs.IntVar(&opts.limit, "limit", defaultLimit, "the most events to read in one run")
	if cmd == "replay" {
		fs.StringVar(&ids, "id", "", "replay only these event IDs, comma-separated (default: all)")
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.url == "" {
		return options{}, errors.New("no broker address: pass -url or set REALTIME_RABBITMQ_URL")
	}
	if opts.limit < 1 {
		return options{}, errors.New("-limit must be 1 or more")
	}
	for _, id := range strings.Split(ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			opts.ids = append(opts.ids, id)
		}
	}
	return opts, nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  go run ./tools/dlq list   [-url amqp://...] [-queue realtime.events.dead] [-limit 1000]
  go run ./tools/dlq replay [-url amqp://...] [-queue realtime.events.dead] [-limit 1000] [-id <event_id>,...]`)
}

// amqpBroker is the broker over one connection and one channel in confirm mode.
type amqpBroker struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func dial(rawURL string) (*amqpBroker, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Host == "" {
		return nil, errors.New("the broker address must look like amqp://user:password@host:5672/") // never echo it: it holds the password
	}
	conn, err := amqp.DialConfig(rawURL, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", u.Host, redact(err))
	}
	ch, err := conn.Channel()
	if err == nil {
		err = ch.Confirm(false)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open a channel on %s: %w", u.Host, err)
	}
	return &amqpBroker{conn: conn, ch: ch}, nil
}

// close closes the connection; the broker puts back any message still unacknowledged.
func (b *amqpBroker) close() { _ = b.conn.Close() }

func (b *amqpBroker) count(queue string) (int, error) {
	q, err := b.ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
		return 0, fmt.Errorf("queue %s does not exist (run rabbitmq-setup)", queue)
	}
	if err != nil {
		return 0, err
	}
	return q.Messages, nil
}

func (b *amqpBroker) get(queue string) (amqp.Delivery, bool, error) {
	return b.ch.Get(queue, false)
}

func (b *amqpBroker) publish(ctx context.Context, exchange, key string, msg amqp.Publishing) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	confirm, err := b.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, msg)
	if err != nil {
		return err
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for the broker to confirm: %w", err)
	}
	if !acked {
		return errors.New("the broker refused the message")
	}
	return nil
}

// redact keeps the password out of an error: amqp091 may quote the URL.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return errors.New(ue.Err.Error())
	}
	return err
}

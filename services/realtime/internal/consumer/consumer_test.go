package consumer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// acker records how a delivery was answered.
type acker struct{ calls []string }

func (a *acker) Ack(_ uint64, _ bool) error { a.calls = append(a.calls, "ack"); return nil }
func (a *acker) Nack(_ uint64, _ bool, requeue bool) error {
	a.calls = append(a.calls, fmt.Sprintf("nack requeue=%v", requeue))
	return nil
}
func (a *acker) Reject(_ uint64, requeue bool) error {
	a.calls = append(a.calls, fmt.Sprintf("reject requeue=%v", requeue))
	return nil
}

type handlerFunc func(ctx context.Context, body []byte) error

func (f handlerFunc) Handle(ctx context.Context, body []byte) error { return f(ctx, body) }

func newConsumer(t *testing.T, h Handler) (*Consumer, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	c, err := New(Config{URL: "amqp://clan:secret-pw@broker:5672/", RetryDelay: time.Millisecond}, h, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c, &logs
}

func TestHandleDecidesHowToAnswerTheBroker(t *testing.T) {
	errDB := errors.New("database down")
	tests := []struct {
		name        string
		err         error
		redelivered bool
		want        string
	}{
		{"success is acknowledged", nil, false, "ack"},
		{"success after a redelivery is acknowledged", nil, true, "ack"},
		{"an unusable event is dropped at once", fmt.Errorf("%w: no data", ErrDrop), false, "nack requeue=false"},
		{"an unusable event is dropped on redelivery too", fmt.Errorf("%w: no data", ErrDrop), true, "nack requeue=false"},
		{"first failure is requeued once", errDB, false, "nack requeue=true"},
		{"second failure is dropped", errDB, true, "nack requeue=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newConsumer(t, handlerFunc(func(context.Context, []byte) error { return tt.err }))
			a := &acker{}
			c.handle(context.Background(), amqp.Delivery{Acknowledger: a, DeliveryTag: 1, Redelivered: tt.redelivered, Body: []byte(`{}`)})
			if len(a.calls) != 1 || a.calls[0] != tt.want {
				t.Errorf("answers = %v, want [%s]", a.calls, tt.want)
			}
		})
	}
}

func TestHandleSurvivesAPanic(t *testing.T) {
	c, _ := newConsumer(t, handlerFunc(func(context.Context, []byte) error { panic("boom") }))
	a := &acker{}
	c.handle(context.Background(), amqp.Delivery{Acknowledger: a, DeliveryTag: 1, Body: []byte(`{}`)})
	if len(a.calls) != 1 || a.calls[0] != "nack requeue=true" {
		t.Errorf("answers = %v, want one requeue like any other failure", a.calls)
	}
}

func TestHandleGivesTheHandlerATimeLimit(t *testing.T) {
	var hadDeadline bool
	c, _ := newConsumer(t, handlerFunc(func(ctx context.Context, _ []byte) error {
		_, hadDeadline = ctx.Deadline()
		return nil
	}))
	c.handle(context.Background(), amqp.Delivery{Acknowledger: &acker{}, Body: []byte(`{}`)})
	if !hadDeadline {
		t.Error("the handler context has no deadline")
	}
}

func TestHandleDoesNotWaitForTheRetryDelayWhenStopping(t *testing.T) {
	c, _ := newConsumer(t, handlerFunc(func(context.Context, []byte) error { return errors.New("x") }))
	c.cfg.RetryDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := &acker{}
	done := make(chan struct{})
	go func() {
		c.handle(ctx, amqp.Delivery{Acknowledger: a, Body: []byte(`{}`)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle kept waiting for the retry delay after the context ended")
	}
	if len(a.calls) != 1 || a.calls[0] != "nack requeue=true" {
		t.Errorf("answers = %v, want the message handed back", a.calls)
	}
}

func TestNewRejectsAnUnusableURLWithoutEchoingIt(t *testing.T) {
	for _, u := range []string{"", "not a url", "http://broker:5672", "amqp://", "amqp://user:pw@"} {
		_, err := New(Config{URL: u}, handlerFunc(nil), slog.Default())
		if err == nil {
			t.Errorf("New(%q) accepted the URL", u)
			continue
		}
		if strings.Contains(err.Error(), "pw") {
			t.Errorf("New(%q) error %q contains the password", u, err)
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.Queue != "realtime.events" || c.Prefetch != 20 || c.RetryDelay != time.Second ||
		c.MinBackoff != time.Second || c.MaxBackoff != 30*time.Second {
		t.Errorf("defaults = %+v", c)
	}
}

// Without a broker the consumer keeps trying and stops promptly when asked; it must not log the password.
func TestRunKeepsTryingAndStopsWithTheContext(t *testing.T) {
	c, logs := newConsumer(t, handlerFunc(func(context.Context, []byte) error { return nil }))
	c.cfg.URL = "amqp://clan:secret-pw@127.0.0.1:1/" // nothing listens on port 1
	c.cfg.MinBackoff, c.cfg.MaxBackoff = time.Millisecond, 5*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
	if n := strings.Count(logs.String(), "will reconnect"); n < 2 {
		t.Errorf("logged %d reconnect attempts, want at least 2:\n%s", n, logs)
	}
	if strings.Contains(logs.String(), "secret-pw") {
		t.Errorf("the log contains the broker password:\n%s", logs)
	}
}

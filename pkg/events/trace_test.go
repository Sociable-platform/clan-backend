package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const testTraceID = "0af7651916cd43dd8448eb211c80319c"

// tracedContext is a context inside a (remote, sampled) trace, with the W3C propagator in place
// as otelx.Setup leaves it.
func tracedContext(t *testing.T) context.Context {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	traceID, _ := trace.TraceIDFromHex(testTraceID)
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
}

func TestCaptureTraceOutsideATraceIsNil(t *testing.T) {
	if got := captureTrace(context.Background()); got != nil {
		t.Errorf("captureTrace = %v, want nil", got)
	}
}

func TestTraceSurvivesTheOutboxButStaysOutOfTheMessage(t *testing.T) {
	ctx := tracedContext(t)
	e := New("clan.created", "g1", map[string]any{"clan_id": "c1"})

	// What Enqueue stores.
	payload, err := json.Marshal(storedEvent{Event: e, Trace: captureTrace(ctx)})
	if err != nil {
		t.Fatal(err)
	}
	// What the relay reads back.
	var stored storedEvent
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Trace["traceparent"], testTraceID) {
		t.Fatalf("stored trace = %v, want a traceparent with %s", stored.Trace, testTraceID)
	}

	// What Publish sends as the body: the event alone.
	relayed := stored.Event
	relayed.trace = stored.Trace
	body, err := json.Marshal(relayed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "_trace") || strings.Contains(string(body), "traceparent") {
		t.Errorf("message body carries the trace: %s", body)
	}

	// And as headers: the same trace, which the consumer continues.
	pubCtx, end := startPublish(context.Background(), relayed)
	end(nil)
	headers := traceHeaders(pubCtx)
	if got, _ := headers["traceparent"].(string); !strings.Contains(got, testTraceID) {
		t.Fatalf("headers = %v, want a traceparent with %s", headers, testTraceID)
	}
	consumeCtx, done := StartConsume(context.Background(), amqp.Delivery{Headers: headers, RoutingKey: "clan.created"}, "realtime.events")
	done(nil)
	if got := trace.SpanContextFromContext(consumeCtx).TraceID().String(); got != testTraceID {
		t.Errorf("consumer trace = %s, want %s", got, testTraceID)
	}
}

func TestNoTraceMeansNoHeaders(t *testing.T) {
	ctx, end := startPublish(context.Background(), New("clan.created", "g1", nil))
	end(nil)
	if headers := traceHeaders(ctx); headers != nil {
		t.Errorf("headers = %v, want none", headers)
	}
}

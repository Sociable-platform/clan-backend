package events

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Trace context travels with an event in three steps, so a trace does not stop at the queue:
//
//  1. Enqueue runs inside the request. It stores the request's trace context with the outbox row
//     (the "_trace" key of the payload), because the relay publishes later, outside the request.
//  2. Publish starts a "publish" span under that context and writes it to the AMQP message headers.
//  3. The consumer reads the headers and runs its handler in a "process" span under the publisher's.
//
// An event written or received outside a trace gets no span and no header.

const tracerName = "gitlab.com/clan-platform/clan-backend/pkg/events"

// storedEvent is an outbox row's payload: the event as it is published, plus its trace context.
type storedEvent struct {
	Event
	Trace map[string]string `json:"_trace,omitempty"`
}

// captureTrace returns the trace context of ctx as text, or nil when ctx is not in a trace.
func captureTrace(ctx context.Context) map[string]string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}

// startPublish starts the span of one publish, under the trace the event was created in (or the
// one ctx is in). The returned function ends it; it does nothing when there is no trace.
func startPublish(ctx context.Context, e Event) (context.Context, func(error)) {
	if len(e.trace) > 0 {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(e.trace))
	}
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, func(error) {}
	}
	ctx, span := otel.Tracer(tracerName).Start(ctx, e.Type+" publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", Exchange),
			attribute.String("messaging.rabbitmq.destination.routing_key", e.Type),
			attribute.String("messaging.message.id", e.ID),
		),
	)
	return ctx, endSpan(span)
}

// StartConsume continues, in the consumer, the trace a message carries in its headers: it starts
// the "process" span of one delivery and returns the context to handle it with. Call the returned
// function with the handler's error when it is done. A message without trace headers gets no span.
func StartConsume(ctx context.Context, d amqp.Delivery, queue string) (context.Context, func(error)) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier(d.Headers))
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, func(error) {}
	}
	ctx, span := otel.Tracer(tracerName).Start(ctx, d.RoutingKey+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", queue),
			attribute.String("messaging.rabbitmq.destination.routing_key", d.RoutingKey),
			attribute.String("messaging.message.id", d.MessageId),
		),
	)
	return ctx, endSpan(span)
}

func endSpan(span trace.Span) func(error) {
	return func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// traceHeaders returns the AMQP headers that carry ctx's trace, or nil outside a trace.
func traceHeaders(ctx context.Context) amqp.Table {
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(headers))
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// headerCarrier lets the propagator read and write AMQP message headers.
type headerCarrier amqp.Table

func (c headerCarrier) Get(key string) string {
	value, _ := c[key].(string)
	return value
}

func (c headerCarrier) Set(key, value string) { c[key] = value }

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

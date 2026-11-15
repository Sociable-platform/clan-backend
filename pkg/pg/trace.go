package pg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "gitlab.com/clan-platform/clan-backend/pkg/pg"

// queryTracer turns every query that runs inside a trace into a span under it, so a slow query
// shows up in the request that caused it. A query outside a trace (the outbox relay's polling, the
// cleanup loops) gets no span: it would be a trace of its own every few hundred milliseconds.
//
// The span carries the SQL text. Every query in this repository passes its values as parameters
// ($1, $2), so the text holds no player data; the parameter values are never recorded.
type queryTracer struct{ tracer trace.Tracer }

type querySpanKey struct{}

func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	ctx, span := t.tracer.Start(ctx, operation(data.SQL),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.query.text", data.SQL),
		),
	)
	// Kept under a key of its own: TraceQueryEnd must end this span, never the request's.
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(querySpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

// operation is the span name: the first word of the statement in capitals ("SELECT", "INSERT",
// "BEGIN"). A handful of names, however many different queries there are.
func operation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "QUERY"
	}
	return strings.ToUpper(fields[0])
}

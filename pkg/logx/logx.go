// Package logx builds the structured JSON logger every service uses.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// New returns a JSON logger that tags every line with the service name.
// level is one of debug, info, warn, error (default info).
//
// A line logged with a context (log.InfoContext(ctx, ...)) that belongs to a traced request also
// gets trace_id and span_id, so the line can be linked to its trace. Lines logged without a
// context, or outside a trace, are unchanged.
func New(service, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(WithTrace(h)).With("service", service)
}

// WithTrace wraps h so that every record logged inside a span gets the span's trace_id (32 hex
// characters) and span_id (16 hex characters), the format Datadog and the other backends link on.
func WithTrace(h slog.Handler) slog.Handler { return traceHandler{h} }

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

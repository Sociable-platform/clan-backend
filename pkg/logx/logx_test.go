package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceIDsAreAddedInsideASpan(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(WithTrace(slog.NewJSONHandler(&out, nil))).With("service", "social")

	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	log.InfoContext(ctx, "clan created")
	log.Info("no context")

	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var traced, plain map[string]any
	if err := json.Unmarshal(lines[0], &traced); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &plain); err != nil {
		t.Fatal(err)
	}

	if traced["trace_id"] != "0af7651916cd43dd8448eb211c80319c" || traced["span_id"] != "b7ad6b7169203331" {
		t.Errorf("traced line = %v, want the span's trace_id and span_id", traced)
	}
	if traced["service"] != "social" {
		t.Errorf("traced line lost its service attribute: %v", traced)
	}
	if _, ok := plain["trace_id"]; ok {
		t.Errorf("line without a span got a trace_id: %v", plain)
	}
}

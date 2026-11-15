package otelx

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestOffWithoutEndpoint(t *testing.T) {
	t.Setenv(EndpointEnv, "")

	shutdown, err := start(context.Background(), "social")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if shutdown != nil {
		t.Fatal("start built providers without " + EndpointEnv)
	}

	// Setup still returns a stop function, so main can defer it either way.
	Setup(context.Background(), "social", discard)()
}

func TestResourceDescribesTheService(t *testing.T) {
	t.Setenv(EnvironmentEnv, "production")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	t.Setenv("OTEL_SERVICE_NAME", "")

	res, err := newResource(context.Background(), "realtime")
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}

	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	want := map[string]string{
		"service.name":           "realtime",
		"service.version":        Version,
		"deployment.environment": "production",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestEnvironmentDefaultsToLocal(t *testing.T) {
	t.Setenv(EnvironmentEnv, "")

	if got := environment(); got != "local" {
		t.Errorf("environment() = %q, want local", got)
	}
}

// With an endpoint nobody listens on, the service must still start at once and stop in time.
func TestStartsAndStopsWithoutACollector(t *testing.T) {
	t.Setenv(EndpointEnv, "http://127.0.0.1:1")

	started := time.Now()
	shutdown, err := start(context.Background(), "platform")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if shutdown == nil {
		t.Fatal("start built nothing although " + EndpointEnv + " is set")
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("start took %s: it must not wait for the collector", took)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopping := time.Now()
	_ = shutdown(ctx) // an error is expected: nothing listens there
	if took := time.Since(stopping); took > 3*time.Second {
		t.Errorf("shutdown took %s, past its 2s deadline", took)
	}
}

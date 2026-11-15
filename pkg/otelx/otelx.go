// Package otelx sets up OpenTelemetry tracing and metrics the same way for every service, so
// platform, social and realtime do not each build their own providers.
//
// Telemetry is off unless OTEL_EXPORTER_OTLP_ENDPOINT is set: a local run, docker compose and
// go test set nothing, start no exporter and never try to reach a collector.
package otelx

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// EndpointEnv switches telemetry on: the OTLP gRPC address of the collector, for example
	// http://10.0.0.5:4317. The exporters read it themselves; "http://" means no TLS.
	EndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// EnvironmentEnv names where the service runs (deployment.environment), "local" when unset.
	EnvironmentEnv = "DEPLOYMENT_ENVIRONMENT"

	// flushTimeout is how long a stopping service waits for its last spans and metrics to be sent.
	flushTimeout = 5 * time.Second
)

// Version is the commit the binary was built from (service.version). The Dockerfiles set it:
//
//	go build -ldflags "-X gitlab.com/clan-platform/clan-backend/pkg/otelx.Version=<commit sha>"
//
// A binary built without it (go run, go test, docker compose) reports "dev".
var Version = "dev"

// Setup starts tracing and metrics for service ("platform", "social" or "realtime") and returns
// the function that stops them. Call it once in main and defer the result:
//
//	defer otelx.Setup(ctx, serviceName, log)()
//
// The returned function sends what is still waiting (at most flushTimeout), so defer it early: it
// then runs after the server and the workers have stopped. Without the endpoint it does nothing.
// A setup that fails is logged and the service starts without telemetry: tracing never stops a
// service from starting.
func Setup(ctx context.Context, service string, log *slog.Logger) (stop func()) {
	shutdown, err := start(ctx, service)
	switch {
	case err != nil:
		log.Error("telemetry is off: setup failed", "error", err)
		return func() {}
	case shutdown == nil:
		log.Info("telemetry is off: " + EndpointEnv + " is not set")
		return func() {}
	}

	// A collector that is down or slow is reported here, in the service's own log format,
	// instead of on stderr. It never fails a request.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("telemetry export failed", "error", err)
	}))
	log.Info("telemetry enabled", "version", Version, "environment", environment())

	return func() {
		// The context of main is already cancelled when this runs, so the flush gets its own.
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			log.Warn("telemetry did not flush cleanly", "error", err)
		}
	}
}

// start builds the providers and makes them the global ones. It returns nil, nil when the
// endpoint is not set.
func start(ctx context.Context, service string) (func(context.Context) error, error) {
	if os.Getenv(EndpointEnv) == "" {
		return nil, nil
	}

	res, err := newResource(ctx, service)
	if err != nil {
		return nil, err
	}

	// Both exporters take the endpoint (and the other OTEL_EXPORTER_OTLP_* settings) from the
	// environment. They connect in the background, so a collector that is down does not block.
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, errors.Join(err, traceExporter.Shutdown(ctx))
	}

	tracers := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	meters := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracers)
	otel.SetMeterProvider(meters)
	// W3C trace context, so a trace continues from one service to the next.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return func(ctx context.Context) error {
		return errors.Join(tracers.Shutdown(ctx), meters.Shutdown(ctx))
	}, nil
}

// newResource describes the service on every span and metric. OTEL_RESOURCE_ATTRIBUTES and
// OTEL_SERVICE_NAME, when set, add to these and win over them.
func newResource(ctx context.Context, service string) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", service),
			attribute.String("service.version", Version),
			attribute.String("deployment.environment", environment()),
		),
		resource.WithFromEnv(),
	)
	// A partial resource (one detector failed) still has our attributes: keep it.
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, err
	}
	return res, nil
}

func environment() string {
	if env := os.Getenv(EnvironmentEnv); env != "" {
		return env
	}
	return "local"
}

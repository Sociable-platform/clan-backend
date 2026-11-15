package httpx

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Trace starts a span for every request and continues the caller's trace when the request carries
// one (the traceparent header). Put it first in Chain, so Recover and RequestLog run inside the
// span and their log lines carry its trace_id.
//
// The span is named after the route pattern ("GET /api/v1/clans/{clanID}"), never the raw path,
// or every clan ID would be an endpoint of its own. Health probes and WebSocket upgrades are not
// traced: the first are noise, the second would be one span as long as the connection.
//
// Without otelx.Setup (no OTEL_EXPORTER_OTLP_ENDPOINT) the spans go nowhere and cost almost nothing.
func Trace() Middleware { return traceWith() }

// traceWith is Trace with extra options, so a test can record the spans.
func traceWith(opts ...otelhttp.Option) Middleware {
	opts = append([]otelhttp.Option{
		otelhttp.WithSpanNameFormatter(spanName),
		otelhttp.WithFilter(traced),
	}, opts...)
	return otelhttp.NewMiddleware("", opts...)
}

// spanName is called twice: before routing, when the pattern is still unknown, and again after
// the handler, when the mux has filled in r.Pattern.
func spanName(_ string, r *http.Request) string {
	switch {
	case r.Pattern == "":
		return r.Method // no route (yet): the method alone, not the path
	case strings.HasPrefix(r.Pattern, r.Method+" "):
		return r.Pattern // the pattern already names the method: "GET /api/v1/clans/{clanID}"
	default:
		return r.Method + " " + r.Pattern
	}
}

func traced(r *http.Request) bool {
	if r.URL.Path == "/health" || r.URL.Path == "/ready" {
		return false
	}
	return !strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// Transport is the transport for a service's own HTTP clients: every call becomes a span and
// carries the trace on to the service it calls.
func Transport() http.RoundTripper { return otelhttp.NewTransport(http.DefaultTransport) }

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceNamesSpansAfterTheRoute(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /api/v1/clans/{clanID}", ok)
	mux.HandleFunc("GET /health", ok)
	h := Chain(mux, traceWith(otelhttp.WithTracerProvider(provider)), RequestLog(discardLogger()))

	get := func(path string, header ...string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if len(header) == 2 {
			r.Header.Set(header[0], header[1])
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	get("/api/v1/clans/42")
	get("/api/v1/clans/43")
	get("/health")
	get("/api/v1/clans/44", "Upgrade", "websocket")
	get("/no/such/route")

	var names []string
	for _, span := range recorder.Ended() {
		names = append(names, span.Name())
	}
	want := []string{"GET /api/v1/clans/{clanID}", "GET /api/v1/clans/{clanID}", "GET"}
	if len(names) != len(want) {
		t.Fatalf("spans = %q, want %q", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("span %d = %q, want %q", i, names[i], want[i])
		}
	}
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

type fakeService struct{ err error }

// passThrough stands in for the player-token middleware in tests that are not about tokens.
func passThrough(next http.Handler) http.Handler { return next }

func (f fakeService) Ready(context.Context) error { return f.err }

func (f fakeService) EnsurePlayer(context.Context, string, string, string) (service.Player, error) {
	return service.Player{}, f.err
}

// testLog is a logger that discards everything, for tests.
func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func serve(t *testing.T, svc Service, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, passThrough, testLog()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealth(t *testing.T) {
	rec := serve(t, fakeService{}, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestReady(t *testing.T) {
	if rec := serve(t, fakeService{}, http.MethodGet, "/ready"); rec.Code != http.StatusOK {
		t.Fatalf("ready with database: status %d", rec.Code)
	}
	rec := serve(t, fakeService{err: errors.New("no db")}, http.MethodGet, "/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready without database: status %d", rec.Code)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "not_ready" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	rec := serve(t, fakeService{}, http.MethodGet, "/nope")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") == "" {
		t.Fatalf("status %d, content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

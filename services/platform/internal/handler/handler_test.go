package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeService struct{ err error }

func (f fakeService) Ready(context.Context) error { return f.err }

func serve(t *testing.T, svc Service, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc).Register(mux)
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

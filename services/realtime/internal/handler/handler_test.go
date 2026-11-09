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

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
)

type fakeService struct{ err error }

// noVerifier rejects every token; the tests in this file are not about the WebSocket.
type noVerifier struct{}

func (noVerifier) Verify(string) (auth.Player, error) {
	return auth.Player{}, httpx.NewError(http.StatusUnauthorized, "invalid_token", "no")
}

func (f fakeService) Ready(context.Context) error { return f.err }

// testLog is a logger that discards everything, for tests.
func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func serve(t *testing.T, svc Service, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New("test", svc, WSOptions{Verifier: noVerifier{}}, testLog()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// errorCode returns the "error.code" of a response body, or "" when there is none.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	return body.Error.Code
}

func TestHealthAndReady(t *testing.T) {
	tests := []struct {
		name       string
		svc        fakeService
		method     string
		path       string
		wantStatus int
		wantCode   string // error code, empty for success
	}{
		{"health", fakeService{}, http.MethodGet, "/health", http.StatusOK, ""},
		{"health does not need the database", fakeService{err: errors.New("no db")}, http.MethodGet, "/health", http.StatusOK, ""},
		{"ready with database", fakeService{}, http.MethodGet, "/ready", http.StatusOK, ""},
		{"ready without database", fakeService{err: errors.New("no db")}, http.MethodGet, "/ready", http.StatusServiceUnavailable, "not_ready"},
		{"unknown route", fakeService{}, http.MethodGet, "/nope", http.StatusNotFound, "not_found"},
		{"wrong method is a JSON 404", fakeService{}, http.MethodPost, "/health", http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, tt.svc, tt.method, tt.path)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCode != "" {
				if got := errorCode(t, rec); got != tt.wantCode {
					t.Fatalf("error code %q, want %q", got, tt.wantCode)
				}
			}
		})
	}
}

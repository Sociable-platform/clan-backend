package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrKeepsAppErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErr(rec, NewError(http.StatusConflict, "clan_full", "Clan has reached 10 members"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "clan_full" || body.Error.Message == "" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestWriteErrHidesInternalErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErr(rec, errors.New("pq: password authentication failed for user secret"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
}

func TestDecodeJSON(t *testing.T) {
	type in struct {
		Name string `json:"name"`
	}
	tests := map[string]struct {
		body string
		ok   bool
	}{
		"valid":         {`{"name":"Wolves"}`, true},
		"unknown field": {`{"name":"Wolves","admin":true}`, false},
		"two objects":   {`{"name":"a"}{"name":"b"}`, false},
		"not json":      {`hello`, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			var v in
			err := DecodeJSON(httptest.NewRecorder(), r, &v)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestRecoverMiddleware(t *testing.T) {
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), Recover(discardLogger()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
}

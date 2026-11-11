package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testInternalToken = "0123456789abcdef0123456789abcdef"

func TestRequireInternal(t *testing.T) {
	cases := []struct {
		name       string
		token      string // the configured secret
		header     *string
		wantStatus int
		wantCode   string
	}{
		{"right token", testInternalToken, ptr(testInternalToken), http.StatusNoContent, ""},
		{"no header", testInternalToken, nil, http.StatusUnauthorized, "invalid_internal_token"},
		{"empty header", testInternalToken, ptr(""), http.StatusUnauthorized, "invalid_internal_token"},
		{"wrong token", testInternalToken, ptr("wrong"), http.StatusUnauthorized, "invalid_internal_token"},
		{"token with extra text", testInternalToken, ptr(testInternalToken + "x"), http.StatusUnauthorized, "invalid_internal_token"},
		{"only the start of the token", testInternalToken, ptr(testInternalToken[:10]), http.StatusUnauthorized, "invalid_internal_token"},
		{"switched off, no header", "", nil, http.StatusNotFound, "not_found"},
		{"switched off, empty header", "", ptr(""), http.StatusNotFound, "not_found"},
		{"switched off, any header", "", ptr(testInternalToken), http.StatusNotFound, "not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			h := RequireInternal(c.token)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/friendships", nil)
			if c.header != nil {
				req.Header.Set(InternalTokenHeader, *c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.wantStatus, rec.Body)
			}
			if reached != (c.wantStatus == http.StatusNoContent) {
				t.Errorf("handler reached = %v for status %d", reached, rec.Code)
			}
			if c.wantCode != "" && !strings.Contains(rec.Body.String(), `"code":"`+c.wantCode+`"`) {
				t.Errorf("body = %s, want code %s", rec.Body, c.wantCode)
			}
			// The secret must never come back in an answer.
			if strings.Contains(rec.Body.String(), testInternalToken) {
				t.Error("the answer contains the internal token")
			}
		})
	}
}

func ptr(s string) *string { return &s }

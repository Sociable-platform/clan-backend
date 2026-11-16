package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const (
	resetPath        = "/api/v1/developers/password-reset"
	resetConfirmPath = "/api/v1/developers/password-reset/confirm"
)

// resetServer keeps one Handler for a whole test, so its rate limits count across the requests
// (call builds a new Handler, with fresh limits, every time).
func resetServer(svc Service) func(path, body, forwardedFor string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	New("test", svc, discardLog()).Register(mux)
	return func(path, body, forwardedFor string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
}

func TestRequestPasswordReset_Accepted(t *testing.T) {
	var got string
	post := resetServer(fakeService{requestPasswordReset: func(email string) error { got = email; return nil }})

	rec := post(resetPath, `{"email":"dev@example.com"}`, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got != "dev@example.com" {
		t.Fatalf("service got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"message"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRequestPasswordReset_Errors(t *testing.T) {
	post := resetServer(fakeService{requestPasswordReset: func(string) error {
		return httpx.NewError(http.StatusBadRequest, "invalid_email", "Email address is not valid")
	}})

	if rec := post(resetPath, `not json`, ""); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
		t.Fatalf("bad body: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(resetPath, `{"email":"nope"}`, ""); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_email" {
		t.Fatalf("invalid email: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestPasswordReset_LimitedPerEmail(t *testing.T) {
	calls := 0
	post := resetServer(fakeService{requestPasswordReset: func(string) error { calls++; return nil }})

	// Each request comes from another address, so only the limit per email can refuse it.
	for i := 0; i < resetPerEmail; i++ {
		ip := "10.0.0." + string(rune('1'+i))
		if rec := post(resetPath, `{"email":"dev@example.com"}`, ip); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The same address in another spelling counts as the same email.
	rec := post(resetPath, `{"email":" DEV@example.com "}`, "10.0.0.9")
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "900" {
		t.Fatalf("Retry-After %q, want 900", rec.Header().Get("Retry-After"))
	}
	if calls != resetPerEmail {
		t.Fatalf("the service was called %d times, want %d", calls, resetPerEmail)
	}
	// Another email is not affected.
	if rec := post(resetPath, `{"email":"other@example.com"}`, "10.0.0.9"); rec.Code != http.StatusAccepted {
		t.Fatalf("another email: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestPasswordReset_LimitedPerIP(t *testing.T) {
	post := resetServer(fakeService{requestPasswordReset: func(string) error { return nil }})

	for i := 0; i < resetPerIP; i++ {
		body := `{"email":"dev` + strings.Repeat("x", i) + `@example.com"}`
		if rec := post(resetPath, body, "203.0.113.7"); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if rec := post(resetPath, `{"email":"last@example.com"}`, "203.0.113.7"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(resetPath, `{"email":"last@example.com"}`, "203.0.113.8"); rec.Code != http.StatusAccepted {
		t.Fatalf("another address: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConfirmPasswordReset(t *testing.T) {
	var got service.ResetPasswordInput
	post := resetServer(fakeService{resetPassword: func(in service.ResetPasswordInput) error {
		got = in
		if in.Token != "good" {
			return httpx.NewError(http.StatusBadRequest, "invalid_reset_token", "no")
		}
		return nil
	}})

	rec := post(resetConfirmPath, `{"token":"good","password":"a brand new one"}`, "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got.Token != "good" || got.Password != "a brand new one" {
		t.Fatalf("service got %+v", got)
	}

	rec = post(resetConfirmPath, `{"token":"bad","password":"a brand new one"}`, "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_reset_token" {
		t.Fatalf("bad token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name, forwardedFor, remoteAddr, want string
	}{
		{"no proxy", "", "198.51.100.4:51234", "198.51.100.4"},
		{"one proxy", "203.0.113.7", "10.42.0.1:1234", "203.0.113.7"},
		{"the caller's own entries are ignored", "1.2.3.4, 203.0.113.7", "10.42.0.1:1234", "203.0.113.7"},
		{"remote address without a port", "", "198.51.100.4", "198.51.100.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}
			if got := clientIP(req); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// linkCatcher is the service's ResetSender in the flow test below: it keeps the last link.
type linkCatcher struct{ link string }

func (c *linkCatcher) SendPasswordReset(_, _, link string) { c.link = link }

// TestPasswordResetFlow runs register -> forgot password -> reset -> login through the real
// handler and service.
func TestPasswordResetFlow(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	catcher := &linkCatcher{}
	svc.WithPasswordReset(catcher, "https://studio.example.com")
	post := resetServer(svc)

	if rec := post("/api/v1/developers/register", `{"email":"dev@example.com","password":"correct horse","name":"Dev One"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	// An unknown email gets the same answer and no link.
	if rec := post(resetPath, `{"email":"nobody@example.com"}`, ""); rec.Code != http.StatusAccepted || catcher.link != "" {
		t.Fatalf("unknown email: %d %s, link %q", rec.Code, rec.Body.String(), catcher.link)
	}
	if rec := post(resetPath, `{"email":"dev@example.com"}`, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d %s", rec.Code, rec.Body.String())
	}
	_, token, found := strings.Cut(catcher.link, "/reset-password#token=")
	if !found || token == "" {
		t.Fatalf("link %q", catcher.link)
	}

	confirm := `{"token":"` + token + `","password":"a brand new one"}`
	if rec := post(resetConfirmPath, confirm, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(resetConfirmPath, confirm, ""); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_reset_token" {
		t.Fatalf("second confirm: %d %s", rec.Code, rec.Body.String())
	}

	if rec := post("/api/v1/developers/login", `{"email":"dev@example.com","password":"correct horse"}`, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with the old password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/v1/developers/login", `{"email":"dev@example.com","password":"a brand new one"}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("login with the new password: %d %s", rec.Code, rec.Body.String())
	}
}

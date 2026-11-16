package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const (
	loginPath   = "/api/v1/developers/login"
	refreshPath = "/api/v1/developers/token/refresh"
	logoutPath  = "/api/v1/developers/logout"
)

// sessionServer keeps one Handler for a test and sends requests with an optional refresh cookie.
func sessionServer(h *Handler) func(path, body, refreshCookie string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.Register(mux)
	return func(path, body, refreshCookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if refreshCookie != "" {
			req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: refreshCookie})
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
}

// refreshCookieOf returns the refresh cookie a response sets, or nil.
func refreshCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshCookieName {
			return c
		}
	}
	return nil
}

func wantClearedCookie(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := refreshCookieOf(rec)
	if c == nil {
		t.Fatal("the refresh cookie was not cleared: no Set-Cookie")
	}
	if c.Value != "" || c.MaxAge >= 0 || c.Path != refreshCookiePath {
		t.Fatalf("the refresh cookie was not cleared: %+v", c)
	}
}

func TestLoginDeveloper_SetsTheRefreshCookie(t *testing.T) {
	svc := fakeService{login: func(service.LoginInput) (service.Session, error) {
		return service.Session{Token: "jwt", ExpiresIn: 900, RefreshToken: "refresh-1", RefreshExpiresIn: 604800}, nil
	}}
	post := sessionServer(New("test", svc, discardLog()))

	rec := post(loginPath, `{"email":"dev@example.com","password":"correct horse"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	c := refreshCookieOf(rec)
	if c == nil {
		t.Fatal("no refresh cookie")
	}
	if c.Value != "refresh-1" || c.Path != "/api/v1/developers" || c.MaxAge != 604800 {
		t.Fatalf("cookie %+v", c)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie must be HttpOnly, Secure and SameSite=Lax: %+v", c)
	}
	// The refresh token is in the cookie only, never in the body.
	if strings.Contains(rec.Body.String(), "refresh-1") {
		t.Fatalf("the body holds the refresh token: %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestLoginDeveloper_InsecureCookiesForLocalhost(t *testing.T) {
	svc := fakeService{login: func(service.LoginInput) (service.Session, error) {
		return service.Session{Token: "jwt", ExpiresIn: 900, RefreshToken: "refresh-1", RefreshExpiresIn: 60}, nil
	}}
	post := sessionServer(New("test", svc, discardLog()).WithInsecureCookies())

	c := refreshCookieOf(post(loginPath, `{"email":"dev@example.com","password":"correct horse"}`, ""))
	if c == nil || c.Secure || !c.HttpOnly {
		t.Fatalf("cookie %+v", c)
	}
}

func TestRefreshDeveloperToken(t *testing.T) {
	var got string
	svc := fakeService{refreshSession: func(refreshToken string) (service.Session, error) {
		got = refreshToken
		return service.Session{Token: "jwt-2", ExpiresIn: 900, RefreshToken: "refresh-2", RefreshExpiresIn: 604800}, nil
	}}
	post := sessionServer(New("test", svc, discardLog()))

	rec := post(refreshPath, "", "refresh-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got != "refresh-1" {
		t.Fatalf("service got %q", got)
	}
	var body loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token != "jwt-2" || body.ExpiresIn != 900 {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
	if c := refreshCookieOf(rec); c == nil || c.Value != "refresh-2" || c.MaxAge != 604800 {
		t.Fatalf("cookie %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestRefreshDeveloperToken_RefusedClearsTheCookie(t *testing.T) {
	var got []string
	svc := fakeService{refreshSession: func(refreshToken string) (service.Session, error) {
		got = append(got, refreshToken)
		return service.Session{}, httpx.NewError(http.StatusUnauthorized, "invalid_refresh_token", "Your session has ended. Log in again.")
	}}
	post := sessionServer(New("test", svc, discardLog()))

	for _, cookie := range []string{"", "old-token"} {
		rec := post(refreshPath, "", cookie)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_refresh_token" {
			t.Fatalf("cookie %q: %d %s", cookie, rec.Code, rec.Body.String())
		}
		wantClearedCookie(t, rec)
	}
	if len(got) != 2 || got[0] != "" || got[1] != "old-token" {
		t.Fatalf("service got %q", got)
	}
}

func TestRefreshDeveloperToken_AServerErrorKeepsTheCookie(t *testing.T) {
	svc := fakeService{refreshSession: func(string) (service.Session, error) {
		return service.Session{}, errors.New("database is down")
	}}
	post := sessionServer(New("test", svc, discardLog()))

	rec := post(refreshPath, "", "refresh-1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if c := refreshCookieOf(rec); c != nil {
		t.Fatalf("a failing database must not log the developer out: %+v", c)
	}
}

func TestLogoutDeveloper(t *testing.T) {
	var got []string
	svc := fakeService{logout: func(refreshToken string) error { got = append(got, refreshToken); return nil }}
	post := sessionServer(New("test", svc, discardLog()))

	for _, cookie := range []string{"refresh-1", ""} {
		rec := post(logoutPath, "", cookie)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("cookie %q: %d %s", cookie, rec.Code, rec.Body.String())
		}
		wantClearedCookie(t, rec)
	}
	if len(got) != 2 || got[0] != "refresh-1" || got[1] != "" {
		t.Fatalf("service got %q", got)
	}
}

func TestSessionEndpoints_LimitedPerIP(t *testing.T) {
	svc := fakeService{logout: func(string) error { return nil }}
	post := sessionServer(New("test", svc, discardLog()))

	for i := 0; i < sessionPerIP; i++ {
		if rec := post(logoutPath, "", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// token/refresh and logout share the count.
	if rec := post(refreshPath, "", "refresh-1"); rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDeveloperSessionFlow runs login -> refresh -> logout through the real handler and service.
func TestDeveloperSessionFlow(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	svc, err := service.New(store, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := New("test", svc, discardLog())
	post := sessionServer(h)

	if rec := post("/api/v1/developers/register", `{"email":"dev@example.com","password":"correct horse","name":"Dev One"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	// No session yet: nothing to refresh.
	if rec := post(refreshPath, "", ""); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_refresh_token" {
		t.Fatalf("refresh without a cookie: %d %s", rec.Code, rec.Body.String())
	}

	rec := post(loginPath, `{"email":"dev@example.com","password":"correct horse"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	first := refreshCookieOf(rec)
	if first == nil || first.Value == "" {
		t.Fatal("login set no refresh cookie")
	}

	rec = post(refreshPath, "", first.Value)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	second := refreshCookieOf(rec)
	if second == nil || second.Value == "" || second.Value == first.Value {
		t.Fatalf("refresh did not replace the cookie: %+v", second)
	}
	var session loginResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &session)

	// The developer token from the refresh opens a developer endpoint.
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/developers/me", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	me := httptest.NewRecorder()
	mux.ServeHTTP(me, req)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"email":"dev@example.com"`) {
		t.Fatalf("me with the refreshed token: %d %s", me.Code, me.Body.String())
	}

	rec = post(logoutPath, "", second.Value)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	wantClearedCookie(t, rec)
	if len(store.refreshTokens) != 0 {
		t.Fatalf("%d refresh tokens left after logout", len(store.refreshTokens))
	}
	for _, old := range []string{first.Value, second.Value} {
		if rec := post(refreshPath, "", old); rec.Code != http.StatusUnauthorized {
			t.Fatalf("refresh after logout: %d %s", rec.Code, rec.Body.String())
		}
	}
}

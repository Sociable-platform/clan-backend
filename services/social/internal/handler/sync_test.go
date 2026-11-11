package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

const (
	syncPath      = "/internal/v1/friendships"
	syncToken     = "0123456789abcdef0123456789abcdef"
	syncGame      = "a6c1e2f0-1111-4222-8333-944455556666"
	syncPlayerOne = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"
	syncPlayerTwo = "8f1d2c3b-4a5e-4b6c-8d7e-9f0a1b2c3d4e"
)

// syncService answers the sync call with a fixed page and remembers what it was asked.
type syncService struct {
	fakeService
	page  service.FriendshipPage
	in    service.SyncFriendshipsInput
	calls int
}

func (s *syncService) ListFriendshipsForSync(_ context.Context, in service.SyncFriendshipsInput) (service.FriendshipPage, error) {
	s.calls++
	s.in = in
	return s.page, s.err
}

// serveSync sends a request to the sync route of a handler. guard nil means "internal routes not configured".
func serveSync(t *testing.T, svc Service, guard httpx.Middleware, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h := New("test", svc, passThrough, testLog())
	if guard != nil {
		h.WithInternal(guard)
	}
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set(httpx.InternalTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncFriendshipsHandler(t *testing.T) {
	since := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	svc := &syncService{page: service.FriendshipPage{
		Friendships: []service.SyncFriendship{{
			GameID:  syncGame,
			PlayerA: service.FriendPlayer{ID: syncPlayerOne, DisplayName: "Aysel"},
			PlayerB: service.FriendPlayer{ID: syncPlayerTwo, DisplayName: "Ali"},
			Since:   since,
		}},
		NextCursor: "abc",
	}}
	rec := serveSync(t, svc, httpx.RequireInternal(syncToken), syncPath+"?after=xyz&limit=50", syncToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if svc.in != (service.SyncFriendshipsInput{After: "xyz", Limit: "50"}) {
		t.Errorf("service was asked %+v, want after=xyz limit=50", svc.in)
	}
	want := `{"friendships":[{"gameId":"` + syncGame + `","playerA":{"id":"` + syncPlayerOne + `","displayName":"Aysel"},` +
		`"playerB":{"id":"` + syncPlayerTwo + `","displayName":"Ali"},"since":"2026-11-10T08:00:00Z"}],"nextCursor":"abc"}`
	if body := strings.TrimSpace(rec.Body.String()); body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

func TestSyncFriendshipsHandlerLastPageHasNoCursor(t *testing.T) {
	rec := serveSync(t, &syncService{}, httpx.RequireInternal(syncToken), syncPath, syncToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"friendships":[]}` {
		t.Errorf("body = %s, want an empty list and no nextCursor", body)
	}
}

func TestSyncFriendshipsHandlerPassesErrorsOn(t *testing.T) {
	bad := httpx.NewError(http.StatusBadRequest, "invalid_cursor", "after is not a cursor from a previous page")
	rec := serveSync(t, &syncService{fakeService: fakeService{err: bad}}, httpx.RequireInternal(syncToken), syncPath+"?after=zz", syncToken)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_cursor"`) {
		t.Errorf("got %d %s, want 400 invalid_cursor", rec.Code, rec.Body)
	}

	rec = serveSync(t, &syncService{fakeService: fakeService{err: errors.New("pq: password=secret")}}, httpx.RequireInternal(syncToken), syncPath, syncToken)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"internal_error"`) || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("got %d %s, want a generic 500 internal_error", rec.Code, rec.Body)
	}
}

func TestSyncFriendshipsNeedsTheInternalToken(t *testing.T) {
	cases := []struct {
		name  string
		guard httpx.Middleware
		token string
		want  int
	}{
		{"no token", httpx.RequireInternal(syncToken), "", http.StatusUnauthorized},
		{"wrong token", httpx.RequireInternal(syncToken), "wrong", http.StatusUnauthorized},
		{"right token", httpx.RequireInternal(syncToken), syncToken, http.StatusOK},
		{"not configured, no token", nil, "", http.StatusNotFound},
		{"not configured, any token", nil, syncToken, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &syncService{}
			rec := serveSync(t, svc, c.guard, syncPath, c.token)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body)
			}
			if (c.want == http.StatusOK) != (svc.calls == 1) {
				t.Errorf("service called %d times for status %d", svc.calls, rec.Code)
			}
		})
	}
}

// A player token is no key to the internal route, and the internal token is no key to a player route.
func TestSyncFriendshipsIsNotReachableWithAPlayerToken(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New("test", &syncService{}, auth.RequirePlayer(pub, time.Now), testLog()).WithInternal(httpx.RequireInternal(syncToken)).Register(mux)

	req := httptest.NewRequest(http.MethodGet, syncPath, nil)
	req.Header.Set("Authorization", "Bearer some.player.token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("player token on the internal route: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/friends", nil)
	req.Header.Set(httpx.InternalTokenHeader, syncToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("internal token on a player route: status = %d, want 401", rec.Code)
	}
}

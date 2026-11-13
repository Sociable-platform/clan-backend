package gameconfig

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const gameID = "11111111-1111-4111-8111-111111111111"

func newSource(t *testing.T, h http.HandlerFunc) (*HTTPSource, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	s, err := NewHTTPSource(srv.URL+"/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return s, &calls
}

func TestFetchReadsTheNewestPublishedConfig(t *testing.T) {
	var path, accept, cacheControl string
	s, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) {
		path, accept, cacheControl = r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Cache-Control")
		w.Write([]byte(`{"version":7,"config":{"theme":{"primary":"#fff"}}}`))
	})
	cfg, err := s.Fetch(context.Background(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 7 || string(cfg.Settings) != `{"theme":{"primary":"#fff"}}` {
		t.Errorf("config = %d %s", cfg.Version, cfg.Settings)
	}
	if path != "/api/v1/public/games/"+gameID+"/widget-config" {
		t.Errorf("path = %q", path)
	}
	if accept != "application/json" || cacheControl != "no-cache" {
		t.Errorf("headers Accept=%q Cache-Control=%q, want JSON and no-cache (never an old cached version)", accept, cacheControl)
	}
}

func TestFetchSendsNothingForAGameIDThatIsNotAUUID(t *testing.T) {
	s, calls := newSource(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	for _, id := range []string{"", "x", "../../internal", gameID + "/x", gameID + "?a=b", "11111111-1111-4111-8111-11111111111%2f"} {
		if _, err := s.Fetch(context.Background(), id); !errors.Is(err, ErrInvalidGame) {
			t.Errorf("Fetch(%q) = %v, want ErrInvalidGame", id, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("platform was called %d times for IDs that are not UUIDs", calls.Load())
	}
}

func TestFetchTellsNotPublishedFromEveryOtherFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"not published", 404, `{"error":{"code":"widget_not_published","message":"x"}}`, ErrNotPublished},
		{"another 404 is a wrong address", 404, `{"error":{"code":"not_found","message":"x"}}`, ErrUnavailable},
		{"404 that is not JSON", 404, `<html>`, ErrUnavailable},
		{"suspended", 403, `{"error":{"code":"game_suspended","message":"This game is suspended"}}`, ErrSuspended},
		{"another 403 says nothing about the game", 403, `{"error":{"code":"origin_not_allowed","message":"x"}}`, ErrUnavailable},
		{"403 that is not JSON", 403, `<html>forbidden</html>`, ErrUnavailable},
		{"server error", 500, `{"error":{"code":"internal_error","message":"x"}}`, ErrUnavailable},
		{"redirect is not followed", 302, ``, ErrUnavailable},
		{"not JSON", 200, `<html>`, ErrUnavailable},
		{"no version", 200, `{"config":{}}`, ErrUnavailable},
		{"version 0", 200, `{"version":0,"config":{}}`, ErrUnavailable},
		{"no config", 200, `{"version":3}`, ErrUnavailable},
		{"null config", 200, `{"version":3,"config":null}`, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.status == 302 {
					w.Header().Set("Location", "http://127.0.0.1:1/")
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			})
			if _, err := s.Fetch(context.Background(), gameID); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFetchFailsWhenPlatformIsDownOrSlow(t *testing.T) {
	s, err := NewHTTPSource("http://127.0.0.1:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch(context.Background(), gameID); !errors.Is(err, ErrUnavailable) {
		t.Errorf("down: error = %v, want ErrUnavailable", err)
	}

	slow, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	slow.http.Timeout = 50 * time.Millisecond
	if _, err := slow.Fetch(context.Background(), gameID); !errors.Is(err, ErrUnavailable) {
		t.Errorf("slow: error = %v, want ErrUnavailable", err)
	}
}

func TestNewHTTPSourceRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "platform:8081", "ftp://platform", "http://", "://x"} {
		if _, err := NewHTTPSource(u, time.Second); err == nil {
			t.Errorf("NewHTTPSource(%q) accepted", u)
		}
	}
}

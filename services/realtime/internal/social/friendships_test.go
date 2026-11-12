package social

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	internalSecret = "0123456789abcdef0123456789abcdef"
	playerOne      = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"
	playerTwo      = "8f1d2c3b-4a5e-4b6c-8d7e-9f0a1b2c3d4e"
)

const onePageBody = `{"friendships":[{"gameId":"` + "A6C1E2F0-1111-4222-8333-944455556666" + `",` +
	`"playerA":{"id":"` + playerOne + `","displayName":"Aysel"},` +
	`"playerB":{"id":"` + "8F1D2C3B-4A5E-4B6C-8D7E-9F0A1B2C3D4E" + `","displayName":"Ali"},` +
	`"since":"2026-11-10T08:00:00Z"}],"nextCursor":"next-1"}`

func TestFriendships(t *testing.T) {
	var gotPath, gotAfter, gotLimit, gotToken string
	c := fakeSocial(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAfter, gotLimit = r.URL.Path, r.URL.Query().Get("after"), r.URL.Query().Get("limit")
		gotToken = r.Header.Get("X-Internal-Token")
		w.Write([]byte(onePageBody))
	})
	page, err := c.WithInternalToken(internalSecret).Friendships(context.Background(), "cursor-0", 200)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/internal/v1/friendships" || gotAfter != "cursor-0" || gotLimit != "200" || gotToken != internalSecret {
		t.Errorf("request = %s after=%q limit=%q token=%q", gotPath, gotAfter, gotLimit, gotToken)
	}
	if page.NextCursor != "next-1" || len(page.Friendships) != 1 {
		t.Fatalf("page = %+v", page)
	}
	f := page.Friendships[0]
	// IDs are normalised to lower case, the form the database and the DM key use.
	want := Friendship{
		GameID: "a6c1e2f0-1111-4222-8333-944455556666",
		A:      Player{ID: playerOne, DisplayName: "Aysel"},
		B:      Player{ID: playerTwo, DisplayName: "Ali"},
	}
	if f != want {
		t.Errorf("friendship = %+v, want %+v", f, want)
	}
}

func TestFriendshipsFirstPageSendsNoCursor(t *testing.T) {
	c := fakeSocial(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("after") {
			t.Errorf("first page sent after=%q", r.URL.Query().Get("after"))
		}
		w.Write([]byte(`{"friendships":[]}`))
	})
	page, err := c.WithInternalToken(internalSecret).Friendships(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Friendships) != 0 || page.NextCursor != "" {
		t.Errorf("page = %+v, want an empty last page", page)
	}
}

func TestFriendshipsFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"wrong token", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }},
		{"route switched off", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"social fails", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"not json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }},
		{"json without friendships", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"players":[]}`)) }},
		{"redirect is not followed", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://elsewhere.invalid/", http.StatusFound)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeSocial(t, tt.handler).WithInternalToken(internalSecret)
			_, err := c.Friendships(context.Background(), "", 10)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if strings.Contains(err.Error(), internalSecret) {
				t.Error("the error contains the internal token")
			}
		})
	}
}

func TestFriendshipsWithoutTokenDoesNotCallSocial(t *testing.T) {
	called := false
	c := fakeSocial(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	_, err := c.Friendships(context.Background(), "", 10)
	if !errors.Is(err, ErrNoInternalToken) || errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrNoInternalToken only", err)
	}
	if called {
		t.Error("social was called without a token")
	}
}

func TestFriendshipsSocialDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	c, err := NewClient(url, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.WithInternalToken(internalSecret).Friendships(context.Background(), "", 10)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), internalSecret) {
		t.Errorf("err = %v, want ErrUnavailable without the token", err)
	}
}

func TestFriendshipsStopsWhenContextEnds(t *testing.T) {
	c := fakeSocial(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(onePageBody)) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.WithInternalToken(internalSecret).Friendships(ctx, "", 10); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

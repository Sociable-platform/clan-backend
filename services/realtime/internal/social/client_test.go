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
	gameID   = "a6c1e2f0-1111-4222-8333-944455556666"
	playerID = "7f1e0c2a-5b3d-4e8f-9a10-2b3c4d5e6f70"
	secret   = "eyJ.SECRET.token"
)

func fakeSocial(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPlayer(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    Player
		wantErr bool
	}{
		{
			name: "ok",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"` + strings.ToUpper(playerID) + `","gameId":"` + gameID + `","externalId":"u_42","displayName":"Ali","createdAt":"2026-11-09T10:00:00Z"}`))
			},
			want: Player{ID: playerID, DisplayName: "Ali"}, // the ID is normalised to lowercase
		},
		{
			name: "game in the answer differs",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"` + playerID + `","gameId":"00000000-0000-0000-0000-000000000002","displayName":"Ali"}`))
			},
			wantErr: true,
		},
		{
			name:    "social answers 401",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			wantErr: true,
		},
		{
			name:    "social answers 500",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			wantErr: true,
		},
		{
			name:    "answer is not JSON",
			handler: func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) },
			wantErr: true,
		},
		{
			name: "answer has no id",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"gameId":"` + gameID + `"}`))
			},
			wantErr: true,
		},
		{
			name: "redirect is not followed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://elsewhere.invalid/steal", http.StatusFound)
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fakeSocial(t, tt.handler).Player(context.Background(), secret, gameID)
			if tt.wantErr {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("error = %v, want ErrUnavailable", err)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("the error contains the token: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Player() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestPlayerSendsTheTokenAsBearer(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	c := fakeSocial(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		w.Write([]byte(`{"id":"` + playerID + `","gameId":"` + gameID + `","displayName":"Ali"}`))
	})
	if _, err := c.Player(context.Background(), secret, gameID); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+secret || gotPath != "/api/v1/players/me" || gotMethod != http.MethodGet {
		t.Fatalf("social got %s %s with Authorization %q", gotMethod, gotPath, gotAuth)
	}
}

func TestPlayerTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.Player(context.Background(), secret, gameID)
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("error = %v after %v, want ErrUnavailable within the timeout", err, time.Since(start))
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error contains the token: %v", err)
	}
}

func TestPlayerSocialDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	c, err := NewClient(url, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Player(context.Background(), secret, gameID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestNewClientRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "social:8082", "ftp://social", "http://", "://x"} {
		if _, err := NewClient(u, time.Second); err == nil {
			t.Errorf("NewClient(%q) succeeded, want an error", u)
		}
	}
	if _, err := NewClient("http://social:8082/", time.Second); err != nil {
		t.Errorf("NewClient with a trailing slash: %v", err)
	}
}

package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// These methods make fakeStore keep the allowed origins of a game, like the games table.

func (f *fakeStore) GameByID(_ context.Context, gameID string) (repository.Game, error) {
	if f.err != nil {
		return repository.Game{}, f.err
	}
	for _, g := range f.games {
		if g.ID == gameID {
			return g, nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

func (f *fakeStore) SetAllowedOrigins(_ context.Context, gameID, developerID string, origins []string) (repository.Game, error) {
	if f.err != nil {
		return repository.Game{}, f.err
	}
	for i, g := range f.games {
		if g.ID == gameID && f.team(gameID)[developerID] == repository.RoleOwner {
			f.games[i].AllowedOrigins = origins
			return f.games[i], nil
		}
	}
	return repository.Game{}, repository.ErrNotFound
}

func TestNormalizeOrigin(t *testing.T) {
	valid := []struct{ in, want string }{
		{"https://play.example.com", "https://play.example.com"},
		{"http://localhost:5174", "http://localhost:5174"},
		{"  https://play.example.com  ", "https://play.example.com"},
		{"HTTPS://Play.Example.COM", "https://play.example.com"},
		{"https://play.example.com:8443", "https://play.example.com:8443"},
		{"http://127.0.0.1:3000", "http://127.0.0.1:3000"},
	}
	for _, tt := range valid {
		t.Run("valid "+tt.in, func(t *testing.T) {
			got, err := normalizeOrigin(tt.in)
			if err != nil || got != tt.want {
				t.Errorf("= (%q, %v), want %q", got, err, tt.want)
			}
		})
	}

	invalid := []struct{ name, in string }{
		{"empty", ""},
		{"blank", "   "},
		{"wildcard", "*"},
		{"wildcard subdomain", "https://*.example.com"},
		{"no scheme", "play.example.com"},
		{"other scheme", "ftp://play.example.com"},
		{"websocket scheme", "wss://play.example.com"},
		{"no host", "https://"},
		{"no host, only a port", "https://:8080"},
		{"colon without a port", "https://play.example.com:"},
		{"port is not a number", "https://play.example.com:abc"},
		{"trailing slash", "https://play.example.com/"},
		{"path", "https://play.example.com/game"},
		{"query", "https://play.example.com?a=1"},
		{"empty query", "https://play.example.com?"},
		{"fragment", "https://play.example.com#top"},
		{"user name", "https://user@play.example.com"},
		{"not a url", "https://play example.com"},
		{"too long", "https://" + strings.Repeat("a", 250) + ".com"},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			got, err := normalizeOrigin(tt.in)
			wantError(t, err, http.StatusBadRequest, "invalid_origins")
			if got != "" {
				t.Errorf("origin = %q, want nothing", got)
			}
		})
	}
}

func TestSetAllowedOrigins_StoresTheCleanList(t *testing.T) {
	svc, alice, _, game := aliceGame(t)

	g, err := svc.SetAllowedOrigins(context.Background(), alice.ID, game.ID,
		[]string{"https://Play.Example.com", "http://localhost:5174", "https://play.example.com", " http://localhost:5174 "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://play.example.com", "http://localhost:5174"} // lower-cased, no repeats, first one wins the place
	if !reflect.DeepEqual(g.AllowedOrigins, want) {
		t.Errorf("origins = %v, want %v", g.AllowedOrigins, want)
	}
	if stored := svc.store.(*fakeStore).games[0].AllowedOrigins; !reflect.DeepEqual(stored, want) {
		t.Errorf("stored = %v, want %v", stored, want)
	}
}

func TestSetAllowedOrigins_AnEmptyListClearsThem(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()
	if _, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, []string{"https://play.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, in := range [][]string{{}, nil} {
		g, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if g.AllowedOrigins == nil || len(g.AllowedOrigins) != 0 {
			t.Errorf("origins = %#v, want an empty, non-nil list", g.AllowedOrigins)
		}
	}
}

func TestSetAllowedOrigins_Limits(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()

	var many []string
	for i := range maxAllowedOrigins {
		many = append(many, "https://site"+strings.Repeat("x", i)+".example.com")
	}
	if _, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, many); err != nil {
		t.Fatalf("exactly %d origins must be allowed: %v", maxAllowedOrigins, err)
	}

	_, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, append(many, "https://one-more.example.com"))
	wantError(t, err, http.StatusBadRequest, "invalid_origins")
}

func TestSetAllowedOrigins_OneBadOriginStoresNothing(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	ctx := context.Background()
	if _, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, []string{"https://keep.example.com"}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, []string{"https://new.example.com", "*"})
	wantError(t, err, http.StatusBadRequest, "invalid_origins")

	if stored := svc.store.(*fakeStore).games[0].AllowedOrigins; !reflect.DeepEqual(stored, []string{"https://keep.example.com"}) {
		t.Errorf("stored = %v, want the old list untouched", stored)
	}
}

func TestSetAllowedOrigins_OnlyTheOwnerCanSetThem(t *testing.T) {
	svc, _, bob, game := aliceGame(t)
	ctx := context.Background()

	_, err := svc.SetAllowedOrigins(ctx, bob.ID, game.ID, []string{"https://evil.example.com"})
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.SetAllowedOrigins(ctx, bob.ID, "not-a-uuid", nil)
	wantError(t, err, http.StatusNotFound, "game_not_found")

	if stored := svc.store.(*fakeStore).games[0].AllowedOrigins; len(stored) != 0 {
		t.Errorf("a stranger changed the origins: %v", stored)
	}
}

func TestSetAllowedOrigins_StoreFailureIsNotAClientError(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	errDB := errors.New("connection refused")
	svc.store.(*fakeStore).err = errDB

	_, err := svc.SetAllowedOrigins(context.Background(), alice.ID, game.ID, []string{"https://play.example.com"})
	if !errors.Is(err, errDB) {
		t.Fatalf("err = %v, want the database error", err)
	}
}

func TestInternalGameConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("never published: version 0, no config, but the origins", func(t *testing.T) {
		svc, alice, _, game := aliceGame(t)
		if _, err := svc.SetAllowedOrigins(ctx, alice.ID, game.ID, []string{"https://play.example.com"}); err != nil {
			t.Fatal(err)
		}
		c, err := svc.InternalGameConfig(ctx, game.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.GameID != game.ID || c.Version != 0 || c.Config != nil || !reflect.DeepEqual(c.AllowedOrigins, []string{"https://play.example.com"}) {
			t.Errorf("config = %+v", c)
		}
	})

	t.Run("published: the newest version and its config", func(t *testing.T) {
		svc, alice, _, game := aliceGame(t)
		saveDraft(t, svc, alice.ID, game.ID, cfg("One"))
		if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
			t.Fatal(err)
		}
		saveDraft(t, svc, alice.ID, game.ID, cfg("Two"))
		if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
			t.Fatal(err)
		}
		saveDraft(t, svc, alice.ID, game.ID, cfg("Draft")) // a draft is never returned

		c, err := svc.InternalGameConfig(ctx, game.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != 2 || string(c.Config) != cfg("Two") {
			t.Errorf("version %d, config %s; want version 2 with the second config", c.Version, c.Config)
		}
		if c.AllowedOrigins == nil || len(c.AllowedOrigins) != 0 {
			t.Errorf("origins = %#v, want an empty, non-nil list", c.AllowedOrigins)
		}
	})

	t.Run("unknown and malformed IDs are game_not_found", func(t *testing.T) {
		svc, _, _, _ := aliceGame(t)
		for _, id := range []string{"00000000-0000-4000-8000-0000000000ff", "nope", "", "' or 1=1 --"} {
			_, err := svc.InternalGameConfig(ctx, id)
			wantError(t, err, http.StatusNotFound, "game_not_found")
		}
	})

	t.Run("store failure is an error, not a 404", func(t *testing.T) {
		svc, _, _, game := aliceGame(t)
		errDB := errors.New("connection refused")
		svc.store.(*fakeStore).err = errDB
		if _, err := svc.InternalGameConfig(ctx, game.ID); !errors.Is(err, errDB) {
			t.Fatalf("err = %v, want the database error", err)
		}
	})
}

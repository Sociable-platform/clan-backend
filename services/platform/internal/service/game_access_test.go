package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// GameAccessOf derives the statuses from the fake's games and its two suspended sets.
func (f *fakeStore) GameAccessOf(_ context.Context, gameID string) (repository.GameAccess, error) {
	if f.accessErr != nil {
		return repository.GameAccess{}, f.accessErr
	}
	if f.err != nil {
		return repository.GameAccess{}, f.err
	}
	for _, g := range f.games {
		if g.ID == gameID {
			a := repository.GameAccess{GameStatus: "active", DeveloperStatus: "active"}
			if f.suspendedGames[g.ID] {
				a.GameStatus = "suspended"
			}
			// Like the SQL: the owners' switch is on while any owner is active, and off when every owner is suspended.
			owners := f.ownersOf(g.ID)
			a.DeveloperStatus = "active"
			if len(owners) > 0 {
				a.DeveloperStatus = "suspended"
				for _, id := range owners {
					if !f.suspendedDevs[id] {
						a.DeveloperStatus = "active"
					}
				}
			}
			return a, nil
		}
	}
	return repository.GameAccess{}, repository.ErrNotFound
}

// suspend switches a game or a developer off in the fake store.
func (f *fakeStore) suspendGame(id string) {
	if f.suspendedGames == nil {
		f.suspendedGames = map[string]bool{}
	}
	f.suspendedGames[id] = true
}

func (f *fakeStore) suspendDeveloper(id string) {
	if f.suspendedDevs == nil {
		f.suspendedDevs = map[string]bool{}
	}
	f.suspendedDevs[id] = true
}

func TestEnsureGameServed(t *testing.T) {
	svc, store, _ := newTestService(t)
	dev, err := svc.Register(context.Background(), validRegister())
	if err != nil {
		t.Fatal(err)
	}
	game, err := svc.RegisterGame(context.Background(), dev.ID, RegisterGameInput{Name: "Farm"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := svc.EnsureGameServed(ctx, game.ID); err != nil {
		t.Fatalf("active game and developer: %v", err)
	}

	store.suspendGame(game.ID)
	wantError(t, svc.EnsureGameServed(ctx, game.ID), http.StatusForbidden, "game_suspended")

	// With both switches off the developer's wins: it is the one that turns off the whole account.
	store.suspendDeveloper(dev.ID)
	wantError(t, svc.EnsureGameServed(ctx, game.ID), http.StatusForbidden, "account_suspended")

	// Resume the game only: the developer is still suspended.
	delete(store.suspendedGames, game.ID)
	wantError(t, svc.EnsureGameServed(ctx, game.ID), http.StatusForbidden, "account_suspended")

	// Resume the developer: served again at once.
	delete(store.suspendedDevs, dev.ID)
	if err := svc.EnsureGameServed(ctx, game.ID); err != nil {
		t.Fatalf("after resume: %v", err)
	}
}

func TestEnsureGameServed_OtherGamesOfASuspendedGameKeepWorking(t *testing.T) {
	svc, store, _ := newTestService(t)
	dev, _ := svc.Register(context.Background(), validRegister())
	a, _ := svc.RegisterGame(context.Background(), dev.ID, RegisterGameInput{Name: "A"})
	b, _ := svc.RegisterGame(context.Background(), dev.ID, RegisterGameInput{Name: "B"})
	other, _ := svc.Register(context.Background(), RegisterInput{Email: "other@example.com", Password: "correct horse", Name: "Other"})
	c, _ := svc.RegisterGame(context.Background(), other.ID, RegisterGameInput{Name: "C"})

	store.suspendGame(a.ID)
	if err := svc.EnsureGameServed(context.Background(), b.ID); err != nil {
		t.Errorf("a sibling game: %v", err)
	}
	store.suspendDeveloper(dev.ID)
	wantError(t, svc.EnsureGameServed(context.Background(), b.ID), http.StatusForbidden, "account_suspended") // one switch, every game
	if err := svc.EnsureGameServed(context.Background(), c.ID); err != nil {
		t.Errorf("another developer's game: %v", err)
	}
}

func TestEnsureGameServed_UnknownGameIsLeftToTheCaller(t *testing.T) {
	svc, _, _ := newTestService(t)
	if err := svc.EnsureGameServed(context.Background(), devID1); err != nil {
		t.Fatalf("unknown game: %v", err)
	}
}

func TestEnsureGameServed_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	var e *httpx.Error
	if err := svc.EnsureGameServed(context.Background(), devID1); err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

// A published game answers 403 game_suspended while the game or its developer is suspended, with the
// same code for both so players never see a billing message, and serves again once resumed.
func TestPublicWidgetConfig_SuspendedGameIsRefused(t *testing.T) {
	svc, alice, _, game := aliceGame(t)
	store := svc.store.(*fakeStore)
	ctx := context.Background()
	if _, err := svc.SaveWidgetDraft(ctx, alice.ID, game.ID, json.RawMessage(cfg("A1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishWidget(ctx, alice.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublicWidgetConfig(ctx, game.ID); err != nil {
		t.Fatalf("before suspension: %v", err)
	}

	store.suspendGame(game.ID)
	_, err := svc.PublicWidgetConfig(ctx, game.ID)
	wantError(t, err, http.StatusForbidden, "game_suspended")
	delete(store.suspendedGames, game.ID)

	store.suspendDeveloper(alice.ID)
	_, err = svc.PublicWidgetConfig(ctx, game.ID)
	wantError(t, err, http.StatusForbidden, "game_suspended") // not account_suspended: players must not learn why
	delete(store.suspendedDevs, alice.ID)

	got, err := svc.PublicWidgetConfig(ctx, game.ID)
	if err != nil || got.Version != 1 {
		t.Fatalf("after resume: %+v, %v", got, err)
	}
}

// A suspended game that was never published is also 403, not 404, and an unknown game stays 404.
func TestPublicWidgetConfig_SuspendedWinsOverNotPublished(t *testing.T) {
	svc, _, _, game := aliceGame(t)
	store := svc.store.(*fakeStore)

	store.suspendGame(game.ID)
	_, err := svc.PublicWidgetConfig(context.Background(), game.ID)
	wantError(t, err, http.StatusForbidden, "game_suspended")

	_, err = svc.PublicWidgetConfig(context.Background(), "00000000-0000-4000-8000-00000000dead")
	wantError(t, err, http.StatusNotFound, "widget_not_published")
}

func TestPublicWidgetConfig_StoreFailureOnTheAccessCheckIsNotAnAPIError(t *testing.T) {
	svc, _, _, game := aliceGame(t)
	svc.store.(*fakeStore).err = errors.New("connection refused")
	var e *httpx.Error
	if _, err := svc.PublicWidgetConfig(context.Background(), game.ID); err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

func TestEnsureDeveloperActive(t *testing.T) {
	svc, store, _ := newTestService(t)
	ctx := context.Background()
	dev, err := svc.Register(ctx, validRegister())
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureDeveloperActive(ctx, dev.ID); err != nil {
		t.Fatalf("active developer: %v", err)
	}
	store.suspendDeveloper(dev.ID)
	err = svc.EnsureDeveloperActive(ctx, dev.ID)
	wantError(t, err, http.StatusForbidden, "account_suspended")
	delete(store.suspendedDevs, dev.ID)
	if err := svc.EnsureDeveloperActive(ctx, dev.ID); err != nil {
		t.Fatalf("after resume: %v", err)
	}

	wantError(t, svc.EnsureDeveloperActive(ctx, "dev-gone"), http.StatusUnauthorized, "unauthorized")
}

func TestEnsureDeveloperActive_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	var e *httpx.Error
	if err := svc.EnsureDeveloperActive(context.Background(), "d1"); err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

// A game suspended on its own does not stop the developer from writing: only the account switch does.
func TestEnsureDeveloperActive_IgnoresAGameSuspension(t *testing.T) {
	svc, store, _ := newTestService(t)
	dev, _ := svc.Register(context.Background(), validRegister())
	game, _ := svc.RegisterGame(context.Background(), dev.ID, RegisterGameInput{Name: "Farm"})
	store.suspendGame(game.ID)
	if err := svc.EnsureDeveloperActive(context.Background(), dev.ID); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestMeAndRegisterReportTheStatus(t *testing.T) {
	svc, store, _ := newTestService(t)
	ctx := context.Background()
	dev, err := svc.Register(ctx, validRegister())
	if err != nil {
		t.Fatal(err)
	}
	if dev.Status != "active" || dev.StatusReason != "" {
		t.Fatalf("a new developer: %+v", dev)
	}
	me, err := svc.Me(ctx, dev.ID)
	if err != nil || me.Status != "active" || me.StatusReason != "" {
		t.Fatalf("me: %+v, %v", me, err)
	}

	store.suspendDeveloper(dev.ID)
	me, err = svc.Me(ctx, dev.ID)
	if err != nil || me.Status != "suspended" || me.StatusReason != "non_payment" {
		t.Fatalf("me while suspended: %+v, %v", me, err)
	}
	// A suspended developer can still log in.
	if _, err := svc.Login(ctx, LoginInput{Email: "dev@example.com", Password: "correct horse"}); err != nil {
		t.Fatalf("login while suspended: %v", err)
	}
}

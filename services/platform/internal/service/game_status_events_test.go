package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// IDs for the tests below: a developer, and games of theirs.
const (
	statusDev   = devID1
	statusGame1 = devID2
	statusGame2 = "6f1d2c3b-4a59-4e68-8b7a-9c0d1e2f3a4b"
	statusGame3 = "7a2e3d4c-5b6a-4f79-9c8b-0d1e2f3a4b5c"
)

// statusEventsService is a service, and the events its transactions committed to the outbox. Its
// store knows one developer with the games game1 and game2, both served.
func statusEventsService(t *testing.T) (*Service, *fakeStore, *events.Recorder) {
	t.Helper()
	svc, store, _ := newTestService(t)
	rec := &store.committed
	store.games = []repository.Game{
		{ID: statusGame1, DeveloperID: statusDev},
		{ID: statusGame2, DeveloperID: statusDev},
	}
	return svc, store, rec
}

// wantGameEvents fails unless rec holds exactly these events, in this order: each a type and a game.
func wantGameEvents(t *testing.T, rec *events.Recorder, want ...[2]string) {
	t.Helper()
	var got [][2]string
	for _, e := range rec.Events() {
		got = append(got, [2]string{e.Type, e.GameID})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

func TestSetGameStatus_PublishesTheChangeOnce(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	ctx := context.Background()

	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusSuspended}
	if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusSuspended, Reason: ReasonAbuse}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, statusGame1})
	if d := rec.Events()[0].Data; !reflect.DeepEqual(d, map[string]any{"status": "suspended"}) {
		t.Errorf("data = %v, want the new status", d)
	}

	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusActive}
	if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, statusGame1}, [2]string{EventGameResumed, statusGame1})
}

func TestSetGameStatus_PublishesNothingWhenNothingChanged(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	store.statusUnchanged = true
	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusSuspended}

	if _, err := svc.SetGameStatus(context.Background(), "admin-1", statusGame1, SetStatusInput{Status: StatusSuspended, Reason: ReasonAbuse}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec)
}

func TestSetGameStatus_PublishesNothingWhileTheDeveloperIsSuspended(t *testing.T) {
	// The game is not served before or after, so the other services have nothing to learn.
	for _, status := range []string{StatusSuspended, StatusActive} {
		t.Run(status, func(t *testing.T) {
			svc, store, rec := statusEventsService(t)
			store.suspendDeveloper(statusDev)
			store.statusGame = repository.AdminGame{ID: statusGame1, Status: status}

			in := SetStatusInput{Status: status, Reason: ReasonOther}
			if _, err := svc.SetGameStatus(context.Background(), "admin-1", statusGame1, in); err != nil {
				t.Fatal(err)
			}
			wantGameEvents(t, rec)
		})
	}
}

// The access check runs in the change's transaction. When it fails, the whole change fails (and
// rolls back), so the admin can try again; no event is guessed.
func TestSetGameStatus_WhenTheAccessCheckFailsTheChangeFails(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	store.accessErr = errors.New("connection refused")
	ctx := context.Background()

	for _, in := range []SetStatusInput{{Status: StatusActive}, {Status: StatusSuspended, Reason: ReasonAbuse}} {
		store.statusGame = repository.AdminGame{ID: statusGame1, Status: in.Status}
		if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, in); !errors.Is(err, store.accessErr) {
			t.Errorf("%s: err = %v, want the access check's error", in.Status, err)
		}
	}
	wantGameEvents(t, rec)
}

func TestSetGameStatus_ADeadStoreOrBadInputPublishesNothing(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	ctx := context.Background()

	// refused before the store: bad input, unknown game
	_, _ = svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusSuspended})
	_, _ = svc.SetGameStatus(ctx, "admin-1", "nope", SetStatusInput{Status: StatusActive})
	// the store fails
	store.err = errors.New("connection refused")
	_, _ = svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusActive})

	wantGameEvents(t, rec)
}

func TestSetDeveloperStatus_PublishesOneEventPerServedGame(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	ctx := context.Background()
	// game1 and game2 are on; game3 was suspended on its own earlier.
	store.adminGames = []repository.AdminGame{
		{ID: statusGame1, Status: StatusActive},
		{ID: statusGame2, Status: StatusActive},
		{ID: statusGame3, Status: StatusSuspended},
	}

	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusSuspended}
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, SetStatusInput{Status: StatusSuspended, Reason: ReasonNonPayment}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec, [2]string{EventGameSuspended, statusGame1}, [2]string{EventGameSuspended, statusGame2})

	// Resuming must not announce game3: it is still suspended, and a game.resumed would let it through.
	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusActive}
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, SetStatusInput{Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec,
		[2]string{EventGameSuspended, statusGame1}, [2]string{EventGameSuspended, statusGame2},
		[2]string{EventGameResumed, statusGame1}, [2]string{EventGameResumed, statusGame2})
}

func TestSetDeveloperStatus_PublishesNothingWhenNothingChangedOrThereAreNoGames(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	ctx := context.Background()
	in := SetStatusInput{Status: StatusSuspended, Reason: ReasonOther}
	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusSuspended}

	store.adminGames = []repository.AdminGame{{ID: statusGame1, Status: StatusActive}}
	store.statusUnchanged = true
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, in); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec)

	store.statusUnchanged = false
	store.adminGames = nil
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, in); err != nil {
		t.Fatal(err)
	}
	wantGameEvents(t, rec)
}

// The events are part of the change: when they cannot be written to the outbox, the status change
// fails and rolls back with them (the broker is never asked during a request).
func TestSetStatus_FailsWhenTheEventCannotBeWritten(t *testing.T) {
	svc, store, rec := statusEventsService(t)
	store.addErr = errors.New("outbox insert failed")
	ctx := context.Background()

	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusSuspended}
	if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusSuspended, Reason: ReasonAbuse}); !errors.Is(err, store.addErr) {
		t.Errorf("game: err = %v, want the outbox error", err)
	}
	store.statusDev = repository.AdminDeveloper{ID: statusDev, Status: StatusSuspended}
	store.adminGames = []repository.AdminGame{{ID: statusGame1, Status: StatusActive}}
	if _, err := svc.SetDeveloperStatus(ctx, "admin-1", statusDev, SetStatusInput{Status: StatusSuspended, Reason: ReasonAbuse}); !errors.Is(err, store.addErr) {
		t.Errorf("developer: err = %v, want the outbox error", err)
	}
	wantGameEvents(t, rec)
}

// game.suspended and game.resumed must have exactly the fields of the same messages in
// docs/event-messages.md. Realtime reads that file in its own tests, so a change on either side
// fails a test there.
func TestGameStatusEventsHaveTheFieldsOfTheEventMessagesFile(t *testing.T) {
	doc, err := os.ReadFile("../../../../docs/event-messages.md")
	if err != nil {
		t.Fatal(err)
	}
	// On Windows git may check the file out with CRLF line endings.
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	fileData := map[string]map[string]any{}
	for _, b := range regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllSubmatch(doc, -1) {
		var msg struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(b[1], &msg) == nil {
			fileData[msg.Type] = msg.Data
		}
	}

	svc, store, rec := statusEventsService(t)
	ctx := context.Background()
	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusSuspended}
	if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusSuspended, Reason: ReasonAbuse}); err != nil {
		t.Fatal(err)
	}
	store.statusGame = repository.AdminGame{ID: statusGame1, Status: StatusActive}
	if _, err := svc.SetGameStatus(ctx, "admin-1", statusGame1, SetStatusInput{Status: StatusActive}); err != nil {
		t.Fatal(err)
	}

	for _, e := range rec.Events() {
		want, ok := fileData[e.Type]
		if !ok {
			t.Fatalf("%s is not in docs/event-messages.md", e.Type)
		}
		// send it through JSON, as the publisher does
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			GameID string         `json:"game_id"`
			Data   map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.GameID != statusGame1 {
			t.Errorf("%s: game_id = %q, want %q", e.Type, got.GameID, statusGame1)
		}
		if len(got.Data) != len(want) {
			t.Fatalf("%s: data = %v, file has %v", e.Type, got.Data, want)
		}
		for k, v := range want {
			g, ok := got.Data[k]
			if !ok || reflect.TypeOf(g) != reflect.TypeOf(v) {
				t.Errorf("%s field %q: published %v (%T), file has %v (%T)", e.Type, k, g, g, v, v)
			}
		}
	}
}

func TestInternalGameConfig_StatusIsTheEffectiveOne(t *testing.T) {
	tests := []struct {
		name        string
		suspendGame bool
		suspendDev  bool
		wantStatus  string
	}{
		{"both on", false, false, StatusActive},
		{"the game is suspended", true, false, StatusSuspended},
		{"the developer is suspended", false, true, StatusSuspended},
		{"both are suspended", true, true, StatusSuspended},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, alice, _, game := aliceGame(t)
			store := svc.store.(*fakeStore)
			if tt.suspendGame {
				store.suspendGame(game.ID)
			}
			if tt.suspendDev {
				store.suspendDeveloper(alice.ID)
			}
			c, err := svc.InternalGameConfig(context.Background(), game.ID)
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", c.Status, tt.wantStatus)
			}
		})
	}

	t.Run("a failing access check is an error, not a guess", func(t *testing.T) {
		svc, _, _, game := aliceGame(t)
		svc.store.(*fakeStore).accessErr = errors.New("connection refused")
		if _, err := svc.InternalGameConfig(context.Background(), game.ID); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("an unknown game is still game_not_found", func(t *testing.T) {
		svc, _, _, _ := aliceGame(t)
		_, err := svc.InternalGameConfig(context.Background(), "00000000-0000-4000-8000-0000000000ff")
		wantError(t, err, http.StatusNotFound, "game_not_found")
	})
}

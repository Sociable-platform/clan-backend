package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
)

// gameCalls records, in order, what the dispatcher did to the status and to the connections.
type gameCalls struct{ log []string }

type fakeGameStatus struct {
	calls     *gameCalls
	resumeErr error
}

func (f *fakeGameStatus) SetSuspended(gameID string, suspended bool) {
	f.calls.log = append(f.calls.log, "set "+gameID+" "+map[bool]string{true: "suspended", false: "active"}[suspended])
}

func (f *fakeGameStatus) Resumed(_ context.Context, gameID string) error {
	f.calls.log = append(f.calls.log, "resumed "+gameID)
	return f.resumeErr
}

type fakeCloser struct {
	calls *gameCalls
	n     int
}

func (f *fakeCloser) CloseGameSuspended(gameID string) int {
	f.calls.log = append(f.calls.log, "close "+gameID)
	return f.n
}

func gameEvent(typ string, data map[string]any) []byte {
	e := map[string]any{"event_id": "e1", "type": typ, "occurred_at": "2026-11-13T10:15:02.118Z", "game_id": gameUUID}
	if data != nil {
		e["data"] = data
	}
	b, _ := json.Marshal(e)
	return b
}

func gameDispatcher(status *fakeGameStatus, closer *fakeCloser) *Dispatcher {
	return NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithGameStatus(status, closer)
}

func newGameFakes() (*gameCalls, *fakeGameStatus, *fakeCloser) {
	calls := &gameCalls{}
	return calls, &fakeGameStatus{calls: calls}, &fakeCloser{calls: calls, n: 3}
}

// The status is recorded before the connections are closed, so a connection that is being opened
// at that moment sees it.
func TestGameSuspendedRecordsTheStatusThenClosesTheGame(t *testing.T) {
	calls, status, closer := newGameFakes()
	if err := gameDispatcher(status, closer).Handle(context.Background(), gameEvent("game.suspended", map[string]any{"status": "suspended"})); err != nil {
		t.Fatal(err)
	}
	want := []string{"set " + gameUUID + " suspended", "close " + gameUUID}
	if !reflect.DeepEqual(calls.log, want) {
		t.Fatalf("calls = %v, want %v", calls.log, want)
	}
}

func TestGameResumedAsksPlatformAndClosesNothing(t *testing.T) {
	calls, status, closer := newGameFakes()
	if err := gameDispatcher(status, closer).Handle(context.Background(), gameEvent("game.resumed", map[string]any{"status": "active"})); err != nil {
		t.Fatal(err)
	}
	if want := []string{"resumed " + gameUUID}; !reflect.DeepEqual(calls.log, want) {
		t.Fatalf("calls = %v, want %v", calls.log, want)
	}
}

// The type of the event is the message; its data only repeats it.
func TestGameEventsNeedNoData(t *testing.T) {
	for _, typ := range []string{"game.suspended", "game.resumed"} {
		calls, status, closer := newGameFakes()
		if err := gameDispatcher(status, closer).Handle(context.Background(), gameEvent(typ, nil)); err != nil || len(calls.log) == 0 {
			t.Errorf("%s: error %v, calls %v; want it handled", typ, err, calls.log)
		}
	}
}

func TestGameResumedIsTriedAgainWhenPlatformCannotBeAsked(t *testing.T) {
	for _, cause := range []error{gameconfig.ErrUnavailable, errors.New("boom")} {
		_, status, closer := newGameFakes()
		status.resumeErr = cause
		err := gameDispatcher(status, closer).Handle(context.Background(), gameEvent("game.resumed", nil))
		if err == nil || errors.Is(err, ErrDrop) {
			t.Errorf("cause %v: error = %v, want a retryable error", cause, err)
		}
	}
}

func TestGameEventsThatCannotBeUsedAreDropped(t *testing.T) {
	t.Run("a game ID that is not a game", func(t *testing.T) {
		_, status, closer := newGameFakes()
		status.resumeErr = gameconfig.ErrInvalidGame
		if err := gameDispatcher(status, closer).Handle(context.Background(), gameEvent("game.resumed", nil)); !errors.Is(err, ErrDrop) {
			t.Fatalf("error = %v, want ErrDrop", err)
		}
	})
	for _, typ := range []string{"game.suspended", "game.resumed"} {
		t.Run("no game_id in "+typ, func(t *testing.T) {
			calls, status, closer := newGameFakes()
			body := []byte(`{"event_id":"e1","type":"` + typ + `","occurred_at":"2026-11-13T10:15:02.118Z"}`)
			if err := gameDispatcher(status, closer).Handle(context.Background(), body); !errors.Is(err, ErrDrop) {
				t.Fatalf("error = %v, want ErrDrop", err)
			}
			if len(calls.log) != 0 {
				t.Fatalf("calls = %v, want none: there is no game to act on", calls.log)
			}
		})
	}
}

// Without the status and the hub wired in (as in the older tests) the events are acknowledged and ignored.
func TestGameEventsAreIgnoredWithoutAGameStatus(t *testing.T) {
	for _, typ := range []string{"game.suspended", "game.resumed"} {
		if err := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).Handle(context.Background(), gameEvent(typ, nil)); err != nil {
			t.Errorf("%s: error = %v, want the event acknowledged", typ, err)
		}
	}
}

// A game that is suspended cannot give its config, and its connections are closed anyway: there is
// nobody to tell and nothing a retry would fix.
func TestConfigPublishedOfASuspendedGameIsAcknowledged(t *testing.T) {
	to := &fakeBroadcaster{}
	cache := &fakeConfig{err: gameconfig.ErrSuspended}
	d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.Default()).WithConfig(cache, to)
	if err := d.Handle(context.Background(), configPublished(5)); err != nil {
		t.Fatalf("error = %v, want the event acknowledged", err)
	}
	if len(to.got) != 0 {
		t.Fatalf("told %v about a game that is suspended", to.got)
	}
}

package consumer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// onePush is a frame the fake service returns for every event, so a test can count frames.
var onePush = []service.Push{{GameID: gameUUID, PlayerIDs: []string{"p-1"}, Type: "notification", Data: map[string]string{"k": "v"}}}

// Delivery is at least once (an outbox relay can send an event twice): a copy must store and send nothing.
func TestDispatchHandlesACopyOfAnEventOnce(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"friend.accepted", `{"event_id":"e1","type":"friend.accepted","game_id":"g-1","data":{"request_id":"r-1","players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}]}}`},
		{"clan.member_joined", `{"event_id":"e2","type":"clan.member_joined","game_id":"g-1","data":{"clan":{"id":"c-1","name":"Wolves"},"player":{"id":"u-1","name":"Emma"}}}`},
		{"clan.member_left", `{"event_id":"e3","type":"clan.member_left","game_id":"g-1","data":{"clan":{"id":"c-1","name":"Wolves"},"player":{"id":"u-1","name":"Emma"},"reason":"kicked"}}`},
		{"friend.request_sent", `{"event_id":"e4","type":"friend.request_sent","game_id":"g-1","data":{"request_id":"r-1","sender":{"id":"u-1","name":"Emma"},"receiver":{"id":"u-2","name":"Oliver"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, send := &fakeEvents{pushes: onePush}, &fakeSender{}
			var logs bytes.Buffer
			d := NewDispatcher(ev, send, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

			for i := range 2 {
				if err := d.Handle(context.Background(), []byte(tt.body)); err != nil {
					t.Fatalf("delivery %d: %v", i+1, err) // a copy is acknowledged, not an error
				}
			}
			if len(ev.calls) != 1 {
				t.Errorf("the service was called %d times (%v), want once", len(ev.calls), ev.calls)
			}
			if len(send.got) != 1 {
				t.Errorf("%d frames sent, want the frame of the first delivery only", len(send.got))
			}
			if !strings.Contains(logs.String(), "skipping an event handled before") {
				t.Errorf("no debug line for the skipped copy; logs:\n%s", logs.String())
			}
		})
	}
}

func TestDispatchHandlesAnEventAgainAfterAFailure(t *testing.T) {
	body := []byte(`{"event_id":"e1","type":"friend.accepted","game_id":"g-1","data":{"players":[{"id":"u-1","name":"Emma"},{"id":"u-2","name":"Oliver"}]}}`)
	ev, send := &fakeEvents{pushes: onePush, err: errors.New("database down")}, &fakeSender{}
	d := NewDispatcher(ev, send, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := d.Handle(context.Background(), body); err == nil || errors.Is(err, ErrDrop) {
		t.Fatalf("first delivery = %v, want an error to retry", err)
	}
	if len(send.got) != 0 {
		t.Fatal("frames were sent for an event whose change failed")
	}

	ev.err = nil
	if err := d.Handle(context.Background(), body); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if len(ev.calls) != 2 || len(send.got) != 1 {
		t.Errorf("calls %v, frames %d; want the retry handled (a failed change does not mark the event handled)", ev.calls, len(send.got))
	}
}

func TestDispatchDropsAnEventWhoseIDsAreNotUUIDs(t *testing.T) {
	ev := &fakeEvents{onceErr: fmt.Errorf("%w: event_id is not a UUID", service.ErrInvalidEvent)}
	d := NewDispatcher(ev, &fakeSender{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := []byte(`{"event_id":"nope","type":"player.renamed","game_id":"g-1","data":{"player":{"id":"u-1","name":"Emma"}}}`)
	if err := d.Handle(context.Background(), body); !errors.Is(err, ErrDrop) {
		t.Fatalf("err = %v, want ErrDrop", err)
	}
}

func TestDispatchRecordsOnlyEventsThatStoreOrSend(t *testing.T) {
	cache, to := &fakeConfig{current: 5, send: true}, &fakeBroadcaster{}
	ev := &fakeEvents{}
	d := NewDispatcher(ev, &fakeSender{}, slog.New(slog.NewTextHandler(io.Discard, nil))).WithConfig(cache, to)

	bodies := [][]byte{
		configPublished(5), configPublished(5), // repeating it changes nothing, and it asks Platform
		[]byte(`{"event_id":"e7","type":"clan.renamed","game_id":"g-1","data":{"clan":{"id":"c-1","name":"New"}}}`),
		[]byte(`{"event_id":"e8","type":"something.new","game_id":"g-1","data":{"k":"v"}}`),
	}
	for _, b := range bodies {
		if err := d.Handle(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if ev.onceRuns != 0 || len(ev.handled) != 0 {
		t.Errorf("HandleOnce ran %d times, recorded %v; want config.published and ignored types left out", ev.onceRuns, ev.handled)
	}
	if len(cache.calls) != 2 {
		t.Errorf("config.published reached the cache %d times, want both deliveries", len(cache.calls))
	}
}

// Every type the dispatcher sends through HandleOnce must be one it stores.
func TestEveryHandledOnceTypeIsStored(t *testing.T) {
	for _, typ := range []string{
		typeFriendRequestSent, typeFriendRequestCancelled, typeFriendRequestDeclined, typeFriendAccepted,
		typeFriendRemoved, typePlayerRenamed, typePlayerBlocked, typePlayerUnblocked,
		typeClanCreated, typeClanMemberJoined, typeClanMemberLeft, typeClanDisbanded,
		typeClanJoinRequested, typeClanJoinDeclined,
	} {
		if !handledOnce(typ) {
			t.Errorf("%s is not handled once", typ)
		}
		d := NewDispatcher(&fakeEvents{}, &fakeSender{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := d.store(context.Background(), eventOf(typ))
		if err != nil && strings.Contains(err.Error(), "does not store") {
			t.Errorf("store has no case for %s", typ)
		}
	}
	for _, typ := range []string{typeConfigPublished, typeGameSuspended, typeGameResumed, typeClanRenamed, typeClanMemberRoleChanged} {
		if handledOnce(typ) {
			t.Errorf("%s is handled once, want it left out", typ)
		}
	}
}

// fakeForgetter counts cleanups and can fail.
type fakeForgetter struct {
	mu    sync.Mutex
	keeps []time.Duration
	err   error
}

func (f *fakeForgetter) ForgetHandledEvents(_ context.Context, keep time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keeps = append(f.keeps, keep)
	return 3, f.err
}

func (f *fakeForgetter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keeps)
}

func TestRunCleanupRepeatsUntilTheContextEnds(t *testing.T) {
	f := &fakeForgetter{err: errors.New("database down")} // a failure does not stop it
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCleanup(ctx, f, 48*time.Hour, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	deadline := time.Now().Add(2 * time.Second)
	for f.count() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not repeat")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not stop after the context ended")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keeps[0] != 48*time.Hour {
		t.Errorf("keep = %s, want 48h", f.keeps[0])
	}
}

// eventOf returns an event of the type with some data; enough to reach the type's case in store.
func eventOf(typ string) events.Event {
	return events.Event{ID: "e", Type: typ, GameID: "g", Data: map[string]any{"k": "v"}}
}

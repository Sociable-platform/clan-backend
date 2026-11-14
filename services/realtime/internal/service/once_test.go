package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const event1 = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestHandleOnceSkipsACopyAndRetriesAFailure(t *testing.T) {
	store := &memStore{}
	svc := New(store)
	ctx := context.Background()
	runs := 0
	ok := func(context.Context) error { runs++; return nil }

	if handled, err := svc.HandleOnce(ctx, game, event1, ok); err != nil || !handled {
		t.Fatalf("first = %v, %v; want handled", handled, err)
	}
	// The same IDs in upper case are the same event.
	if handled, err := svc.HandleOnce(ctx, strings.ToUpper(game), strings.ToUpper(event1), ok); err != nil || handled {
		t.Fatalf("copy = %v, %v; want skipped", handled, err)
	}
	if runs != 1 {
		t.Errorf("fn ran %d times, want 1", runs)
	}

	boom := errors.New("boom")
	other := "6ba7b810-9dad-41d1-80b4-00c04fd430c8"
	if _, err := svc.HandleOnce(ctx, game, other, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want fn's error as it is", err)
	}
	if handled, err := svc.HandleOnce(ctx, game, other, ok); err != nil || !handled {
		t.Errorf("after a failure = %v, %v; want handled again", handled, err)
	}
}

func TestHandleOnceRejectsIDsThatAreNotUUIDs(t *testing.T) {
	svc := New(&memStore{})
	for _, ids := range [][2]string{{"g-1", event1}, {game, "e1"}, {game, ""}} {
		ran := false
		_, err := svc.HandleOnce(context.Background(), ids[0], ids[1], func(context.Context) error { ran = true; return nil })
		if !errors.Is(err, ErrInvalidEvent) || ran {
			t.Errorf("HandleOnce(%q, %q) = %v, ran %v; want ErrInvalidEvent without running fn", ids[0], ids[1], err, ran)
		}
	}
}

func TestForgetHandledEventsDeletesBeforeNowMinusKeep(t *testing.T) {
	store := &memStore{}
	svc := New(store)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	if _, err := svc.ForgetHandledEvents(context.Background(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(store.deletedBefore) != 1 || !store.deletedBefore[0].Equal(now.Add(-24*time.Hour)) {
		t.Errorf("cutoffs = %v, want %v", store.deletedBefore, now.Add(-24*time.Hour))
	}
	if _, err := svc.ForgetHandledEvents(context.Background(), 0); err == nil {
		t.Error("keep 0 was accepted: it would forget everything at once")
	}
}

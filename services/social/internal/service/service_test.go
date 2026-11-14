package service

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

func TestChangeCommitsTheEventsWithTheChange(t *testing.T) {
	store := newMemStore()
	s := New(store)
	ev := events.New("friend.accepted", gameA, nil)

	if err := s.change(context.Background(), func(context.Context) ([]events.Event, error) {
		return []events.Event{ev}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.committed.Events(); len(got) != 1 || got[0].ID != ev.ID {
		t.Fatalf("committed %+v, want the one event", got)
	}
}

func TestChangeKeepsNoEventWhenTheChangeFails(t *testing.T) {
	store := newMemStore()
	s := New(store)
	boom := errors.New("clan full")

	err := s.change(context.Background(), func(ctx context.Context) ([]events.Event, error) {
		return []events.Event{events.New("clan.member_joined", gameA, nil)}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want fn's error as it is", err)
	}
	if got := store.committed.Events(); len(got) != 0 {
		t.Errorf("committed %+v after a failed change, want nothing", got)
	}
}

func TestChangeWithoutEventsWritesNothing(t *testing.T) {
	store := newMemStore()
	store.addErr = errors.New("must not be called")
	if err := New(store).change(context.Background(), func(context.Context) ([]events.Event, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("err = %v, want nil: no event, no outbox write", err)
	}
}

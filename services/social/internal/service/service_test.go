package service

import (
	"context"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

func TestNewDropsEventsUntilAPublisherIsSet(t *testing.T) {
	s := New(nil)
	if _, ok := s.events.(events.Noop); !ok {
		t.Fatalf("events = %T, want events.Noop", s.events)
	}
}

func TestWithPublisherSetsThePublisher(t *testing.T) {
	rec := &events.Recorder{}
	s := New(nil)
	if got := s.WithPublisher(rec); got != s {
		t.Fatal("WithPublisher did not return the same service")
	}
	if err := s.events.Publish(context.Background(), events.New("friend.accepted", "g", nil)); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events()) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.Events()))
	}
}

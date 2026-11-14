package service

import (
	"context"
	"errors"
	"time"
)

// HandleOnce runs fn, which handles one event through the service's event methods with the ctx it is
// given, unless an event with the same ID was handled before. RabbitMQ delivers at least once, and a
// publisher's outbox relay can send an event twice, so without this a copy would send its frames again.
//
// The event's ID is recorded in the same transaction as everything fn stores: if fn fails, neither is
// kept and the event can be handled again. handled is false for a copy that was skipped; then fn was
// not run. fn's error is returned as it is. A game or event ID that is not a UUID is ErrInvalidEvent.
func (s *Service) HandleOnce(ctx context.Context, gameID, eventID string, fn func(ctx context.Context) error) (handled bool, err error) {
	gameID, err = cleanID("game_id", gameID)
	if err != nil {
		return false, err
	}
	eventID, err = cleanID("event_id", eventID)
	if err != nil {
		return false, err
	}
	return s.store.RunOnce(ctx, gameID, eventID, fn)
}

// ForgetHandledEvents deletes the records of the events handled more than keep ago, and returns how
// many there were. keep must be longer than any copy of an event can arrive after the first.
func (s *Service) ForgetHandledEvents(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= 0 {
		return 0, errors.New("keep must be positive")
	}
	return s.store.DeleteProcessedEvents(ctx, s.now().Add(-keep))
}

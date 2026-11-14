package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/config"
	"gitlab.com/clan-platform/clan-backend/pkg/events"
)

// relayStopWait is how long shutdown waits for the outbox relay to record the batch it is sending.
const relayStopWait = 5 * time.Second

// startRelay starts the outbox relay in its own goroutine, which stops when ctx ends. The returned
// channel is closed once it has stopped. Without a database there is no outbox, so nothing is
// started and the channel is closed at once. The relay is configured by <prefix>_OUTBOX_INTERVAL
// and <prefix>_OUTBOX_RETENTION.
func startRelay(ctx context.Context, pool *pgxpool.Pool, pub events.Publisher, prefix string, log *slog.Logger) (<-chan struct{}, error) {
	done := make(chan struct{})
	if pool == nil {
		close(done)
		return done, nil
	}
	interval, err := config.Duration(prefix+"_OUTBOX_INTERVAL", events.DefaultRelayInterval)
	if err != nil {
		return nil, err
	}
	retention, err := config.Duration(prefix+"_OUTBOX_RETENTION", events.DefaultRelayRetention)
	if err != nil {
		return nil, err
	}
	relay, err := events.NewRelay(pool, pub, events.RelayConfig{Interval: interval, Retention: retention}, log)
	if err != nil {
		return nil, fmt.Errorf("%s_OUTBOX_INTERVAL / %s_OUTBOX_RETENTION: %w", prefix, prefix, err)
	}
	go func() {
		defer close(done)
		relay.Run(ctx) // returns when ctx ends (SIGTERM)
	}()
	return done, nil
}

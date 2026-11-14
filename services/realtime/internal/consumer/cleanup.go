package consumer

import (
	"context"
	"log/slog"
	"time"
)

// DefaultHandledRetention is how long the ID of a handled event is kept, so a copy that arrives
// within that time is skipped.
const DefaultHandledRetention = 7 * 24 * time.Hour

// cleanupEvery is how often RunCleanup deletes old records.
const cleanupEvery = time.Hour

// Forgetter deletes the records of events handled long ago. The service does this.
type Forgetter interface {
	ForgetHandledEvents(ctx context.Context, keep time.Duration) (int64, error)
}

// RunCleanup deletes the records of events handled more than keep ago, once at start and then every
// hour, until ctx ends. A failure is only logged; the records are deleted next time.
func RunCleanup(ctx context.Context, f Forgetter, keep time.Duration, log *slog.Logger) {
	runCleanup(ctx, f, keep, cleanupEvery, log)
}

func runCleanup(ctx context.Context, f Forgetter, keep, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := f.ForgetHandledEvents(ctx, keep)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("deleting old handled events failed, will retry", "error", err, "retry_in", every.String())
		case n > 0:
			log.Info("deleted old handled events", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

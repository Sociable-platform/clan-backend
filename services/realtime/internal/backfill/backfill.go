// Package backfill copies the friendships that Social already has into Realtime, once, at startup.
//
// Realtime learns about friendships from events, so it knows nothing about the ones made before it ran,
// or whose event was lost. Run reads every friendship from Social's internal sync endpoint, page by page,
// and stores the DM conversation of each pair. It only adds: a conversation that Social no longer lists
// is left alone. It is safe to run any number of times.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

const (
	defaultPageSize = 200
	defaultAttempts = 5
	defaultBackoff  = time.Second
	maxBackoff      = 8 * time.Second
)

// Source gives the friendships, one page at a time. social.Client implements it.
type Source interface {
	Friendships(ctx context.Context, after string, limit int) (social.FriendshipPage, error)
}

// Store keeps one friendship. service.Service implements it.
type Store interface {
	SyncFriendship(ctx context.Context, gameID string, a, b service.Player) error
}

// Options tune Run. The zero value is fine.
type Options struct {
	PageSize int           // friendships per request; default 200
	Attempts int           // tries per page while Social is unavailable; default 5
	Backoff  time.Duration // wait before the second try, doubled each time up to 8 s; default 1 s
	Log      *slog.Logger  // optional

	wait func(ctx context.Context, d time.Duration) error // replaced in tests
}

// Result tells what Run did.
type Result struct {
	Pages   int // pages read
	Synced  int // friendships stored (or found stored already)
	Skipped int // friendships Social listed that Realtime refused as invalid
}

// Run reads every friendship from src and stores it with st. A page that Social cannot answer is tried
// again, waiting longer each time; when it keeps failing, Run returns the error together with what it did
// until then. A friendship with a wrong ID or no name is skipped and counted, not fatal. A database
// error stops Run at once. Run ends early when ctx ends.
func Run(ctx context.Context, src Source, st Store, opts Options) (Result, error) {
	opts = opts.withDefaults()
	var res Result
	cursor := ""
	for {
		page, err := fetch(ctx, src, cursor, opts)
		if err != nil {
			return res, fmt.Errorf("read friendships page %d: %w", res.Pages+1, err)
		}
		res.Pages++

		for _, f := range page.Friendships {
			err := st.SyncFriendship(ctx, f.GameID, service.Player{ID: f.A.ID, Name: f.A.DisplayName}, service.Player{ID: f.B.ID, Name: f.B.DisplayName})
			switch {
			case errors.Is(err, service.ErrInvalidEvent):
				res.Skipped++
				opts.Log.Warn("backfill skipped a friendship", "game_id", f.GameID, "player_a", f.A.ID, "player_b", f.B.ID, "error", err)
			case err != nil:
				return res, fmt.Errorf("store friendship: %w", err)
			default:
				res.Synced++
			}
		}

		if page.NextCursor == "" {
			return res, nil
		}
		if page.NextCursor == cursor {
			// Without this, a misbehaving Social could keep the service in this loop forever.
			return res, fmt.Errorf("social repeated the cursor of page %d", res.Pages)
		}
		cursor = page.NextCursor
	}
}

// fetch gets one page, trying again while Social is unavailable.
func fetch(ctx context.Context, src Source, cursor string, opts Options) (social.FriendshipPage, error) {
	backoff := opts.Backoff
	for attempt := 1; ; attempt++ {
		page, err := src.Friendships(ctx, cursor, opts.PageSize)
		if err == nil {
			return page, nil
		}
		// Only an outage is worth another try; a setup mistake or an ended context will not change.
		if ctx.Err() != nil || !errors.Is(err, social.ErrUnavailable) || attempt >= opts.Attempts {
			return social.FriendshipPage{}, err
		}
		opts.Log.Warn("backfill could not read friendships, trying again", "attempt", attempt, "wait", backoff, "error", err)
		if werr := opts.wait(ctx, backoff); werr != nil {
			return social.FriendshipPage{}, werr
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (o Options) withDefaults() Options {
	if o.PageSize <= 0 {
		o.PageSize = defaultPageSize
	}
	if o.Attempts <= 0 {
		o.Attempts = defaultAttempts
	}
	if o.Backoff <= 0 {
		o.Backoff = defaultBackoff
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.wait == nil {
		o.wait = sleep
	}
	return o
}

// sleep waits d, or less when ctx ends first, which is then its error.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

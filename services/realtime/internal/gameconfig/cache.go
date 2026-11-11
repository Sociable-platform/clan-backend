package gameconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrStale means Platform still answers with an older version than the one that was just
// published. The event was ahead of the data, so the caller should try again shortly.
var ErrStale = errors.New("platform has not got the published version yet")

// Cache holds the newest config version realtime has seen of each game. It is safe for concurrent use.
type Cache struct {
	src     Source
	now     func() time.Time
	failFor time.Duration // how long a failed or empty load is remembered
	timeout time.Duration // limit of one load, whoever asked

	mu      sync.Mutex
	configs map[string]Config  // game ID -> newest version seen
	failed  map[string]failure // game ID -> why the last lazy load failed
	loads   singleflight.Group // one request to Platform at a time per game and kind
}

type failure struct {
	at  time.Time
	err error
}

// Options tune a Cache. Zero values give the defaults.
type Options struct {
	// FailFor is how long a lazy load that failed (or found no published config) is remembered, so a
	// Platform that is down does not slow down every call (default 5 s).
	FailFor time.Duration
	// Timeout limits one load from the source (default 5 s).
	Timeout time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// NewCache returns an empty Cache that loads from src.
func NewCache(src Source, opts Options) *Cache {
	if opts.FailFor <= 0 {
		opts.FailFor = 5 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Cache{
		src: src, now: opts.Now, failFor: opts.FailFor, timeout: opts.Timeout,
		configs: make(map[string]Config), failed: make(map[string]failure),
	}
}

// Get returns the config of a game, loading it from Platform the first time it is asked for.
// Concurrent first calls for the same game make one request. A game with no published config
// gives a zero Config (Version 0) and no error. A Platform that cannot answer gives an error
// wrapping ErrUnavailable, and nothing is cached for it, except that the failure is remembered
// for a few seconds.
func (c *Cache) Get(ctx context.Context, gameID string) (Config, error) {
	c.mu.Lock()
	if cfg, ok := c.configs[gameID]; ok {
		c.mu.Unlock()
		return cfg, nil
	}
	if f, ok := c.failed[gameID]; ok && c.now().Sub(f.at) < c.failFor {
		c.mu.Unlock()
		return Config{}, nothingIfNotPublished(f.err)
	}
	c.mu.Unlock()

	// The load stores its own result, so it is not lost when the caller stops waiting.
	cfg, err := c.wait(ctx, c.loads.DoChan("get:"+gameID, func() (any, error) {
		cfg, err := c.fetchNow(ctx, gameID)
		if err != nil {
			c.mu.Lock()
			c.failed[gameID] = failure{at: c.now(), err: err}
			c.mu.Unlock()
			return nil, err
		}
		return c.store(gameID, cfg), nil
	}))
	return cfg, nothingIfNotPublished(err)
}

// Version returns the version of the game's config, or 0 when it is not known (no published config,
// Platform not reachable, or ctx ended first). It never fails: the caller wants a number to show.
func (c *Cache) Version(ctx context.Context, gameID string) int {
	cfg, err := c.Get(ctx, gameID)
	if err != nil {
		return 0
	}
	return cfg.Version
}

// Published is called when the game published a config version. It fetches the config again and
// swaps it in the cache, then reports the version to tell the players about and whether to tell
// them at all.
//
// When Platform fails, or does not have that version yet (ErrStale), the old config stays in the
// cache and an error is returned, so the caller can try the event again.
//
// send is false when the cache already held a newer version than the event's: the event arrived
// out of order and the players were told about the newer one. Otherwise it is true even when the
// cache already had this version, because the cache can be filled by a player connecting
// (hello) before the event arrives, and the players who were connected earlier still need to hear it.
func (c *Cache) Published(ctx context.Context, gameID string, version int) (current int, send bool, err error) {
	// A refresh is its own request: a lazy load that started before the publish may have the old version.
	cfg, err := c.wait(ctx, c.loads.DoChan("refresh:"+gameID, func() (any, error) {
		return c.fetchNow(ctx, gameID)
	}))
	if err != nil {
		return 0, false, fmt.Errorf("fetch config of game %s: %w", gameID, err)
	}
	if cfg.Version < version {
		return 0, false, fmt.Errorf("published version %d, platform has %d: %w", version, cfg.Version, ErrStale)
	}
	c.mu.Lock()
	old, had := c.configs[gameID]
	c.mu.Unlock()
	if had && old.Version > version {
		return old.Version, false, nil
	}
	stored := c.store(gameID, cfg)
	return stored.Version, true, nil
}

// store puts cfg in the cache unless a newer version is there, and returns what the cache holds.
func (c *Cache) store(gameID string, cfg Config) Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.configs[gameID]; ok && old.Version > cfg.Version {
		return old
	}
	cfg.Chat = ParseChatRules(cfg.Settings) // once per version, not once per message
	c.configs[gameID] = cfg
	return cfg
}

// fetchNow asks the source once. The request does not stop when the caller that started it gives up
// (other callers may be waiting for it), but it has a limit of its own.
func (c *Cache) fetchNow(ctx context.Context, gameID string) (Config, error) {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	return c.src.Fetch(lctx, gameID)
}

// wait returns the result of a shared load, or an error wrapping ErrUnavailable when ctx ends first.
func (c *Cache) wait(ctx context.Context, ch <-chan singleflight.Result) (Config, error) {
	select {
	case res := <-ch:
		if res.Err != nil {
			return Config{}, res.Err
		}
		return res.Val.(Config), nil
	case <-ctx.Done():
		return Config{}, fmt.Errorf("waiting for platform: %w", errors.Join(ErrUnavailable, ctx.Err()))
	}
}

// nothingIfNotPublished turns "no published config" into an empty answer: that is a normal state of a game.
func nothingIfNotPublished(err error) error {
	if errors.Is(err, ErrNotPublished) {
		return nil
	}
	return err
}

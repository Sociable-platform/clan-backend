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

// Cache holds the newest config version realtime has seen of each game, and whether the game is
// suspended. It is safe for concurrent use.
type Cache struct {
	src       Source
	now       func() time.Time
	failFor   time.Duration // how long a failed or empty load is remembered
	timeout   time.Duration // limit of one load, whoever asked
	statusTTL time.Duration // how long a status is trusted before the next new connection checks again

	mu      sync.Mutex
	configs map[string]Config  // game ID -> newest version seen
	failed  map[string]failure // game ID -> why the last lazy load failed
	status  map[string]status  // game ID -> last known suspension status
	loads   singleflight.Group // one request to Platform at a time per game and kind
}

type failure struct {
	at  time.Time
	err error
}

// status is what realtime last learned about whether a game is suspended.
type status struct {
	suspended bool
	// until is when a new connection asks Platform again. Events set the status at once; this only
	// bounds how long a lost event (or a game never asked about) can leave it wrong.
	until time.Time
}

// Options tune a Cache. Zero values give the defaults.
type Options struct {
	// FailFor is how long a lazy load that failed (or found no published config) is remembered, so a
	// Platform that is down does not slow down every call (default 5 s).
	FailFor time.Duration
	// Timeout limits one load from the source (default 5 s).
	Timeout time.Duration
	// StatusTTL is how long a game's suspended or active status is trusted before the next new
	// connection checks Platform again (default 30 s). Events change it at once; this is the safety
	// net for an event that never arrived.
	StatusTTL time.Duration
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
	if opts.StatusTTL <= 0 {
		opts.StatusTTL = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Cache{
		src: src, now: opts.Now, failFor: opts.FailFor, timeout: opts.Timeout, statusTTL: opts.StatusTTL,
		configs: make(map[string]Config), failed: make(map[string]failure), status: make(map[string]status),
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
// (other callers may be waiting for it), but it has a limit of its own. Whatever the answer says
// about the game being suspended is remembered.
func (c *Cache) fetchNow(ctx context.Context, gameID string) (Config, error) {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	cfg, err := c.src.Fetch(lctx, gameID)
	c.learn(gameID, err)
	return cfg, err
}

// learn records what an answer of Platform says about the game's status. A config (or "not
// published") means the game is served, ErrSuspended means it is not. Anything else is trouble and
// says nothing: the last known status stays, and Platform is not asked again for a few seconds. A
// game never seen is taken as served, so a Platform that is down never locks players out.
func (c *Cache) learn(gameID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil || errors.Is(err, ErrNotPublished):
		c.status[gameID] = status{suspended: false, until: c.now().Add(c.statusTTL)}
	case errors.Is(err, ErrSuspended):
		c.status[gameID] = status{suspended: true, until: c.now().Add(c.statusTTL)}
	case errors.Is(err, ErrInvalidGame):
		// nothing to remember about a game ID that is not a game
	default:
		old := c.status[gameID] // the zero value (served) for a game never seen
		c.status[gameID] = status{suspended: old.suspended, until: c.now().Add(c.failFor)}
	}
}

// Suspended reports whether the game is suspended, for a connection that is about to open. It is
// answered from memory while the status is fresh (events keep it so); otherwise Platform is asked
// once, shared by everyone who asks at the same time. It never fails: when Platform cannot be
// reached or ctx ends first, the last known status is used, or "not suspended" for a game never seen.
func (c *Cache) Suspended(ctx context.Context, gameID string) bool {
	c.mu.Lock()
	st, known := c.status[gameID]
	c.mu.Unlock()
	if known && c.now().Before(st.until) {
		return st.suspended
	}

	// The answer also tells the config, so keep it: the hello that follows then finds it in memory.
	cfg, err := c.wait(ctx, c.loads.DoChan("refresh:"+gameID, func() (any, error) {
		return c.fetchNow(ctx, gameID)
	}))
	if err == nil {
		c.store(gameID, cfg)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status[gameID].suspended
}

// SetSuspended records what an event said: the game was just suspended (or resumed). The change is
// seen by the next connection at once, without asking Platform.
func (c *Cache) SetSuspended(gameID string, suspended bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status[gameID] = status{suspended: suspended, until: c.now().Add(c.statusTTL)}
}

// Resumed is called when the game was resumed. It asks Platform, which has the final say, and keeps
// the answer: new connections succeed again, and the config version they are told is the newest
// one (the game may have published while it was suspended, and that event had nothing to refresh).
// If Platform says the game is still suspended (the event was old), nothing else is needed. An
// error means Platform could not be asked, so the caller should try the event again; until it
// works the last known status stays and the safety net of StatusTTL applies.
func (c *Cache) Resumed(ctx context.Context, gameID string) error {
	cfg, err := c.wait(ctx, c.loads.DoChan("refresh:"+gameID, func() (any, error) {
		return c.fetchNow(ctx, gameID)
	}))
	switch {
	case err == nil:
		c.store(gameID, cfg)
		return nil
	case errors.Is(err, ErrNotPublished), errors.Is(err, ErrSuspended):
		return nil
	}
	return fmt.Errorf("fetch config of game %s: %w", gameID, err)
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

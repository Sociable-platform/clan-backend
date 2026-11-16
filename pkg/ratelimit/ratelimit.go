// Package ratelimit counts attempts per key (an email address, an IP address) in memory.
//
// It is a fixed window: a key may make Limit attempts, and the count starts again Window after the
// key's first attempt. The counts live in this process only, so each pod counts on its own and a
// restart forgets them. That is enough to stop one person from flooding an endpoint; it is not a
// quota.
package ratelimit

import (
	"sync"
	"time"
)

// sweepAt is how many keys are kept before the finished windows are thrown away.
const sweepAt = 10000

type window struct {
	start time.Time
	count int
}

// Limiter allows a fixed number of attempts per key and window. Use New; it is safe for
// concurrent use.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]window
}

// New returns a Limiter that allows limit attempts per key in each window.
func New(limit int, window time.Duration) *Limiter {
	return newLimiter(limit, window, time.Now)
}

func newLimiter(limit int, per time.Duration, now func() time.Time) *Limiter {
	return &Limiter{limit: limit, window: per, now: now, windows: map[string]window{}}
}

// Allow counts one attempt for key and reports whether it is still within the limit.
func (l *Limiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= l.window {
		if len(l.windows) >= sweepAt {
			l.sweep(now)
		}
		w = window{start: now}
	}
	w.count++
	l.windows[key] = w
	return w.count <= l.limit
}

// RetryAfter is how long a refused caller should wait: the length of a window.
func (l *Limiter) RetryAfter() time.Duration { return l.window }

// sweep removes the windows that are over. The caller holds the lock.
func (l *Limiter) sweep(now time.Time) {
	for key, w := range l.windows {
		if now.Sub(w.start) >= l.window {
			delete(l.windows, key)
		}
	}
}

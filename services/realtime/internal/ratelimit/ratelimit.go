// Package ratelimit holds the two limits on how fast a player may send messages: a sliding window per
// connection (spam protection) and the game's slow mode per player (a pause between messages).
package ratelimit

import (
	"sync"
	"time"
)

// Window allows at most max events in any period of length per. It is not safe for concurrent use:
// the one goroutine that reads a connection's frames owns it.
type Window struct {
	max   int
	per   time.Duration
	times []time.Time // the events that were allowed and are still inside the window, oldest first
}

// NewWindow returns a Window that allows max events per period.
func NewWindow(max int, per time.Duration) *Window {
	return &Window{max: max, per: per}
}

// Allow reports whether an event at now may happen, and counts it when it may. An event that is
// refused is not counted, so a client that stops sending is allowed again once the oldest events
// are older than the period.
func (w *Window) Allow(now time.Time) bool {
	cut := 0
	for cut < len(w.times) && !w.times[cut].After(now.Add(-w.per)) {
		cut++
	}
	w.times = w.times[cut:]
	if len(w.times) >= w.max {
		return false
	}
	w.times = append(w.times, now)
	return true
}

// staleAfter is how long a player's last message time is kept at least: longer than any slow mode a game can set (30 s).
const staleAfter = time.Minute

type slowKey struct{ gameID, playerID string }

// Slow remembers when each player last sent a message, so a game's slow mode holds for all their tabs
// and conversations. It is safe for concurrent use. It lives in the memory of the one process.
type Slow struct {
	mu        sync.Mutex
	last      map[slowKey]time.Time
	lastPrune time.Time
}

// NewSlow returns an empty Slow.
func NewSlow() *Slow {
	return &Slow{last: make(map[slowKey]time.Time)}
}

// Reservation is a message time that was taken with Reserve. Cancel gives it back.
type Reservation struct {
	slow *Slow
	key  slowKey
	at   time.Time
	prev time.Time
	had  bool
}

// Reserve takes the player's turn at now, atomically, so two tabs cannot both pass. When the player sent
// a message less than interval ago it returns ok false and how long to wait. An interval of zero or less
// means no slow mode: nothing is remembered and ok is true.
func (s *Slow) Reserve(gameID, playerID string, now time.Time, interval time.Duration) (r Reservation, retry time.Duration, ok bool) {
	if interval <= 0 {
		return Reservation{}, 0, true
	}
	k := slowKey{gameID, playerID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	prev, had := s.last[k]
	if had {
		if elapsed := now.Sub(prev); elapsed >= 0 && elapsed < interval {
			return Reservation{}, interval - elapsed, false
		}
	}
	s.last[k] = now
	return Reservation{slow: s, key: k, at: now, prev: prev, had: had}, 0, true
}

// Cancel gives the turn back, because the message was not stored after all. It does nothing for a
// reservation that has been replaced by a later one, or one that did not take anything.
func (r Reservation) Cancel() {
	if r.slow == nil {
		return
	}
	r.slow.mu.Lock()
	defer r.slow.mu.Unlock()
	if cur, ok := r.slow.last[r.key]; !ok || !cur.Equal(r.at) {
		return
	}
	if r.had {
		r.slow.last[r.key] = r.prev
	} else {
		delete(r.slow.last, r.key)
	}
}

// prune forgets players whose last message is old, at most once a minute, so the map does not grow for ever.
func (s *Slow) prune(now time.Time) {
	if now.Sub(s.lastPrune) < time.Minute {
		return
	}
	s.lastPrune = now
	for k, t := range s.last {
		if now.Sub(t) > staleAfter {
			delete(s.last, k)
		}
	}
}

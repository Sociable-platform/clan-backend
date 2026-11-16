package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Minute, func() time.Time { return now })

	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("the first two attempts must be allowed")
	}
	if l.Allow("a") {
		t.Fatal("the third attempt in the window must be refused")
	}
	if !l.Allow("b") {
		t.Fatal("another key has its own count")
	}

	now = now.Add(59 * time.Second)
	if l.Allow("a") {
		t.Fatal("still the same window")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("a new window must start one minute after the first attempt")
	}
}

func TestLimiterSweepsFinishedWindows(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	l := newLimiter(1, time.Minute, func() time.Time { return now })
	l.Allow("old")
	now = now.Add(2 * time.Minute)
	l.sweep(now)
	if len(l.windows) != 0 {
		t.Fatalf("%d windows left after the sweep, want 0", len(l.windows))
	}
}

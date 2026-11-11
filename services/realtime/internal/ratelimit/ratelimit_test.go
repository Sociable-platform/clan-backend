package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func TestWindow(t *testing.T) {
	tests := []struct {
		name   string
		offs   []time.Duration // when events are tried
		allows []bool
	}{
		{"five at once, the sixth is refused", []time.Duration{0, 0, 0, 0, 0, 0}, []bool{true, true, true, true, true, false}},
		{"spread over 5 s: the sixth right after the window moves on",
			[]time.Duration{0, time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 4900 * time.Millisecond, 5 * time.Second},
			[]bool{true, true, true, true, true, false, true}},
		{"exactly 5 s after the first one is a new window", []time.Duration{0, 0, 0, 0, 0, 5 * time.Second}, []bool{true, true, true, true, true, true}},
		{"just under 5 s after is still full", []time.Duration{0, 0, 0, 0, 0, 5*time.Second - time.Millisecond}, []bool{true, true, true, true, true, false}},
		{"refused events are not counted: stopping for 5 s is enough",
			[]time.Duration{0, 0, 0, 0, 0, 1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second},
			[]bool{true, true, true, true, true, false, false, false, false, true}},
		{"a slow sender is never limited", []time.Duration{0, 2 * time.Second, 4 * time.Second, 6 * time.Second, 8 * time.Second, 10 * time.Second, 12 * time.Second}, []bool{true, true, true, true, true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWindow(5, 5*time.Second)
			for i, off := range tt.offs {
				if got := w.Allow(t0.Add(off)); got != tt.allows[i] {
					t.Errorf("event %d at +%v: allowed = %v, want %v", i+1, off, got, tt.allows[i])
				}
			}
		})
	}
}

func TestSlowRefusesWithinTheIntervalAndTellsHowLong(t *testing.T) {
	s := NewSlow()
	if _, _, ok := s.Reserve("g", "p", t0, 5*time.Second); !ok {
		t.Fatal("the first message was refused")
	}
	_, retry, ok := s.Reserve("g", "p", t0.Add(1500*time.Millisecond), 5*time.Second)
	if ok || retry != 3500*time.Millisecond {
		t.Fatalf("ok = %v, retry = %v, want refused with 3.5 s to wait", ok, retry)
	}
	if _, _, ok := s.Reserve("g", "p", t0.Add(5*time.Second), 5*time.Second); !ok {
		t.Error("a message exactly one interval later was refused")
	}
}

func TestSlowRefusedAttemptsDoNotPushTheTurnBack(t *testing.T) {
	s := NewSlow()
	s.Reserve("g", "p", t0, 5*time.Second)
	for i := 1; i <= 4; i++ {
		s.Reserve("g", "p", t0.Add(time.Duration(i)*time.Second), 5*time.Second) // refused
	}
	if _, _, ok := s.Reserve("g", "p", t0.Add(5*time.Second), 5*time.Second); !ok {
		t.Error("trying again and again moved the player's turn back")
	}
}

func TestSlowIsPerPlayerAndPerGame(t *testing.T) {
	s := NewSlow()
	s.Reserve("g1", "ali", t0, 5*time.Second)
	for _, c := range []struct{ game, player string }{{"g1", "vusal"}, {"g2", "ali"}} {
		if _, _, ok := s.Reserve(c.game, c.player, t0, 5*time.Second); !ok {
			t.Errorf("%s/%s was held back by somebody else's message", c.game, c.player)
		}
	}
}

func TestSlowZeroIntervalIsNoSlowMode(t *testing.T) {
	s := NewSlow()
	for i := 0; i < 3; i++ {
		if _, _, ok := s.Reserve("g", "p", t0, 0); !ok {
			t.Fatal("refused without slow mode")
		}
	}
	if len(s.last) != 0 {
		t.Error("something was remembered without slow mode")
	}
}

func TestSlowTheGamesCurrentIntervalDecides(t *testing.T) {
	s := NewSlow()
	s.Reserve("g", "p", t0, 10*time.Second)
	if _, _, ok := s.Reserve("g", "p", t0.Add(3*time.Second), 2*time.Second); !ok {
		t.Error("after the game shortened the interval to 2 s, a message 3 s later was refused")
	}
}

func TestSlowCancelGivesTheTurnBack(t *testing.T) {
	s := NewSlow()
	r, _, _ := s.Reserve("g", "p", t0, 5*time.Second)
	r.Cancel()
	if _, _, ok := s.Reserve("g", "p", t0.Add(time.Second), 5*time.Second); !ok {
		t.Error("the turn of a message that was not stored still counted")
	}
}

func TestSlowCancelRestoresThePreviousMessageTime(t *testing.T) {
	s := NewSlow()
	s.Reserve("g", "p", t0, 5*time.Second)
	r, _, ok := s.Reserve("g", "p", t0.Add(6*time.Second), 5*time.Second)
	if !ok {
		t.Fatal("refused")
	}
	r.Cancel()
	// The message at +6 s was not stored; the one at t0 was, and it is long ago.
	if _, _, ok := s.Reserve("g", "p", t0.Add(7*time.Second), 5*time.Second); !ok {
		t.Error("a cancelled reservation blocked the player")
	}
}

func TestSlowCancelOfAReplacedReservationDoesNothing(t *testing.T) {
	s := NewSlow()
	old, _, _ := s.Reserve("g", "p", t0, 5*time.Second)
	_, _, _ = s.Reserve("g", "p", t0.Add(6*time.Second), 5*time.Second) // a later message was stored
	old.Cancel()
	if _, _, ok := s.Reserve("g", "p", t0.Add(7*time.Second), 5*time.Second); ok {
		t.Error("cancelling an old reservation erased the newer message time")
	}
}

func TestSlowCancelOfAZeroReservationDoesNothing(t *testing.T) {
	Reservation{}.Cancel() // must not panic
}

func TestSlowForgetsOldPlayers(t *testing.T) {
	s := NewSlow()
	s.Reserve("g", "old", t0, time.Second)
	s.Reserve("g", "new", t0.Add(2*time.Minute), time.Second)
	if _, ok := s.last[slowKey{"g", "old"}]; ok {
		t.Error("a player who wrote 2 minutes ago is still remembered")
	}
	if _, ok := s.last[slowKey{"g", "new"}]; !ok {
		t.Error("the current player is not remembered")
	}
}

// Two tabs of one player send at the same moment: exactly one gets through. Run with -race.
func TestSlowTwoTabsAtOnceOnlyOneGetsThrough(t *testing.T) {
	s := NewSlow()
	var passed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, _, ok := s.Reserve("g", "p", t0, 5*time.Second); ok {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("%d messages got through, want exactly 1", passed.Load())
	}
}

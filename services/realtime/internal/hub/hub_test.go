package hub

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// detached returns a Conn that has no socket: enough to test queueing, closing and the hub.
func detached(gameID, playerID string, buffer int) *Conn {
	opts := ConnOptions{SendBuffer: buffer}.withDefaults()
	return &Conn{
		id:   Identity{GameID: gameID, PlayerID: playerID},
		opts: opts,
		send: make(chan []byte, opts.SendBuffer),
		done: make(chan struct{}),
	}
}

// drain returns the frames waiting in the queue, as strings.
func drain(c *Conn) []string {
	var got []string
	for {
		select {
		case f := <-c.send:
			got = append(got, string(f))
		default:
			return got
		}
	}
}

func TestRegisterAndUnregisterReportFirstAndLastTab(t *testing.T) {
	h := New()
	tab1, tab2 := detached("g1", "p1", 4), detached("g1", "p1", 4)

	if !h.Register(tab1) {
		t.Fatal("first tab: first = false, want true")
	}
	if h.Register(tab2) {
		t.Fatal("second tab: first = true, want false")
	}
	if !h.Online("g1", "p1") || h.Len() != 2 {
		t.Fatalf("online = %v, len = %d; want true, 2", h.Online("g1", "p1"), h.Len())
	}

	if h.Unregister(tab1) {
		t.Fatal("closing one of two tabs: last = true, want false")
	}
	if !h.Online("g1", "p1") {
		t.Fatal("the player went offline while a tab is still open")
	}
	if !h.Unregister(tab2) {
		t.Fatal("closing the last tab: last = false, want true")
	}
	if h.Online("g1", "p1") || h.Len() != 0 {
		t.Fatalf("online = %v, len = %d; want false, 0", h.Online("g1", "p1"), h.Len())
	}
	if len(h.conns) != 0 {
		t.Fatalf("empty games and players are not cleaned up: %v", h.conns)
	}
}

func TestUnregisterUnknownConnDoesNothing(t *testing.T) {
	h := New()
	registered := detached("g1", "p1", 4)
	h.Register(registered)
	if h.Unregister(detached("g1", "p1", 4)) || h.Unregister(detached("g2", "p9", 4)) {
		t.Fatal("unregistering a connection that was never registered reported a change")
	}
	if h.Len() != 1 {
		t.Fatalf("len = %d, want 1", h.Len())
	}
	h.Unregister(registered)
	if h.Unregister(registered) { // a second Unregister of the same connection
		t.Fatal("unregistering twice reported a change the second time")
	}
}

func TestSendToPlayers(t *testing.T) {
	h := New()
	alice1, alice2 := detached("g1", "alice", 4), detached("g1", "alice", 4)
	bob := detached("g1", "bob", 4)
	carol := detached("g1", "carol", 4)
	aliceOtherGame := detached("g2", "alice", 4) // same player ID, another game
	for _, c := range []*Conn{alice1, alice2, bob, carol, aliceOtherGame} {
		h.Register(c)
	}

	h.SendToPlayers("g1", []string{"alice", "bob", "alice", "nobody"}, []byte("hello"))

	for name, tc := range map[string]struct {
		c    *Conn
		want int
	}{
		"alice tab 1":           {alice1, 1}, // listed twice, still one frame
		"alice tab 2":           {alice2, 1}, // every tab of a listed player
		"bob":                   {bob, 1},
		"carol (not listed)":    {carol, 0},
		"alice of another game": {aliceOtherGame, 0}, // games never see each other's frames
	} {
		if got := drain(tc.c); len(got) != tc.want || (tc.want == 1 && got[0] != "hello") {
			t.Errorf("%s got %v, want %d frame(s)", name, got, tc.want)
		}
	}

	// Nothing for an unknown game, nothing for an empty list; neither may panic.
	h.SendToPlayers("g-unknown", []string{"alice"}, []byte("x"))
	h.SendToPlayers("g1", nil, []byte("x"))
}

func TestEnqueueOnAFullQueueClosesWith4002WithoutBlocking(t *testing.T) {
	c := detached("g1", "p1", 2)
	c.Enqueue([]byte("1"))
	c.Enqueue([]byte("2"))

	finished := make(chan struct{})
	go func() {
		c.Enqueue([]byte("3")) // the queue is full: must close, not wait
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	select {
	case <-c.Done():
	default:
		t.Fatal("the connection was not closed")
	}
	if code, reason := c.CloseInfo(); code != CloseSlowClient || reason != "slow client" {
		t.Fatalf("closed with %d %q, want 4002 slow client", code, reason)
	}
	if got := drain(c); len(got) != 2 {
		t.Fatalf("queue holds %v, want the first 2 frames (the third is dropped)", got)
	}
}

func TestOneSlowConnDoesNotStopTheOthers(t *testing.T) {
	h := New()
	slow := detached("g1", "slow", 1)
	healthy := detached("g1", "healthy", 8)
	h.Register(slow)
	h.Register(healthy)
	slow.Enqueue([]byte("already full"))

	finished := make(chan struct{})
	go func() {
		h.SendToPlayers("g1", []string{"slow", "healthy"}, []byte("news"))
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("SendToPlayers blocked on a slow client")
	}

	if got := drain(healthy); len(got) != 1 || got[0] != "news" {
		t.Fatalf("healthy client got %v, want [news]", got)
	}
	if code, _ := slow.CloseInfo(); code != CloseSlowClient {
		t.Fatalf("slow client closed with %d, want 4002", code)
	}
}

func TestEnqueueAfterCloseDropsTheFrame(t *testing.T) {
	c := detached("g1", "p1", 4)
	c.Close(websocket.StatusNormalClosure, "bye")
	c.Enqueue([]byte("late"))
	if got := drain(c); len(got) != 0 {
		t.Fatalf("a closed connection queued %v", got)
	}
}

func TestOnlyTheFirstCloseCounts(t *testing.T) {
	c := detached("g1", "p1", 4)
	if code, reason := c.CloseInfo(); code != 0 || reason != "" {
		t.Fatalf("CloseInfo before Close = %d %q, want zero values", code, reason)
	}
	c.Close(CloseTokenExpired, "token expired")
	c.Close(CloseSlowClient, "slow client")
	if code, reason := c.CloseInfo(); code != CloseTokenExpired || reason != "token expired" {
		t.Fatalf("closed with %d %q, want 4001 token expired", code, reason)
	}
}

// Run with -race: connections come and go while frames are sent to them.
func TestHubIsSafeForConcurrentUse(t *testing.T) {
	h := New()
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		for p := 0; p < 5; p++ {
			wg.Go(func() {
				game, player := fmt.Sprintf("g%d", g), fmt.Sprintf("p%d", p)
				for i := 0; i < 200; i++ {
					c := detached(game, player, 2)
					h.Register(c)
					h.Online(game, player)
					c.Enqueue([]byte("x"))
					h.Unregister(c)
				}
			})
		}
	}
	for s := 0; s < 4; s++ {
		wg.Go(func() {
			for i := 0; i < 500; i++ {
				h.SendToPlayers("g1", []string{"p0", "p1", "p2", "p3", "p4"}, []byte("news"))
				h.Len()
			}
		})
	}
	wg.Wait()
	if h.Len() != 0 || len(h.conns) != 0 {
		t.Fatalf("connections left behind: len = %d, games = %d", h.Len(), len(h.conns))
	}
}

func TestSendToGameReachesEveryConnectionOfThatGameOnly(t *testing.T) {
	h := New()
	alice1, alice2 := detached("g1", "alice", 4), detached("g1", "alice", 4)
	bob := detached("g1", "bob", 4)
	otherGame := detached("g2", "alice", 4) // same player ID, another game
	for _, c := range []*Conn{alice1, alice2, bob, otherGame} {
		h.Register(c)
	}

	h.SendToGame("g1", []byte("config"))

	for name, tc := range map[string]struct {
		c    *Conn
		want int
	}{"alice tab 1": {alice1, 1}, "alice tab 2": {alice2, 1}, "bob": {bob, 1}, "a player of another game": {otherGame, 0}} {
		if got := drain(tc.c); len(got) != tc.want || (tc.want == 1 && got[0] != "config") {
			t.Errorf("%s got %v, want %d frame(s)", name, got, tc.want)
		}
	}
	// an unknown game, and a game whose last player left, are nobody: no panic
	h.SendToGame("g-unknown", []byte("x"))
	h.Unregister(otherGame)
	h.SendToGame("g2", []byte("x"))
}

func TestSendToGameWithAFullQueueDoesNotStopTheOthers(t *testing.T) {
	h := New()
	slow, fast := detached("g1", "slow", 1), detached("g1", "fast", 4)
	h.Register(slow)
	h.Register(fast)
	h.SendToGame("g1", []byte("1"))
	h.SendToGame("g1", []byte("2")) // slow's queue (1) is full now: it is closed, fast still gets it
	if got := drain(fast); len(got) != 2 {
		t.Errorf("fast got %v, want both frames", got)
	}
}

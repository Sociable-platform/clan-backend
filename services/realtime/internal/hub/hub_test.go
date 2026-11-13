package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCloseAfterFlushKeepsCodeAndReasonAndOnlyTheFirstCloseCounts(t *testing.T) {
	c := detached("g", "p", 4)
	c.CloseAfterFlush(ClosePolicy, "rate limit")
	c.Close(CloseSlowClient, "slow client") // a later close does not change it
	select {
	case <-c.Done():
	default:
		t.Fatal("not closed")
	}
	if code, reason := c.CloseInfo(); code != ClosePolicy || reason != "rate limit" || int(ClosePolicy) != 1008 {
		t.Errorf("close = %d %q, want 1008 rate limit", code, reason)
	}
	if !c.flush {
		t.Error("the flush flag was not kept")
	}

	d := detached("g", "p", 4)
	d.Close(websocket.StatusNormalClosure, "")
	d.CloseAfterFlush(ClosePolicy, "rate limit") // too late
	if code, _ := d.CloseInfo(); code != websocket.StatusNormalClosure || d.flush {
		t.Errorf("a close after the first one changed the result: %d flush=%v", code, d.flush)
	}
}

// Frames that are queued when the connection closes with CloseAfterFlush all reach the client, then it gets the
// close code. Closing before Serve starts makes the writer see both a full queue and a closed connection.
func TestCloseAfterFlushWritesTheQueuedFramesBeforeTheCloseFrame(t *testing.T) {
	const frames = 20
	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c := NewConn(ws, Identity{GameID: "g", PlayerID: "p"}, ConnOptions{SendBuffer: frames})
		for i := 0; i < frames; i++ {
			c.Enqueue([]byte(fmt.Sprintf("frame %d", i)))
		}
		c.CloseAfterFlush(ClosePolicy, "rate limit exceeded")
		c.Serve(r.Context(), func(*Conn, websocket.MessageType, []byte) {})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	for i := 0; i < frames; i++ {
		_, b, err := client.Read(ctx)
		if err != nil || string(b) != fmt.Sprintf("frame %d", i) {
			t.Fatalf("frame %d: %q, %v; want every queued frame, in order, before the close", i, b, err)
		}
	}
	_, _, err = client.Read(ctx)
	if websocket.CloseStatus(err) != 1008 {
		t.Fatalf("after the frames: %v, want a close with 1008", err)
	}
	<-served
}

func TestCloseGameClosesEveryConnectionOfThatGameOnly(t *testing.T) {
	h := New()
	a1, a2, a3 := detached("game-a", "p1", 4), detached("game-a", "p1", 4), detached("game-a", "p2", 4)
	b1 := detached("game-b", "p1", 4)
	for _, c := range []*Conn{a1, a2, a3, b1} {
		h.Register(c)
	}

	if n := h.CloseGameSuspended("game-a"); n != 3 {
		t.Fatalf("closed %d connections, want 3", n)
	}
	for i, c := range []*Conn{a1, a2, a3} {
		select {
		case <-c.Done():
		default:
			t.Fatalf("connection %d of game-a is still open", i)
		}
		if code, reason := c.CloseInfo(); code != 4403 || reason != "game_suspended" {
			t.Errorf("connection %d closed with %d %q, want 4403 game_suspended", i, code, reason)
		}
	}
	select {
	case <-b1.Done():
		t.Fatal("a connection of another game was closed")
	default:
	}
	if h.CloseGameSuspended("nobody") != 0 {
		t.Fatal("a game without connections has nothing to close")
	}
}

func TestCloseGameUsesTheGivenCode(t *testing.T) {
	h := New()
	c := detached("game-a", "p1", 4)
	h.Register(c)
	h.CloseGame("game-a", websocket.StatusGoingAway, "maintenance")
	if code, reason := c.CloseInfo(); code != websocket.StatusGoingAway || reason != "maintenance" {
		t.Fatalf("closed with %d %q", code, reason)
	}
}

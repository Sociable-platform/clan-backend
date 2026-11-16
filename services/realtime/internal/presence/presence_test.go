package presence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

const (
	game  = "a6c1e2f0-1111-4222-8333-944455556666"
	other = "00000000-0000-0000-0000-000000000002"
	ali   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	vusal = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	leyla = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// timers is a clock the test controls: nothing runs until fire is called.
type timers struct {
	mu   sync.Mutex
	list []*timer
}

type timer struct {
	d       time.Duration
	f       func()
	stopped bool
	fired   bool
}

func (ts *timers) afterFunc(d time.Duration, f func()) func() bool {
	t := &timer{d: d, f: f}
	ts.mu.Lock()
	ts.list = append(ts.list, t)
	ts.mu.Unlock()
	return func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// fire runs every timer that is still waiting, like the clock passing the grace period. A timer that
// was stopped does not run (a stopped real timer never does either).
func (ts *timers) fire() {
	ts.mu.Lock()
	var run []*timer
	for _, t := range ts.list {
		if !t.stopped && !t.fired {
			t.fired = true
			run = append(run, t)
		}
	}
	ts.mu.Unlock()
	for _, t := range run {
		t.f()
	}
}

func (ts *timers) waiting() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	n := 0
	for _, t := range ts.list {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

type fakeConns struct {
	mu     sync.Mutex
	online map[key]bool
}

func (c *fakeConns) set(g, p string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.online == nil {
		c.online = map[key]bool{}
	}
	c.online[key{g, p}] = on
}

func (c *fakeConns) Online(g, p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.online[key{g, p}]
}

type fakeFriends struct {
	mu      sync.Mutex
	friends map[key][]string
	err     error
	asked   []key
	onAsk   func() // runs during the lookup, before it answers
}

func (f *fakeFriends) FriendsOf(_ context.Context, g, p string) ([]string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, key{g, p})
	friends, err, onAsk := f.friends[key{g, p}], f.err, f.onAsk
	f.mu.Unlock()
	if onAsk != nil {
		onAsk()
	}
	return friends, err
}

type sent struct {
	gameID string
	to     []string
	frame  []byte
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
}

func (s *fakeSender) SendToPlayers(g string, to []string, frame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sent{g, slices.Clone(to), frame})
}

func (s *fakeSender) all() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

type env struct {
	tr      *Tracker
	clock   *timers
	conns   *fakeConns
	friends *fakeFriends
	out     *fakeSender
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		clock:   &timers{},
		conns:   &fakeConns{},
		friends: &fakeFriends{friends: map[key][]string{{game, ali}: {vusal, leyla}}},
		out:     &fakeSender{},
		now:     time.Date(2026, 11, 10, 12, 0, 0, 123_000_000, time.UTC),
	}
	e.tr = New(e.conns, e.friends, e.out, Options{AfterFunc: e.clock.afterFunc, Now: func() time.Time { return e.now }})
	return e
}

// closeLast is what the WebSocket handler does when the player's last tab closes.
func (e *env) closeLast(g, p string) {
	e.conns.set(g, p, false)
	e.tr.Disconnected(g, p)
}

// open is what the handler does for a new tab: the hub reports whether it is the player's first one.
// It returns what Connected answered: whether the player came online.
func (e *env) open(g, p string) bool {
	first := !e.conns.Online(g, p)
	e.conns.set(g, p, true)
	return e.tr.Connected(g, p, first)
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("frame is not JSON: %v", err)
	}
	return m
}

func TestOfflineIsSentOnlyAfterTheGracePeriod(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.closeLast(game, ali)

	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("friends were told at once: %d frames; they must wait for the grace period", len(got))
	}
	e.clock.fire()

	got := e.out.all()
	if len(got) != 1 {
		t.Fatalf("%d sends, want 1", len(got))
	}
	if got[0].gameID != game || !slices.Equal(got[0].to, []string{vusal, leyla}) {
		t.Errorf("sent to %v in game %s, want the two friends in %s", got[0].to, got[0].gameID, game)
	}
	f := decode(t, got[0].frame)
	if f["type"] != "presence.update" || f["game_id"] != game {
		t.Errorf("frame type %v, game %v; want presence.update in %s", f["type"], f["game_id"], game)
	}
	data, _ := f["data"].(map[string]any)
	if len(data) != 2 || data["playerId"] != ali || data["status"] != "offline" {
		t.Errorf("data = %v, want exactly {playerId: ali, status: offline}", data)
	}
	if f["occurred_at"] != "2026-11-10T12:00:00.123Z" {
		t.Errorf("occurred_at = %v, want the time of the offline decision", f["occurred_at"])
	}
	if f["event_id"] == "" || f["event_id"] == nil {
		t.Error("the frame has no event_id")
	}
}

func TestUsesTheConfiguredGrace(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	if got := e.clock.list[0].d; got != 10*time.Second {
		t.Errorf("grace = %v, want 10s by default", got)
	}
	custom := New(e.conns, e.friends, e.out, Options{AfterFunc: e.clock.afterFunc, Grace: 3 * time.Second})
	custom.Disconnected(game, vusal)
	if got := e.clock.list[1].d; got != 3*time.Second {
		t.Errorf("grace = %v, want 3s", got)
	}
}

// The Done-when of the ticket: refreshing the page does not flash offline to a friend.
func TestARefreshIsNeverOffline(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.closeLast(game, ali) // the old page closes ...
	e.open(game, ali)      // ... and the new one opens before the grace period ends
	e.clock.fire()

	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("friends were told %d frames after a refresh, want none", len(got))
	}
	if e.clock.waiting() != 0 {
		t.Error("the grace timer is still running after the player came back")
	}
	if !e.tr.Online(game, ali) {
		t.Error("the player is not online after the refresh")
	}
}

func TestPlayerIsOnlineDuringTheGracePeriodButNotAfter(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.closeLast(game, ali)
	if !e.tr.Online(game, ali) {
		t.Error("offline right after the last tab closed, want online until the grace period ends")
	}
	e.clock.fire()
	if e.tr.Online(game, ali) {
		t.Error("still online after the grace period")
	}
}

func TestOnlineIsPerGame(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	if e.tr.Online(other, ali) {
		t.Error("a grace period in one game made the same player ID online in another game")
	}
	e.conns.set(other, vusal, true)
	if !e.tr.Online(other, vusal) || e.tr.Online(game, vusal) {
		t.Error("a connection in one game counts in another")
	}
}

func TestTheGameOfTheTimerDecidesWhoIsTold(t *testing.T) {
	e := newEnv(t)
	e.friends.friends[key{other, ali}] = []string{leyla}
	e.closeLast(other, ali)
	e.clock.fire()
	got := e.out.all()
	if len(got) != 1 || got[0].gameID != other || !slices.Equal(got[0].to, []string{leyla}) {
		t.Fatalf("sent %+v, want the friends of ali in the other game only", got)
	}
	if len(e.friends.asked) != 1 || e.friends.asked[0] != (key{other, ali}) {
		t.Errorf("friends were looked up as %v, want the game of the connection", e.friends.asked)
	}
}

func TestNothingIsSentWhenTheHubSaysTheyAreBack(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	e.conns.set(game, ali, true) // registered, but Connected did not run yet
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent to the friends of a player who is connected", len(got))
	}
	if e.tr.Online(game, ali) != true {
		t.Error("a connected player is not online")
	}
}

func TestPlayerWhoComesBackWhileFriendsAreLookedUpIsNotAnnouncedOffline(t *testing.T) {
	e := newEnv(t)
	e.friends.onAsk = func() { e.tr.Connected(game, ali, true) } // the player is back during the lookup
	e.closeLast(game, ali)
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent although the player came back", len(got))
	}
}

func TestNoFriendsNothingToSend(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, vusal) // vusal has no friends in the fake
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent for a player without friends", len(got))
	}
	if e.tr.Online(game, vusal) {
		t.Error("still online after the grace period")
	}
}

func TestFailedFriendLookupSendsNothingAndEndsTheGracePeriod(t *testing.T) {
	e := newEnv(t)
	e.friends.err = errors.New("database down")
	e.closeLast(game, ali)
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent without knowing the friends", len(got))
	}
	if e.tr.Online(game, ali) {
		t.Error("a player whose lookup failed stays online for ever")
	}
}

func TestSecondDisconnectReplacesTheTimer(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	e.closeLast(game, ali)
	if e.clock.waiting() != 1 {
		t.Fatalf("%d timers waiting, want 1", e.clock.waiting())
	}
	e.clock.fire()
	if got := e.out.all(); len(got) != 1 {
		t.Fatalf("%d sends, want 1", len(got))
	}
}

func TestAStaleTimerDoesNotEndANewerGracePeriod(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	old := e.clock.list[0]
	e.open(game, ali)
	e.closeLast(game, ali) // a second, newer grace period
	old.f()                // the first timer fires late (it was stopped, but a callback may already be running)
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("a stale timer announced the player as offline: %d frames", len(got))
	}
	if !e.tr.Online(game, ali) {
		t.Error("the newer grace period was ended by the stale timer")
	}
	if len(e.friends.asked) != 0 {
		t.Errorf("a stale timer looked up the friends (%d lookups); it should do nothing", len(e.friends.asked))
	}
}

func TestCloseStopsEverythingAndIgnoresLaterDisconnects(t *testing.T) {
	e := newEnv(t)
	e.closeLast(game, ali)
	e.tr.Close()
	if e.clock.waiting() != 0 {
		t.Error("a timer is still waiting after Close")
	}
	e.closeLast(game, vusal)
	if e.clock.waiting() != 0 {
		t.Error("Disconnected started a timer after Close")
	}
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent after Close", len(got))
	}
}

func TestOnlineFriendsListsConnectedAndGracePeriodFriends(t *testing.T) {
	e := newEnv(t)
	e.open(game, vusal)
	e.open(game, leyla)
	e.closeLast(game, leyla) // in the grace period: still online

	got, err := e.tr.OnlineFriends(context.Background(), game, ali)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{vusal, leyla}) {
		t.Errorf("online friends = %v, want both", got)
	}
	e.clock.fire()
	got, _ = e.tr.OnlineFriends(context.Background(), game, ali)
	if !slices.Equal(got, []string{vusal}) {
		t.Errorf("after the grace period = %v, want only the one who is connected", got)
	}
}

func TestOnlineFriendsIsAnEmptyListNotNil(t *testing.T) {
	e := newEnv(t)
	got, err := e.tr.OnlineFriends(context.Background(), game, ali)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v; want an empty, non-nil list", got, err)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Errorf("JSON = %s, want []", b)
	}
}

func TestOnlineFriendsDoesNotListAPlayerWhoIsNotAFriend(t *testing.T) {
	e := newEnv(t)
	e.open(game, "dddddddd-dddd-4ddd-8ddd-dddddddddddd") // online, but not a friend of ali
	got, _ := e.tr.OnlineFriends(context.Background(), game, ali)
	if len(got) != 0 {
		t.Errorf("listed %v, want only friends", got)
	}
}

func TestOnlineFriendsReturnsTheLookupError(t *testing.T) {
	e := newEnv(t)
	e.friends.err = errors.New("database down")
	if _, err := e.tr.OnlineFriends(context.Background(), game, ali); err == nil {
		t.Fatal("no error")
	}
}

// Run with -race: tabs opening and closing while timers fire.
func TestConcurrentUse(t *testing.T) {
	conns, out := &fakeConns{}, &fakeSender{}
	friends := &fakeFriends{friends: map[key][]string{{game, ali}: {vusal}}}
	tr := New(conns, friends, out, Options{Grace: time.Millisecond})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				conns.set(game, ali, true)
				if tr.Connected(game, ali, true) {
					tr.AnnounceOnline(game, ali, []string{vusal})
				}
				conns.set(game, ali, false)
				tr.Disconnected(game, ali)
				tr.Online(game, ali)
				_, _ = tr.OnlineFriends(context.Background(), game, vusal)
			}
		})
	}
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	tr.Close()
}

// TEAM-274: a player's first connection makes them come online, and their online friends are told.
func TestFirstConnectionIsAnnouncedOnline(t *testing.T) {
	e := newEnv(t)
	if !e.open(game, ali) {
		t.Fatal("Connected = false for the first connection of an offline player, want true")
	}
	e.tr.AnnounceOnline(game, ali, []string{vusal})

	got := e.out.all()
	if len(got) != 1 || got[0].gameID != game || !slices.Equal(got[0].to, []string{vusal}) {
		t.Fatalf("sent %+v, want one frame to vusal in %s", got, game)
	}
	f := decode(t, got[0].frame)
	data, _ := f["data"].(map[string]any)
	if f["type"] != "presence.update" || len(data) != 2 || data["playerId"] != ali || data["status"] != "online" {
		t.Errorf("frame %v, want presence.update with exactly {playerId: ali, status: online}", f)
	}
	if f["occurred_at"] != "2026-11-10T12:00:00.123Z" {
		t.Errorf("occurred_at = %v, want the time the player came online", f["occurred_at"])
	}
}

func TestComingOnlineWithoutOnlineFriendsSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.tr.AnnounceOnline(game, ali, nil)
	e.tr.AnnounceOnline(game, ali, []string{})
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames sent with nobody to tell", len(got))
	}
}

// Several tabs: only the first one in the hub makes the player come online.
func TestAnotherTabIsNotNews(t *testing.T) {
	e := newEnv(t)
	if !e.open(game, ali) {
		t.Fatal("the first tab did not make the player come online")
	}
	if e.open(game, ali) {
		t.Fatal("a second tab made the player come online again")
	}
}

// A page refresh: the player comes back within the grace period, so friends hear neither
// "offline" nor "online".
func TestComingBackWithinTheGracePeriodIsNotAnnounced(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.closeLast(game, ali)
	if e.open(game, ali) {
		t.Fatal("Connected = true for a player who came back within the grace period, want false")
	}
	e.clock.fire()
	if got := e.out.all(); len(got) != 0 {
		t.Fatalf("%d frames after a refresh, want none", len(got))
	}
}

// After the grace period ended (friends were told "offline"), coming back is coming online again.
func TestComingBackAfterGoingOfflineIsAnnounced(t *testing.T) {
	e := newEnv(t)
	e.open(game, ali)
	e.closeLast(game, ali)
	e.clock.fire() // "offline" to the friends
	if !e.open(game, ali) {
		t.Fatal("Connected = false for a player who was offline, want true")
	}
}

// While the service shuts down nobody comes online.
func TestNobodyComesOnlineAfterClose(t *testing.T) {
	e := newEnv(t)
	e.tr.Close()
	if e.open(game, ali) {
		t.Fatal("Connected = true after Close, want false")
	}
}

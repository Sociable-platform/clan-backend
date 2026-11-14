package events

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an outbox in memory. Rows are kept in id order; finish applies what the relay reports.
type fakeStore struct {
	mu       sync.Mutex
	rows     []*fakeRow
	claimErr error // returned by claim while set
	claims   int

	finishCtxErr  error       // ctx.Err() seen by the last finish
	deletedBefore []time.Time // cutoffs passed to deleteSent
}

type fakeRow struct {
	pendingRow
	sent      bool
	attempts  int
	lastError string
}

func newFakeStore(events ...Event) *fakeStore {
	s := &fakeStore{}
	for i, e := range events {
		s.rows = append(s.rows, &fakeRow{pendingRow: pendingRow{id: int64(i + 1), event: e}})
	}
	return s
}

func (s *fakeStore) claim(_ context.Context, limit int) (outboxClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	var out []pendingRow
	for _, r := range s.rows {
		if !r.sent && len(out) < limit {
			out = append(out, r.pendingRow)
		}
	}
	return &fakeClaim{store: s, pending: out}, nil
}

func (s *fakeStore) deleteSent(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedBefore = append(s.deletedBefore, before)
	return 0, nil
}

func (s *fakeStore) row(id int64) fakeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id-1]
}

func (s *fakeStore) unsent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if !r.sent {
			n++
		}
	}
	return n
}

type fakeClaim struct {
	store    *fakeStore
	pending  []pendingRow
	finished bool
}

func (c *fakeClaim) rows() []pendingRow { return c.pending }

func (c *fakeClaim) finish(ctx context.Context, sent []int64, failed *failedRow) error {
	if c.finished {
		panic("finish called twice")
	}
	c.finished = true
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCtxErr = ctx.Err()
	for _, id := range sent {
		s.rows[id-1].sent = true
	}
	if failed != nil {
		s.rows[failed.id-1].attempts++
		s.rows[failed.id-1].lastError = failed.reason
	}
	return nil
}

// flakyPublisher fails while down is true, and records what it sent.
type flakyPublisher struct {
	mu     sync.Mutex
	down   bool
	failOn string // an event ID that always fails
	sent   []string
	onSend func(Event)
}

func (p *flakyPublisher) Publish(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down || e.ID == p.failOn {
		return ErrUnavailable
	}
	p.sent = append(p.sent, e.ID)
	if p.onSend != nil {
		p.onSend(e)
	}
	return nil
}

func (p *flakyPublisher) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

func (p *flakyPublisher) sentIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sent)
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testEvents(ids ...string) []Event {
	out := make([]Event, len(ids))
	for i, id := range ids {
		out[i] = Event{ID: id, Type: "friend.accepted", GameID: "g1", OccurredAt: time.Unix(0, 0).UTC()}
	}
	return out
}

func mustRelay(t *testing.T, store outboxStore, pub Publisher, cfg RelayConfig) *Relay {
	t.Helper()
	r, err := newRelay(store, pub, cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSendBatchSendsOldestFirstAndMarksSent(t *testing.T) {
	store := newFakeStore(testEvents("e1", "e2", "e3")...)
	pub := &flakyPublisher{}
	r := mustRelay(t, store, pub, RelayConfig{})

	sent, more, err := r.sendBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 3 || more {
		t.Errorf("sent, more = %d, %v; want 3, false", sent, more)
	}
	if got := pub.sentIDs(); !slices.Equal(got, []string{"e1", "e2", "e3"}) {
		t.Errorf("published %v, want e1 e2 e3 in order", got)
	}
	if n := store.unsent(); n != 0 {
		t.Errorf("%d rows still unsent", n)
	}
}

func TestSendBatchStopsAtTheFirstFailureToKeepTheOrder(t *testing.T) {
	store := newFakeStore(testEvents("e1", "e2", "e3")...)
	pub := &flakyPublisher{failOn: "e2"}
	r := mustRelay(t, store, pub, RelayConfig{})

	sent, _, err := r.sendBatch(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if sent != 1 || !slices.Equal(pub.sentIDs(), []string{"e1"}) {
		t.Errorf("sent %d %v, want only e1", sent, pub.sentIDs())
	}
	if !store.row(1).sent {
		t.Error("e1 was confirmed but not marked sent")
	}
	failed := store.row(2)
	if failed.sent || failed.attempts != 1 || !strings.Contains(failed.lastError, "broker unavailable") {
		t.Errorf("e2 = sent %v, attempts %d, last_error %q; want unsent, 1 attempt and the error", failed.sent, failed.attempts, failed.lastError)
	}
	if store.row(3).sent || store.row(3).attempts != 0 {
		t.Error("e3 was touched although e2 before it failed")
	}

	// Once e2 goes through, e2 and e3 follow in order.
	pub.failOn = ""
	if _, _, err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pub.sentIDs(); !slices.Equal(got, []string{"e1", "e2", "e3"}) {
		t.Errorf("published %v, want e1 e2 e3", got)
	}
}

func TestSendBatchReportsAFullBatch(t *testing.T) {
	store := newFakeStore(testEvents("e1", "e2", "e3")...)
	pub := &flakyPublisher{}
	r := mustRelay(t, store, pub, RelayConfig{})
	r.batchSize = 2

	sent, more, err := r.sendBatch(context.Background())
	if err != nil || sent != 2 || !more {
		t.Fatalf("first batch: sent %d, more %v, err %v; want 2, true, nil", sent, more, err)
	}
	sent, more, err = r.sendBatch(context.Background())
	if err != nil || sent != 1 || more {
		t.Fatalf("second batch: sent %d, more %v, err %v; want 1, false, nil", sent, more, err)
	}
}

func TestSendBatchRecordsConfirmedRowsDuringShutdown(t *testing.T) {
	store := newFakeStore(testEvents("e1", "e2")...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pub := &flakyPublisher{onSend: func(Event) { cancel() }} // shutdown right after the first confirm
	r := mustRelay(t, store, pub, RelayConfig{})

	if _, _, err := r.sendBatch(ctx); err == nil {
		t.Fatal("want the error of the cut-short send")
	}
	if store.finishCtxErr != nil {
		t.Errorf("finish ran with an ended context (%v): the confirmed row could not be recorded", store.finishCtxErr)
	}
	if !store.row(1).sent {
		t.Error("e1 was confirmed before shutdown but not marked sent")
	}
	if e2 := store.row(2); e2.sent || e2.attempts != 0 {
		t.Errorf("e2 = sent %v, attempts %d; a send cut short by shutdown is not a failed attempt", e2.sent, e2.attempts)
	}
}

func TestSendBatchReturnsAClaimError(t *testing.T) {
	store := newFakeStore(testEvents("e1")...)
	store.claimErr = errors.New("database down")
	pub := &flakyPublisher{}
	r := mustRelay(t, store, pub, RelayConfig{})

	if _, _, err := r.sendBatch(context.Background()); err == nil || !strings.Contains(err.Error(), "database down") {
		t.Fatalf("err = %v, want the claim error", err)
	}
	if len(pub.sentIDs()) != 0 {
		t.Error("published without a claim")
	}
}

func TestRunSendsOnceTheBrokerIsBackAndStopsWithTheContext(t *testing.T) {
	store := newFakeStore(testEvents("e1", "e2")...)
	pub := &flakyPublisher{down: true}
	r := mustRelay(t, store, pub, RelayConfig{Interval: time.Millisecond})
	r.maxBackoff = 2 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	waitFor(t, "a few failed attempts", func() bool { return store.row(1).attempts >= 3 })
	pub.setDown(false)
	waitFor(t, "every row to be sent", func() bool { return store.unsent() == 0 })
	if got := pub.sentIDs(); !slices.Equal(got, []string{"e1", "e2"}) {
		t.Errorf("published %v, want e1 e2", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}

func TestRunStopsDuringALongWait(t *testing.T) {
	r := mustRelay(t, newFakeStore(), &flakyPublisher{}, RelayConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	time.Sleep(10 * time.Millisecond) // let it reach the hour-long wait
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}

func TestRunDeletesSentRowsPastTheRetention(t *testing.T) {
	store := newFakeStore()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	r := mustRelay(t, store, &flakyPublisher{}, RelayConfig{Interval: time.Millisecond, Retention: 48 * time.Hour})
	r.now = func() time.Time { return now }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	waitFor(t, "a few batches", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claims >= 5
	})
	cancel()
	<-done

	// Once at start, then not again within the hour (the clock does not move).
	if want := []time.Time{now.Add(-48 * time.Hour)}; !slices.Equal(store.deletedBefore, want) {
		t.Errorf("deleteSent cutoffs = %v, want %v", store.deletedBefore, want)
	}
}

func TestNewRelayDefaultsAndRejectsNegativeDurations(t *testing.T) {
	r := mustRelay(t, newFakeStore(), Noop{}, RelayConfig{})
	if r.cfg.Interval != DefaultRelayInterval || r.cfg.Retention != DefaultRelayRetention {
		t.Errorf("config = %+v, want the defaults", r.cfg)
	}

	for _, cfg := range []RelayConfig{{Interval: -time.Second}, {Retention: -time.Hour}} {
		if _, err := newRelay(newFakeStore(), Noop{}, cfg, discardLog()); err == nil {
			t.Errorf("newRelay(%+v) succeeded, want an error", cfg)
		}
	}
	if _, err := NewRelay(nil, Noop{}, RelayConfig{}, discardLog()); err == nil {
		t.Error("NewRelay without a pool succeeded, want an error")
	}
}

func TestEnqueueRejectsAnEventWithoutIDOrType(t *testing.T) {
	for _, e := range []Event{{Type: "friend.accepted"}, {ID: NewID()}} {
		// The event is checked before the transaction is used, so no database is needed.
		if err := Enqueue(context.Background(), nil, e); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("Enqueue(%+v) = %v, want ErrInvalidEvent", e, err)
		}
	}
}

func TestTruncateKeepsWholeCharacters(t *testing.T) {
	if got := truncate("ağac", 2); got != "ağ" {
		t.Errorf("truncate = %q, want %q", got, "ağ")
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q", got)
	}
}

// The outbox tables of platform and social must stay the same, because one relay serves both.
func TestOutboxMigrationsAreTheSameInEveryService(t *testing.T) {
	for _, suffix := range []string{".up.sql", ".down.sql"} {
		social, err := os.ReadFile("../../services/social/migrations/000008_create_outbox" + suffix)
		if err != nil {
			t.Fatal(err)
		}
		platform, err := os.ReadFile("../../services/platform/migrations/000010_create_outbox" + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(social, platform) {
			t.Errorf("the social and platform outbox migrations (%s) differ", suffix)
		}
	}
}

// waitFor polls cond until it is true, or fails the test after two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// fakeRules answers the same rules for every game and records which games were asked.
type fakeRules struct {
	mu    sync.Mutex
	rules map[string]gameconfig.ChatRules // per game; a game that is not listed has the defaults
	asked []string
}

func (f *fakeRules) ChatRules(_ context.Context, gameID string) gameconfig.ChatRules {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, gameID)
	if r, ok := f.rules[gameID]; ok {
		return r
	}
	return gameconfig.DefaultChatRules
}

func rulesWith(change func(*gameconfig.ChatRules)) *fakeRules {
	r := gameconfig.DefaultChatRules
	change(&r)
	return &fakeRules{rules: map[string]gameconfig.ChatRules{game: r}}
}

// clock is a time the test moves by hand.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time      { return c.now }
func (c *clock) add(d time.Duration) { c.now = c.now.Add(d) }
func newClock() *clock               { return &clock{now: testNow} }
func (c *clock) option() Option      { return WithClock(c.Now) }
func chatWith(store *memStore, r Rules, c *clock) *Service {
	return New(store, WithIDs(fixedIDs{id: "0193a6c0-1111-7222-8333-944455556666", at: testNow}), WithRules(r), c.option())
}

func sendErr(t *testing.T, err error) *SendError {
	t.Helper()
	var se *SendError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a SendError", err)
	}
	return se
}

var (
	dmRoom   = &repository.ConversationInfo{Kind: "dm", Members: []string{ali, rashad}}
	clanRoom = &repository.ConversationInfo{Kind: "clan", Members: []string{ali, rashad, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}
)

func TestFeatureEnabled(t *testing.T) {
	tests := []struct {
		name     string
		conv     *repository.ConversationInfo
		change   func(*gameconfig.ChatRules)
		wantCode string
		wantMsg  string
	}{
		{"chat on, DM", dmRoom, func(*gameconfig.ChatRules) {}, "", ""},
		{"chat on, clan room", clanRoom, func(*gameconfig.ChatRules) {}, "", ""},
		{"chat off, DM", dmRoom, func(r *gameconfig.ChatRules) { r.Enabled = false }, CodeFeatureDisabled, "Chat is turned off"},
		{"chat off, clan room", clanRoom, func(r *gameconfig.ChatRules) { r.Enabled = false }, CodeFeatureDisabled, "Chat is turned off"},
		{"direct messages off, DM", dmRoom, func(r *gameconfig.ChatRules) { r.DirectMessages = false }, CodeFeatureDisabled, "Direct messages are turned off"},
		{"direct messages off, clan room still works", clanRoom, func(r *gameconfig.ChatRules) { r.DirectMessages = false }, "", ""},
		{"clan chat off, clan room", clanRoom, func(r *gameconfig.ChatRules) { r.ClanChat = false }, CodeFeatureDisabled, "Clan chat is turned off"},
		{"clan chat off, DM still works", dmRoom, func(r *gameconfig.ChatRules) { r.ClanChat = false }, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{conv: tt.conv, members: []string{ali, rashad}}
			_, err := chatWith(store, rulesWith(tt.change), newClock()).SendMessage(context.Background(), sender, convA, "hi")
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("error = %v, want the message stored", err)
				}
				return
			}
			se := sendErr(t, err)
			if se.Code != tt.wantCode || !strings.Contains(se.Message, tt.wantMsg) {
				t.Errorf("error = %+v, want %s containing %q", se, tt.wantCode, tt.wantMsg)
			}
			if len(store.inserts) != 0 {
				t.Error("stored although the feature is off")
			}
		})
	}
}

func TestBlocked(t *testing.T) {
	tests := []struct {
		name        string
		blocked     map[string]bool
		conv        *repository.ConversationInfo
		wantBlocked bool
	}{
		{"nobody blocked anybody", nil, dmRoom, false},
		{"the sender blocked the other", map[string]bool{ali + "|" + rashad: true}, dmRoom, true},
		{"the other blocked the sender", map[string]bool{rashad + "|" + ali: true}, dmRoom, true},
		{"somebody else is blocked", map[string]bool{rashad + "|cccccccc-cccc-4ccc-8ccc-cccccccccccc": true}, dmRoom, false},
		{"a clan room is not affected by blocks", map[string]bool{ali + "|" + rashad: true}, clanRoom, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{conv: tt.conv, blocked: tt.blocked, members: []string{ali, rashad}}
			_, err := chatWith(store, nil, newClock()).SendMessage(context.Background(), sender, convA, "hi")
			if !tt.wantBlocked {
				if err != nil {
					t.Fatalf("error = %v, want the message stored", err)
				}
				return
			}
			if se := sendErr(t, err); se.Code != CodeBlocked || se.Message == "" {
				t.Fatalf("error = %+v, want blocked", se)
			}
			if len(store.inserts) != 0 {
				t.Error("stored although blocked")
			}
		})
	}
}

func TestMaxLength(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		body   string
		wantOK bool
	}{
		{"exactly the limit", 20, strings.Repeat("a", 20), true},
		{"one over", 20, strings.Repeat("a", 21), false},
		{"emoji count as one each", 20, strings.Repeat("😀", 20), true},
		{"one emoji over", 20, strings.Repeat("😀", 21), false},
		{"non-Latin letters count as one each", 5, "əəəəə", true},
		{"spaces around do not count (trimmed first)", 20, "   " + strings.Repeat("a", 20) + "   ", true},
		{"a limit of 1", 1, "a", true},
		{"a limit of 1, two characters", 1, "ab", false},
		{"the schema's maximum", 1000, strings.Repeat("a", 1000), true},
		{"the schema's maximum, one over", 1000, strings.Repeat("a", 1001), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{members: []string{ali, rashad}}
			r := rulesWith(func(r *gameconfig.ChatRules) { r.MaxMessageLength = tt.limit })
			_, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, tt.body)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			se := sendErr(t, err)
			if se.Code != CodeTooLong || !strings.Contains(se.Message, " "+strconv.Itoa(tt.limit)+" ") {
				t.Errorf("error = %+v, want too_long naming the limit %d", se, tt.limit)
			}
			if len(store.inserts) != 0 {
				t.Error("stored although too long")
			}
		})
	}
}

func TestAGamesLimitAboveTheSchemaIsStillCappedAt1000(t *testing.T) {
	store := &memStore{members: []string{ali}}
	r := rulesWith(func(r *gameconfig.ChatRules) { r.MaxMessageLength = 5000 })
	_, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, strings.Repeat("a", 1001))
	if se := sendErr(t, err); se.Code != CodeTooLong {
		t.Fatalf("error = %+v, want too_long", se)
	}
	r = rulesWith(func(r *gameconfig.ChatRules) { r.MaxMessageLength = 0 }) // not a value Platform can send; must not reject everything
	if _, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, "hi"); err != nil {
		t.Fatalf("a limit of 0 rejected a short message: %v", err)
	}
}

func TestSlowMode(t *testing.T) {
	store := &memStore{members: []string{ali, rashad}}
	clk := newClock()
	svc := chatWith(store, rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 5 }), clk)
	ctx := context.Background()

	if _, err := svc.SendMessage(ctx, sender, convA, "one"); err != nil {
		t.Fatal(err)
	}
	clk.add(1500 * time.Millisecond)
	_, err := svc.SendMessage(ctx, sender, convA, "two")
	se := sendErr(t, err)
	if se.Code != CodeSlowMode || se.RetryAfter != 3500*time.Millisecond || se.Message == "" {
		t.Fatalf("error = %+v, want slow_mode with 3.5 s to wait", se)
	}
	if len(store.inserts) != 1 {
		t.Fatalf("inserts = %d, the refused message must not be stored", len(store.inserts))
	}

	// Another conversation, same player (another tab or room): the same slow mode.
	_, err = svc.SendMessage(ctx, sender, "0193a6b4-7c1e-7d2a-9f10-2b3c4d5e6f71", "three")
	if se := sendErr(t, err); se.Code != CodeSlowMode {
		t.Errorf("a message to another conversation: %+v, want slow_mode too", se)
	}
	// Another player is not held back by it.
	if _, err := svc.SendMessage(ctx, Sender{GameID: game, ID: rashad, Name: "Rashad"}, convA, "hi"); err != nil {
		t.Errorf("another player was held back: %v", err)
	}

	clk.add(3500 * time.Millisecond)
	if _, err := svc.SendMessage(ctx, sender, convA, "four"); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}

func TestSlowModeIsOffWithZeroSeconds(t *testing.T) {
	store := &memStore{members: []string{ali}}
	svc := chatWith(store, rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 0 }), newClock())
	for i := 0; i < 5; i++ {
		if _, err := svc.SendMessage(context.Background(), sender, convA, "spam"); err != nil {
			t.Fatalf("message %d: %v", i+1, err)
		}
	}
}

func TestSlowModeOfOneGameDoesNotHoldBackAnother(t *testing.T) {
	store := &memStore{members: []string{ali}}
	other := "22222222-2222-4222-8222-222222222222"
	r := &fakeRules{rules: map[string]gameconfig.ChatRules{
		game:  {Enabled: true, DirectMessages: true, ClanChat: true, MaxMessageLength: 1000, SlowModeSeconds: 30},
		other: gameconfig.DefaultChatRules,
	}}
	svc := chatWith(store, r, newClock())
	if _, err := svc.SendMessage(context.Background(), sender, convA, "hi"); err != nil {
		t.Fatal(err)
	}
	// The same player ID in another game, with no slow mode there.
	if _, err := svc.SendMessage(context.Background(), Sender{GameID: other, ID: ali, Name: "Ali"}, convA, "hi"); err != nil {
		t.Errorf("slow mode of game A applied in game B: %v", err)
	}
	if _, err := svc.SendMessage(context.Background(), Sender{GameID: other, ID: ali, Name: "Ali"}, convA, "hi again"); err != nil {
		t.Errorf("game B has no slow mode but refused: %v", err)
	}
}

func TestMessagesThatAreRefusedDoNotStartTheSlowMode(t *testing.T) {
	store := &memStore{members: []string{ali, rashad}}
	r := rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 10; r.MaxMessageLength = 20 })
	svc := chatWith(store, r, newClock())
	ctx := context.Background()
	for name, body := range map[string]string{"empty": " ", "too long": strings.Repeat("a", 21)} {
		if _, err := svc.SendMessage(ctx, sender, convA, body); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	store.blocked = map[string]bool{rashad + "|" + ali: true}
	if _, err := svc.SendMessage(ctx, sender, convA, "hi"); sendErr(t, err).Code != CodeBlocked {
		t.Fatal("not blocked")
	}
	store.blocked = nil
	if _, err := svc.SendMessage(ctx, sender, convA, "hi"); err != nil {
		t.Errorf("the refused messages started the slow mode: %v", err)
	}
}

func TestAMessageThatCouldNotBeStoredDoesNotCostTheTurn(t *testing.T) {
	store := &memStore{members: []string{ali}, insertErr: errors.New("connection refused")}
	svc := chatWith(store, rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 10 }), newClock())
	if _, err := svc.SendMessage(context.Background(), sender, convA, "hi"); err == nil {
		t.Fatal("no error")
	}
	store.insertErr = nil
	if _, err := svc.SendMessage(context.Background(), sender, convA, "hi"); err != nil {
		t.Errorf("a failed store started the slow mode: %v", err)
	}
	// and a sender who was removed a moment ago (the insert's own membership check)
	store.insertErr = repository.ErrNotMember
	svc2 := chatWith(store, rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 10 }), newClock())
	if se := sendErr(t, mustFail(t, svc2)); se.Code != CodeNotMember {
		t.Errorf("code = %s, want not_member", se.Code)
	}
	store.insertErr = nil
	if _, err := svc2.SendMessage(context.Background(), sender, convA, "hi"); err != nil {
		t.Errorf("a rejected insert started the slow mode: %v", err)
	}
}

func mustFail(t *testing.T, svc *Service) error {
	t.Helper()
	_, err := svc.SendMessage(context.Background(), sender, convA, "hi")
	if err == nil {
		t.Fatal("no error")
	}
	return err
}

// Two tabs of one player send at the same moment: only one message gets through (run with -race).
func TestSlowModeTwoTabsAtOnce(t *testing.T) {
	store := &memStore{members: []string{ali}}
	svc := chatWith(store, rulesWith(func(r *gameconfig.ChatRules) { r.SlowModeSeconds = 30 }), newClock())
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, slow := 0, 0
	for range 20 {
		wg.Go(func() {
			_, err := svc.SendMessage(context.Background(), sender, convA, "hi")
			mu.Lock()
			defer mu.Unlock()
			var se *SendError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &se) && se.Code == CodeSlowMode:
				slow++
			}
		})
	}
	wg.Wait()
	if ok != 1 || slow != 19 {
		t.Fatalf("%d stored and %d held back, want 1 and 19", ok, slow)
	}
}

func TestProfanityFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter bool
		body   string
		want   string
	}{
		{"filter on: the word becomes ***", true, "you are a bitch", "you are a ***"},
		{"filter on: lower case match", true, "SHIT happens", "*** happens"},
		{"filter on: whole words only", true, "a classy assassin", "a classy assassin"},
		{"filter on: punctuation is kept", true, "what the fuck!?", "what the ***!?"},
		{"filter on: nothing to replace", true, "good game", "good game"},
		{"filter off: untouched", false, "you are a bitch", "you are a bitch"},
		{"filter on: trimmed first", true, "  shit  ", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{members: []string{ali}}
			r := rulesWith(func(r *gameconfig.ChatRules) { r.ProfanityFilter = tt.filter })
			sent, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if sent.Message.Body != tt.want || store.inserts[0].msg.Body != tt.want {
				t.Errorf("returned %q, stored %q, want %q in both", sent.Message.Body, store.inserts[0].msg.Body, tt.want)
			}
		})
	}
}

func TestTheLengthIsCountedBeforeTheFilter(t *testing.T) {
	// "fuck" is 4 characters and becomes 3: the limit is about what the player typed.
	store := &memStore{members: []string{ali}}
	r := rulesWith(func(r *gameconfig.ChatRules) { r.ProfanityFilter = true; r.MaxMessageLength = 20 })
	_, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, "fuck fuck fuck fuck fuck")
	if se := sendErr(t, err); se.Code != CodeTooLong {
		t.Fatalf("error = %+v, want too_long", se)
	}
}

// The order of the rules: when two fail at once, the one that comes first in the ticket answers.
func TestTheRulesRunInOrder(t *testing.T) {
	long := strings.Repeat("a", 30)
	all := func(r *gameconfig.ChatRules) { r.Enabled = false; r.MaxMessageLength = 20; r.SlowModeSeconds = 10 }
	tests := []struct {
		name    string
		store   *memStore
		change  func(*gameconfig.ChatRules)
		prepare func(*Service)
		body    string
		want    string
	}{
		{"empty comes before everything", &memStore{convErr: repository.ErrNotMember}, all, nil, "  ", CodeEmpty},
		{"not a member comes before feature_disabled", &memStore{convErr: repository.ErrNotMember}, all, nil, long, CodeNotMember},
		{"feature_disabled comes before blocked", &memStore{conv: dmRoom, blocked: map[string]bool{ali + "|" + rashad: true}}, all, nil, long, CodeFeatureDisabled},
		{"blocked comes before too_long", &memStore{conv: dmRoom, blocked: map[string]bool{ali + "|" + rashad: true}}, func(r *gameconfig.ChatRules) { r.MaxMessageLength = 20 }, nil, long, CodeBlocked},
		{"too_long comes before slow_mode", &memStore{conv: dmRoom, members: []string{ali}}, func(r *gameconfig.ChatRules) { r.MaxMessageLength = 20; r.SlowModeSeconds = 10 },
			func(s *Service) { _, _ = s.SendMessage(context.Background(), sender, convA, "first") }, long, CodeTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := chatWith(tt.store, rulesWith(tt.change), newClock())
			if tt.prepare != nil {
				tt.prepare(svc)
			}
			before := len(tt.store.inserts)
			_, err := svc.SendMessage(context.Background(), sender, convA, tt.body)
			if got := sendErr(t, err).Code; got != tt.want {
				t.Errorf("code = %s, want %s", got, tt.want)
			}
			if len(tt.store.inserts) != before {
				t.Error("stored although refused")
			}
		})
	}
}

func TestRulesAreAskedForTheGameOfTheSender(t *testing.T) {
	store := &memStore{members: []string{ali}}
	r := &fakeRules{}
	if _, err := chatWith(store, r, newClock()).SendMessage(context.Background(), sender, convA, "hi"); err != nil {
		t.Fatal(err)
	}
	if len(r.asked) != 1 || r.asked[0] != game {
		t.Errorf("rules asked for %v, want the game of the sender", r.asked)
	}
}

func TestWithoutRulesTheDefaultsApply(t *testing.T) {
	store := &memStore{members: []string{ali}}
	svc := New(store, WithIDs(fixedIDs{id: "0193a6c0-1111-7222-8333-944455556666", at: testNow}))
	sent, err := svc.SendMessage(context.Background(), sender, convA, "damn shit")
	if err != nil || sent.Message.Body != "damn shit" {
		t.Fatalf("got %+v, %v, want the message untouched (no filter by default)", sent, err)
	}
}

func TestStoreFailuresOnTheWayAreOurErrors(t *testing.T) {
	errDB := errors.New("connection refused")
	tests := map[string]*memStore{
		"the conversation cannot be loaded": {convErr: errDB},
		"the block cannot be checked":       {conv: dmRoom, blockErr: errDB},
	}
	for name, store := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := chatWith(store, nil, newClock()).SendMessage(context.Background(), sender, convA, "hi")
			var se *SendError
			if errors.As(err, &se) || !errors.Is(err, errDB) {
				t.Fatalf("error = %v, want the database error and not a SendError", err)
			}
			if len(store.inserts) != 0 {
				t.Error("stored although a check failed: it must fail closed")
			}
		})
	}
}

func TestNotAMemberAtTheConversationLookup(t *testing.T) {
	store := &memStore{convErr: repository.ErrNotMember}
	_, err := chatWith(store, nil, newClock()).SendMessage(context.Background(), sender, convA, "hi")
	if se := sendErr(t, err); se.Code != CodeNotMember {
		t.Fatalf("code = %s, want not_member", se.Code)
	}
}

func TestPlayerBlockedAndUnblocked(t *testing.T) {
	store := &memStore{}
	svc := New(store)
	ctx := context.Background()
	up := strings.ToUpper(rashad)
	if pushes, err := svc.PlayerBlocked(ctx, game, ali, up); err != nil || len(pushes) != 0 {
		t.Fatalf("PlayerBlocked: %v, %d pushes (blocking is private: nothing is sent)", err, len(pushes))
	}
	if pushes, err := svc.PlayerUnblocked(ctx, game, ali, rashad); err != nil || len(pushes) != 0 {
		t.Fatalf("PlayerUnblocked: %v, %d pushes", err, len(pushes))
	}
	want := []string{"add " + game + "|" + ali + "|" + rashad, "remove " + game + "|" + ali + "|" + rashad}
	if len(store.blockOps) != 2 || store.blockOps[0] != want[0] || store.blockOps[1] != want[1] {
		t.Errorf("store calls = %v, want %v (IDs in lower case)", store.blockOps, want)
	}
}

func TestBlockEventsRejectBadInputWithoutTouchingTheDatabase(t *testing.T) {
	tests := map[string][3]string{
		"game is not a uuid":    {"g", ali, rashad},
		"blocker is not a uuid": {game, "u_1", rashad},
		"blocked is not a uuid": {game, ali, ""},
		"blocking oneself":      {game, ali, ali},
	}
	for name, a := range tests {
		for op, call := range map[string]func(*Service) error{
			"blocked": func(s *Service) error { _, err := s.PlayerBlocked(context.Background(), a[0], a[1], a[2]); return err },
			"unblocked": func(s *Service) error {
				_, err := s.PlayerUnblocked(context.Background(), a[0], a[1], a[2])
				return err
			},
		} {
			t.Run(op+": "+name, func(t *testing.T) {
				store := &memStore{}
				if err := call(New(store)); !errors.Is(err, ErrInvalidEvent) {
					t.Errorf("error = %v, want ErrInvalidEvent", err)
				}
				if len(store.blockOps) != 0 {
					t.Error("touched the database")
				}
			})
		}
	}
	errDB := errors.New("db down")
	store := &memStore{blockErr: errDB}
	if _, err := New(store).PlayerBlocked(context.Background(), game, ali, rashad); !errors.Is(err, errDB) || errors.Is(err, ErrInvalidEvent) {
		t.Errorf("a database failure must be a retryable error, got %v", err)
	}
}

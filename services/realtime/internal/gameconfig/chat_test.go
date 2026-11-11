package gameconfig

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseChatRules(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want ChatRules
	}{
		{"every field", `{"chat":{"enabled":true,"directMessages":false,"clanChat":true,"maxMessageLength":200,"slowModeSeconds":5,"profanityFilter":true,"layout":"log","historyLimit":20}}`,
			ChatRules{Enabled: true, DirectMessages: false, ClanChat: true, MaxMessageLength: 200, SlowModeSeconds: 5, ProfanityFilter: true}},
		{"chat turned off", `{"chat":{"enabled":false,"directMessages":true,"clanChat":true}}`,
			ChatRules{Enabled: false, DirectMessages: true, ClanChat: true, MaxMessageLength: 1000}},
		{"clan chat off", `{"chat":{"clanChat":false}}`,
			ChatRules{Enabled: true, DirectMessages: true, ClanChat: false, MaxMessageLength: 1000}},
		{"no chat section: the defaults", `{"theme":{}}`, DefaultChatRules},
		{"chat is null: the defaults", `{"chat":null}`, DefaultChatRules},
		{"empty document", `{}`, DefaultChatRules},
		{"not JSON: the defaults", `nope`, DefaultChatRules},
		{"max length below 1 is 1", `{"chat":{"maxMessageLength":0}}`, ChatRules{Enabled: true, DirectMessages: true, ClanChat: true, MaxMessageLength: 1}},
		{"max length above 1000 is 1000", `{"chat":{"maxMessageLength":5000}}`, DefaultChatRules},
		{"negative slow mode is none", `{"chat":{"slowModeSeconds":-4}}`, DefaultChatRules},
		{"slow mode above 30 is 30", `{"chat":{"slowModeSeconds":300}}`, ChatRules{Enabled: true, DirectMessages: true, ClanChat: true, MaxMessageLength: 1000, SlowModeSeconds: 30}},
		{"a field of the wrong type: the defaults", `{"chat":{"maxMessageLength":"long"}}`, DefaultChatRules},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseChatRules([]byte(tt.doc)); got != tt.want {
				t.Errorf("rules = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDefaultChatRulesAllowEverything(t *testing.T) {
	d := DefaultChatRules
	if !d.Enabled || !d.DirectMessages || !d.ClanChat || d.MaxMessageLength != 1000 || d.SlowModeSeconds != 0 || d.ProfanityFilter {
		t.Errorf("defaults = %+v, want chat on, 1000 characters, no slow mode, no filter", d)
	}
}

// chatSource serves one document for every game, and can fail or hang.
type chatSource struct {
	doc  string
	err  error
	hang bool
}

func (s chatSource) Fetch(ctx context.Context, _ string) (Config, error) {
	if s.hang {
		<-ctx.Done()
		return Config{}, ctx.Err()
	}
	if s.err != nil {
		return Config{}, s.err
	}
	return Config{Version: 3, Settings: []byte(s.doc)}, nil
}

func TestCacheChatRules(t *testing.T) {
	doc := `{"chat":{"enabled":true,"directMessages":false,"clanChat":true,"maxMessageLength":50,"slowModeSeconds":4,"profanityFilter":true}}`
	c := NewCache(chatSource{doc: doc}, Options{})
	want := ChatRules{Enabled: true, DirectMessages: false, ClanChat: true, MaxMessageLength: 50, SlowModeSeconds: 4, ProfanityFilter: true}
	if got := c.ChatRules(context.Background(), "g"); got != want {
		t.Errorf("rules = %+v, want %+v", got, want)
	}
}

func TestCacheChatRulesFollowANewPublish(t *testing.T) {
	src := &swapSource{doc: `{"chat":{"slowModeSeconds":4}}`}
	c := NewCache(src, Options{})
	if got := c.ChatRules(context.Background(), "g").SlowModeSeconds; got != 4 {
		t.Fatalf("slow mode = %d, want 4", got)
	}
	src.doc, src.version = `{"chat":{"slowModeSeconds":9}}`, 4
	if _, send, err := c.Published(context.Background(), "g", 4); err != nil || !send {
		t.Fatalf("published: send = %v, err = %v", send, err)
	}
	if got := c.ChatRules(context.Background(), "g").SlowModeSeconds; got != 9 {
		t.Errorf("slow mode after the publish = %d, want 9", got)
	}
}

type swapSource struct {
	doc     string
	version int
}

func (s *swapSource) Fetch(context.Context, string) (Config, error) {
	v := s.version
	if v == 0 {
		v = 3
	}
	return Config{Version: v, Settings: []byte(s.doc)}, nil
}

func TestCacheChatRulesAreTheDefaultsWhenTheConfigIsNotKnown(t *testing.T) {
	for name, src := range map[string]Source{
		"nothing published": chatSource{err: ErrNotPublished},
		"platform is down":  chatSource{err: ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			if got := NewCache(src, Options{}).ChatRules(context.Background(), "g"); got != DefaultChatRules {
				t.Errorf("rules = %+v, want the defaults", got)
			}
		})
	}
}

func TestCacheChatRulesDoNotWaitLongForPlatform(t *testing.T) {
	c := NewCache(chatSource{hang: true}, Options{})
	start := time.Now()
	got := c.ChatRules(context.Background(), "g")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("waited %v, want about %v", d, rulesTimeout)
	}
	if got != DefaultChatRules {
		t.Errorf("rules = %+v, want the defaults", got)
	}
}

func TestCacheChatRulesOfACallerThatGaveUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewCache(chatSource{err: errors.New("x")}, Options{})
	if got := c.ChatRules(ctx, "g"); got != DefaultChatRules {
		t.Errorf("rules = %+v, want the defaults", got)
	}
}

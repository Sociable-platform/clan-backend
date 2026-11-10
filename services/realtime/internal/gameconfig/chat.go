package gameconfig

import (
	"context"
	"encoding/json"
	"time"
)

// ChatRules are the message rules of a game, from the chat section of its published config.
type ChatRules struct {
	Enabled          bool // chat.enabled: false turns all chat off
	DirectMessages   bool // chat.directMessages
	ClanChat         bool // chat.clanChat
	MaxMessageLength int  // chat.maxMessageLength, in characters (runes)
	SlowModeSeconds  int  // chat.slowModeSeconds, 0 = no slow mode
	ProfanityFilter  bool // chat.profanityFilter
}

// Hard limits of the config schema (config.v1.schema.json): a value outside them cannot come from Platform.
const (
	maxLength   = 1000
	maxSlowMode = 30
)

// DefaultChatRules are the rules of a game whose config is not known (nothing published yet, or Platform did
// not answer): everything is allowed, up to 1000 characters, no slow mode, no filter. Chat keeps working while
// Platform is down; the rules the developer chose apply again as soon as the config is known.
var DefaultChatRules = ChatRules{Enabled: true, DirectMessages: true, ClanChat: true, MaxMessageLength: maxLength}

// ParseChatRules reads the chat section of a config document. A missing section, or a missing field,
// has the value of DefaultChatRules, and a number outside the schema's limits is brought inside them.
func ParseChatRules(settings json.RawMessage) ChatRules {
	var doc struct {
		Chat *struct {
			Enabled          *bool `json:"enabled"`
			DirectMessages   *bool `json:"directMessages"`
			ClanChat         *bool `json:"clanChat"`
			MaxMessageLength *int  `json:"maxMessageLength"`
			SlowModeSeconds  *int  `json:"slowModeSeconds"`
			ProfanityFilter  *bool `json:"profanityFilter"`
		} `json:"chat"`
	}
	rules := DefaultChatRules
	if json.Unmarshal(settings, &doc) != nil || doc.Chat == nil {
		return rules
	}
	c := doc.Chat
	if c.Enabled != nil {
		rules.Enabled = *c.Enabled
	}
	if c.DirectMessages != nil {
		rules.DirectMessages = *c.DirectMessages
	}
	if c.ClanChat != nil {
		rules.ClanChat = *c.ClanChat
	}
	if c.MaxMessageLength != nil {
		rules.MaxMessageLength = min(max(*c.MaxMessageLength, 1), maxLength)
	}
	if c.SlowModeSeconds != nil {
		rules.SlowModeSeconds = min(max(*c.SlowModeSeconds, 0), maxSlowMode)
	}
	if c.ProfanityFilter != nil {
		rules.ProfanityFilter = *c.ProfanityFilter
	}
	return rules
}

// rulesTimeout limits how long a message waits for the config of a game that is not in the cache yet.
const rulesTimeout = time.Second

// ChatRules returns the message rules of the game. It reads the cache, and asks Platform only the first
// time (at most a second). It never fails: when the config is not known, the rules are DefaultChatRules.
func (c *Cache) ChatRules(ctx context.Context, gameID string) ChatRules {
	ctx, cancel := context.WithTimeout(ctx, rulesTimeout)
	defer cancel()
	cfg, err := c.Get(ctx, gameID)
	if err != nil || cfg.Version == 0 {
		return DefaultChatRules
	}
	return cfg.Chat
}

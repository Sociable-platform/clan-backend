package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// clanName is the clan the run creates. Every run has its own game, so the name never collides.
const clanName = "E2E Wolves"

// clanRoom is the part of a clan room's conversation object that the steps check.
type clanRoom struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	ClanID  *string `json:"clanId"`
	Members []struct {
		ID string `json:"id"`
	} `json:"members"`
	LastMessage *struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	} `json:"lastMessage"`
	Unread int `json:"unread"`
}

// clanChat runs the clan chat steps with a as the owner and b as the member who comes and goes.
func (s *scenario) clanChat(ctx context.Context, a, b *player) error {
	social := s.cfg.socialURL + "/api/v1/clans"

	// a creates the clan and gets its room.
	start := time.Now()
	var clan struct {
		ID string `json:"id"`
	}
	if err := s.api.call(ctx, http.MethodPost, social, bearerHeader(a.token), map[string]string{"name": clanName}, &clan, http.StatusCreated); err != nil {
		return fmt.Errorf("create clan: %w", err)
	}
	room, err := s.expectClanRoom(a, clan.ID, start, a)
	if err != nil {
		return err
	}
	pass(a.name+" created a clan and got its room", time.Since(start))

	// b joins: b gets the room with both members, a is told.
	if err := s.joinClan(ctx, b, a, clan.ID, room.ID, "joined"); err != nil {
		return err
	}

	// a writes; both get it.
	start = time.Now()
	first, err := s.sendMessage(ctx, a, []*player{b, a}, room.ID, "e2e-clan-1", "clan practice at 8")
	if err != nil {
		return err
	}
	pass("clan: ack for the sender, message.new for both", time.Since(start))

	// b leaves: the room leaves b's list.
	start = time.Now()
	if err := s.api.call(ctx, http.MethodPost, social+"/"+clan.ID+"/leave", bearerHeader(b.token), nil, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("leave clan: %w", err)
	}
	if _, err := b.ws.expect(start.Add(s.cfg.deadline), "conversation.removed", ofConversationRemoved(room.ID)); err != nil {
		return err
	}
	pass(b.name+" left and got conversation.removed", time.Since(start))

	// a writes while b is away; b must not get it.
	start = time.Now()
	missed, err := s.sendMessage(ctx, a, []*player{a}, room.ID, "e2e-clan-2", "written while you were away")
	if err != nil {
		return err
	}
	if err := b.ws.absent(time.Now().Add(300*time.Millisecond), "message.new of a clan they left", ofMessage(missed)); err != nil {
		return err
	}
	pass("clan: a message while "+b.name+" is away reaches only the members", time.Since(start))

	// b joins again and gets the whole history, the missed message included.
	start = time.Now()
	back, err := s.joinClanRoom(ctx, b, a, clan.ID, room.ID)
	if err != nil {
		return err
	}
	if back.LastMessage == nil || back.LastMessage.ID != missed {
		return fmt.Errorf("%s: the room's last message is %+v, want the one written while away (%s)", b.name, back.LastMessage, missed)
	}
	// What was written before joining is history, not unread.
	if back.Unread != 0 {
		return fmt.Errorf("%s: unread = %d, want 0 (a joiner starts with nothing unread)", b.name, back.Unread)
	}
	var history struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	err = s.api.call(ctx, http.MethodGet, s.cfg.realtimeURL+"/api/v1/conversations/"+room.ID+"/messages",
		bearerHeader(b.token), nil, &history, http.StatusOK)
	if err != nil {
		return fmt.Errorf("read clan history: %w", err)
	}
	if len(history.Messages) != 2 || history.Messages[0].ID != missed || history.Messages[1].ID != first {
		return fmt.Errorf("%s: history = %+v, want the two messages, newest first", b.name, history.Messages)
	}
	pass(b.name+" joined again and got the whole history", time.Since(start))

	// a, the owner, kicks b.
	start = time.Now()
	err = s.api.call(ctx, http.MethodDelete, social+"/"+clan.ID+"/members/"+b.socialID, bearerHeader(a.token), nil, nil, http.StatusNoContent)
	if err != nil {
		return fmt.Errorf("kick member: %w", err)
	}
	if _, err := b.ws.expect(start.Add(s.cfg.deadline), "conversation.removed", ofConversationRemoved(room.ID)); err != nil {
		return err
	}
	if _, err := b.ws.expect(start.Add(s.cfg.deadline), "clan_kicked notification", ofClanNotification("clan_kicked", clan.ID)); err != nil {
		return err
	}
	pass(b.name+" was kicked: conversation.removed and clan_kicked", time.Since(start))

	// a, the last member, leaves: the clan is disbanded.
	start = time.Now()
	if err := s.api.call(ctx, http.MethodPost, social+"/"+clan.ID+"/leave", bearerHeader(a.token), nil, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("leave clan: %w", err)
	}
	if _, err := a.ws.expect(start.Add(s.cfg.deadline), "conversation.removed", ofConversationRemoved(room.ID)); err != nil {
		return err
	}
	if _, err := a.ws.expect(start.Add(s.cfg.deadline), "clan_disbanded notification", ofClanNotification("clan_disbanded", clan.ID)); err != nil {
		return err
	}
	pass(a.name+" left last: conversation.removed and clan_disbanded", time.Since(start))
	return nil
}

// joinClan lets p join the clan and checks the room p gets and the clan_joined of member.
func (s *scenario) joinClan(ctx context.Context, p, member *player, clanID, roomID, what string) error {
	start := time.Now()
	if _, err := s.joinClanRoom(ctx, p, member, clanID, roomID); err != nil {
		return err
	}
	pass(fmt.Sprintf("%s %s: got the room, %s got clan_joined", p.name, what, member.name), time.Since(start))
	return nil
}

// joinClanRoom lets p join the clan, waits for p's conversation.added (the same room, p and member in
// it) and member's clan_joined naming p, and returns the room p got.
func (s *scenario) joinClanRoom(ctx context.Context, p, member *player, clanID, roomID string) (clanRoom, error) {
	start := time.Now()
	err := s.api.call(ctx, http.MethodPost, s.cfg.socialURL+"/api/v1/clans/"+clanID+"/join", bearerHeader(p.token), nil, nil, http.StatusOK)
	if err != nil {
		return clanRoom{}, fmt.Errorf("join clan: %w", err)
	}
	room, err := s.expectClanRoom(p, clanID, start, member, p)
	if err != nil {
		return clanRoom{}, err
	}
	if room.ID != roomID {
		return clanRoom{}, fmt.Errorf("%s: got room %s, the owner has %s", p.name, room.ID, roomID)
	}
	f, err := member.ws.expect(start.Add(s.cfg.deadline), "clan_joined notification", ofClanNotification("clan_joined", clanID))
	if err != nil {
		return clanRoom{}, err
	}
	var joined struct {
		Payload struct {
			Player struct {
				ID string `json:"id"`
			} `json:"player"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(f.Data, &joined); err != nil {
		return clanRoom{}, fmt.Errorf("%s: read clan_joined: %w", member.name, err)
	}
	if joined.Payload.Player.ID != p.socialID {
		return clanRoom{}, fmt.Errorf("%s: clan_joined names %q, want %s (%s)", member.name, joined.Payload.Player.ID, p.name, p.socialID)
	}
	return room, nil
}

// expectClanRoom waits for p's conversation.added of the clan's room and checks that its members are
// exactly want, in that order (the order they joined).
func (s *scenario) expectClanRoom(p *player, clanID string, start time.Time, want ...*player) (clanRoom, error) {
	f, err := p.ws.expect(start.Add(s.cfg.deadline), "conversation.added of the clan room", ofClanRoom(clanID))
	if err != nil {
		return clanRoom{}, err
	}
	var added struct {
		Conversation clanRoom `json:"conversation"`
	}
	if err := json.Unmarshal(f.Data, &added); err != nil {
		return clanRoom{}, fmt.Errorf("%s: read conversation.added: %w", p.name, err)
	}
	room := added.Conversation
	if room.ID == "" {
		return clanRoom{}, fmt.Errorf("%s: the clan room has no ID", p.name)
	}
	if len(room.Members) != len(want) {
		return clanRoom{}, fmt.Errorf("%s: the clan room has %d members, want %d", p.name, len(room.Members), len(want))
	}
	for i, w := range want {
		if room.Members[i].ID != w.socialID {
			return clanRoom{}, fmt.Errorf("%s: member %d of the clan room is %s, want %s", p.name, i, room.Members[i].ID, w.name)
		}
	}
	return room, nil
}

// ofClanRoom matches the conversation.added frame of one clan's room.
func ofClanRoom(clanID string) func(frame) bool {
	return func(f frame) bool {
		if f.Type != "conversation.added" {
			return false
		}
		var added struct {
			Conversation clanRoom `json:"conversation"`
		}
		return json.Unmarshal(f.Data, &added) == nil && added.Conversation.Kind == "clan" &&
			added.Conversation.ClanID != nil && *added.Conversation.ClanID == clanID
	}
}

// ofConversationRemoved matches the conversation.removed frame of one conversation.
func ofConversationRemoved(conversationID string) func(frame) bool {
	return func(f frame) bool {
		if f.Type != "conversation.removed" {
			return false
		}
		var removed struct {
			ConversationID string `json:"conversationId"`
		}
		return json.Unmarshal(f.Data, &removed) == nil && removed.ConversationID == conversationID
	}
}

// ofClanNotification matches a clan notification of one kind about one clan.
func ofClanNotification(kind, clanID string) func(frame) bool {
	return func(f frame) bool {
		if f.Type != "notification" {
			return false
		}
		var n struct {
			Kind    string `json:"kind"`
			Payload struct {
				ClanID string `json:"clanId"`
			} `json:"payload"`
		}
		return json.Unmarshal(f.Data, &n) == nil && n.Kind == kind && n.Payload.ClanID == clanID
	}
}

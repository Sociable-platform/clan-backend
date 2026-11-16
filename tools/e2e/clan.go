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
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		SenderID string `json:"senderId"`
		Body     string `json:"body"`
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

	// b joins: b gets the room with both members, a is told, and both get b's member_joined line.
	if err := s.joinClan(ctx, b, a, clan.ID, room.ID, "joined"); err != nil {
		return err
	}
	start = time.Now()
	joined1, err := s.expectMemberLine(start, room.ID, "member_joined", b, a, b)
	if err != nil {
		return err
	}
	pass("clan: both got "+b.name+"'s member_joined line", time.Since(start))

	// a writes; both get it.
	start = time.Now()
	first, err := s.sendMessage(ctx, a, []*player{b, a}, room.ID, "e2e-clan-1", "clan practice at 8")
	if err != nil {
		return err
	}
	pass("clan: ack for the sender, message.new for both", time.Since(start))

	// b leaves: the room leaves b's list, and a gets b's member_left line.
	start = time.Now()
	if err := s.api.call(ctx, http.MethodPost, social+"/"+clan.ID+"/leave", bearerHeader(b.token), nil, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("leave clan: %w", err)
	}
	if _, err := b.ws.expect(start.Add(s.cfg.deadline), "conversation.removed", ofConversationRemoved(room.ID)); err != nil {
		return err
	}
	left, err := s.expectMemberLine(start, room.ID, "member_left", b, a)
	if err != nil {
		return err
	}
	pass(b.name+" left: conversation.removed, and "+a.name+" got the member_left line", time.Since(start))

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

	// b joins again and gets the whole history, the missed message included. The room ends with b's
	// new member_joined line, which both get.
	start = time.Now()
	back, err := s.joinClanRoom(ctx, b, a, clan.ID, room.ID)
	if err != nil {
		return err
	}
	joined2, err := s.expectMemberLine(start, room.ID, "member_joined", b, a, b)
	if err != nil {
		return err
	}
	if back.LastMessage == nil || back.LastMessage.ID != joined2 || back.LastMessage.Kind != "member_joined" {
		return fmt.Errorf("%s: the room's last message is %+v, want the new member_joined line (%s)", b.name, back.LastMessage, joined2)
	}
	// What was written before joining is history, not unread, and member lines never count.
	if back.Unread != 0 {
		return fmt.Errorf("%s: unread = %d, want 0 (a joiner starts with nothing unread)", b.name, back.Unread)
	}
	var history struct {
		Messages []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"messages"`
	}
	err = s.api.call(ctx, http.MethodGet, s.cfg.realtimeURL+"/api/v1/conversations/"+room.ID+"/messages",
		bearerHeader(b.token), nil, &history, http.StatusOK)
	if err != nil {
		return fmt.Errorf("read clan history: %w", err)
	}
	want := []string{joined2 + " member_joined", missed + " text", left + " member_left", first + " text", joined1 + " member_joined"}
	var got []string
	for _, m := range history.Messages {
		got = append(got, m.ID+" "+m.Kind)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("%s: history = %v, want %v (newest first)", b.name, got, want)
	}
	// a has read nothing in the room: the member lines must not count, and the messages are a's own.
	if unread, err := s.roomUnread(ctx, a, room.ID); err != nil || unread != 0 {
		return fmt.Errorf("%s: unread in the clan room = %d (%v), want 0: member lines are never unread", a.name, unread, err)
	}
	pass(b.name+" joined again and got the whole history with the member lines, none unread", time.Since(start))

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
	if _, err := s.expectMemberLine(start, room.ID, "member_kicked", b, a); err != nil {
		return err
	}
	pass(b.name+" was kicked: conversation.removed and clan_kicked, and "+a.name+" got the member_kicked line", time.Since(start))

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

// expectMemberLine waits until every player in to got the message.new member line of kind about
// subject in the room, checks it (subject as sender, empty body) and returns its ID.
func (s *scenario) expectMemberLine(start time.Time, roomID, kind string, subject *player, to ...*player) (string, error) {
	var id string
	for _, p := range to {
		f, err := p.ws.expect(start.Add(s.cfg.deadline), kind+" line", ofMemberLine(roomID, kind))
		if err != nil {
			return "", err
		}
		var line struct {
			Message struct {
				ID       string `json:"id"`
				SenderID string `json:"senderId"`
				Body     string `json:"body"`
			} `json:"message"`
		}
		if err := json.Unmarshal(f.Data, &line); err != nil {
			return "", fmt.Errorf("%s: read the %s line: %w", p.name, kind, err)
		}
		if line.Message.SenderID != subject.socialID || line.Message.Body != "" {
			return "", fmt.Errorf("%s: the %s line is %+v, want %s as the sender and an empty body", p.name, kind, line.Message, subject.name)
		}
		if id != "" && line.Message.ID != id {
			return "", fmt.Errorf("%s: the %s line is %s, another player got %s", p.name, kind, line.Message.ID, id)
		}
		id = line.Message.ID
	}
	return id, nil
}

// roomUnread returns p's unread count of one conversation from GET /api/v1/conversations.
func (s *scenario) roomUnread(ctx context.Context, p *player, roomID string) (int, error) {
	var list struct {
		Conversations []clanRoom `json:"conversations"`
	}
	err := s.api.call(ctx, http.MethodGet, s.cfg.realtimeURL+"/api/v1/conversations", bearerHeader(p.token), nil, &list, http.StatusOK)
	if err != nil {
		return 0, fmt.Errorf("list conversations: %w", err)
	}
	for _, c := range list.Conversations {
		if c.ID == roomID {
			return c.Unread, nil
		}
	}
	return 0, fmt.Errorf("%s: room %s is not in the conversation list", p.name, roomID)
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

// ofMemberLine matches the message.new of a member line of one kind in one conversation.
func ofMemberLine(conversationID, kind string) func(frame) bool {
	return func(f frame) bool {
		if f.Type != "message.new" {
			return false
		}
		var m struct {
			ConversationID string `json:"conversationId"`
			Message        struct {
				Kind string `json:"kind"`
			} `json:"message"`
		}
		return json.Unmarshal(f.Data, &m) == nil && m.ConversationID == conversationID && m.Message.Kind == kind
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

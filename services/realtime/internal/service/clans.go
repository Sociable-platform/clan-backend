package service

import (
	"context"
	"fmt"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// Notification kinds of the clan events (see openapi/realtime-ws.md).
const (
	kindClanJoined    = "clan_joined"
	kindClanKicked    = "clan_kicked"
	kindClanDisbanded = "clan_disbanded"
)

// ClanCreated handles clan.created: the clan gets its chat room with the owner as its first member,
// and the owner gets conversation.added. Running it again stores nothing new; the frame is sent
// again, which the SDK ignores (it knows the conversation ID already).
func (s *Service) ClanCreated(ctx context.Context, gameID, clanID string, owner Player) ([]Push, error) {
	gameID, clanID, owner, err := cleanClanMember(gameID, clanID, owner)
	if err != nil {
		return nil, err
	}
	room, err := s.store.JoinClanRoom(ctx, gameID, clanID, repository.Member{PlayerID: owner.ID, Name: owner.Name})
	if err != nil {
		return nil, fmt.Errorf("clan created: %w", err)
	}
	return []Push{s.clanRoomAdded(gameID, clanID, room, owner.ID)}, nil
}

// ClanMemberJoined handles clan.member_joined: the player becomes a member of the clan's room (the
// room is made if clan.created was never seen) and gets conversation.added with the room as it is,
// the whole history included: what was written before they joined, and while they were away if they
// had been a member before. The other members get a clan_joined notification naming the player.
//
// When the player was not a member yet, a member_joined line is stored in the room and every member,
// the joiner included, gets it as message.new; the joiner's conversation.added already has it as the
// last message. Running it again for a member stores nothing new, writes no second line and sends
// the room and clan_joined again.
func (s *Service) ClanMemberJoined(ctx context.Context, gameID, clanID string, p Player) ([]Push, error) {
	gameID, clanID, p, err := cleanClanMember(gameID, clanID, p)
	if err != nil {
		return nil, err
	}
	room, err := s.store.JoinClanRoom(ctx, gameID, clanID, repository.Member{PlayerID: p.ID, Name: p.Name})
	if err != nil {
		return nil, fmt.Errorf("clan member joined: %w", err)
	}
	var line *Push
	if room.Joined {
		msg, push, err := s.memberLine(ctx, gameID, room.ConversationID, MessageMemberJoined, p)
		if err != nil {
			return nil, fmt.Errorf("clan member joined: %w", err)
		}
		room.LastMessage, line = msg, push
	}

	pushes := []Push{s.clanRoomAdded(gameID, clanID, room, p.ID)}
	others := make([]string, 0, len(room.Members))
	for _, m := range room.Members {
		if m.PlayerID != p.ID {
			others = append(others, m.PlayerID)
		}
	}
	if len(others) > 0 {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: others, Type: frameNotification,
			Data: notificationData{
				Kind:    kindClanJoined,
				Payload: map[string]any{"clanId": clanID, "player": playerRef(p)},
			},
		})
	}
	if line != nil {
		pushes = append(pushes, *line)
	}
	return pushes, nil
}

// ClanMemberLeft handles clan.member_left: the player stops being a member of the clan's room and
// gets conversation.removed. The messages stay for the others. When kicked is true (reason kicked)
// the player also gets a clan_kicked notification; that one is sent even when Realtime has no room
// for the clan, because the player was removed from the clan all the same.
//
// When the player was a member, a member_left line (member_kicked when kicked) with the name they had
// in the room is stored, and the members who stay get it as message.new. Running it again finds no
// membership, so it writes no second line.
func (s *Service) ClanMemberLeft(ctx context.Context, gameID, clanID, playerID string, kicked bool) ([]Push, error) {
	gameID, clanID, err := cleanClan(gameID, clanID)
	if err != nil {
		return nil, err
	}
	if playerID, err = cleanID("player.id", playerID); err != nil {
		return nil, err
	}
	leave, found, err := s.store.LeaveClanRoom(ctx, gameID, clanID, playerID)
	if err != nil {
		return nil, fmt.Errorf("clan member left: %w", err)
	}
	var line *Push
	if found && leave.Left {
		kind := MessageMemberLeft
		if kicked {
			kind = MessageMemberKicked
		}
		_, line, err = s.memberLine(ctx, gameID, leave.ConversationID, kind, Player{ID: playerID, Name: leave.Name})
		if err != nil {
			return nil, fmt.Errorf("clan member left: %w", err)
		}
	}

	var pushes []Push
	if found {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{playerID}, Type: frameConversationRemoved,
			Data: map[string]any{"conversationId": leave.ConversationID},
		})
	}
	if kicked {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{playerID}, Type: frameNotification,
			Data: notificationData{Kind: kindClanKicked, Payload: map[string]any{"clanId": clanID}},
		})
	}
	if line != nil {
		pushes = append(pushes, *line)
	}
	return pushes, nil
}

// ClanDisbanded handles clan.disbanded: the clan's room is deleted with its messages, and everybody
// who was still a member gets conversation.removed and a clan_disbanded notification. Social sends
// only this event when the last member leaves, so that member is told here too. When the game has no
// room for the clan (a second run, or the room was never made) there is nobody to tell.
func (s *Service) ClanDisbanded(ctx context.Context, gameID, clanID string) ([]Push, error) {
	gameID, clanID, err := cleanClan(gameID, clanID)
	if err != nil {
		return nil, err
	}
	id, memberIDs, found, err := s.store.DeleteClanRoom(ctx, gameID, clanID)
	if err != nil {
		return nil, fmt.Errorf("clan disbanded: %w", err)
	}
	if !found || len(memberIDs) == 0 {
		return nil, nil
	}
	return []Push{
		{
			GameID: gameID, PlayerIDs: memberIDs, Type: frameConversationRemoved,
			Data: map[string]any{"conversationId": id},
		},
		{
			GameID: gameID, PlayerIDs: memberIDs, Type: frameNotification,
			Data: notificationData{Kind: kindClanDisbanded, Payload: map[string]any{"clanId": clanID}},
		},
	}, nil
}

// clanRoomAdded is the conversation.added frame of a clan room for one of its members. The unread
// count is that member's own.
func (s *Service) clanRoomAdded(gameID, clanID string, room repository.ClanRoom, to string) Push {
	c := Conversation{
		ID:      room.ConversationID,
		Kind:    kindClan,
		ClanID:  &clanID,
		Members: make([]ConversationMember, 0, len(room.Members)),
		Unread:  room.Unread[to],
	}
	for _, m := range room.Members {
		c.Members = append(c.Members, ConversationMember{ID: m.PlayerID, Name: m.Name, Online: s.online(gameID, m.PlayerID)})
	}
	c.LastMessage = toMessage(room.LastMessage)
	return Push{
		GameID: gameID, PlayerIDs: []string{to}, Type: frameConversationAdded,
		Data: map[string]any{"conversation": c},
	}
}

// memberLine stores a member line of kind about p in the clan room, and returns it as stored and the
// message.new frame for the room's members now. The push is nil when nobody is left to tell.
func (s *Service) memberLine(ctx context.Context, gameID, conversationID, kind string, p Player) (*repository.Message, *Push, error) {
	id, at := s.ids.New()
	members, err := s.store.InsertMemberMessage(ctx, gameID, conversationID, repository.MemberMessage{
		ID: id, Kind: kind, PlayerID: p.ID, PlayerName: p.Name, CreatedAt: at,
	})
	if err != nil {
		return nil, nil, err
	}
	stored := &repository.Message{ID: id, Kind: kind, SenderID: p.ID, SenderName: p.Name, CreatedAt: at}
	if len(members) == 0 {
		return stored, nil, nil
	}
	return stored, &Push{
		GameID: gameID, PlayerIDs: members, Type: frameMessageNew,
		Data: messageNew{ConversationID: conversationID, Message: *toMessage(stored)},
	}, nil
}

// messageNew is the data of a message.new frame (openapi/realtime-ws.md).
type messageNew struct {
	ConversationID string  `json:"conversationId"`
	Message        Message `json:"message"`
}

func cleanClan(gameID, clanID string) (string, string, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return "", "", err
	}
	if clanID, err = cleanID("clan.id", clanID); err != nil {
		return "", "", err
	}
	return gameID, clanID, nil
}

func cleanClanMember(gameID, clanID string, p Player) (string, string, Player, error) {
	gameID, clanID, err := cleanClan(gameID, clanID)
	if err != nil {
		return "", "", Player{}, err
	}
	if p, err = cleanPlayer("player", p); err != nil {
		return "", "", Player{}, err
	}
	return gameID, clanID, p, nil
}

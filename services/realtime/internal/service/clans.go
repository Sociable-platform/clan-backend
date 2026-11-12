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
// Running it again stores nothing new and sends the same frames again.
func (s *Service) ClanMemberJoined(ctx context.Context, gameID, clanID string, p Player) ([]Push, error) {
	gameID, clanID, p, err := cleanClanMember(gameID, clanID, p)
	if err != nil {
		return nil, err
	}
	room, err := s.store.JoinClanRoom(ctx, gameID, clanID, repository.Member{PlayerID: p.ID, Name: p.Name})
	if err != nil {
		return nil, fmt.Errorf("clan member joined: %w", err)
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
	return pushes, nil
}

// ClanMemberLeft handles clan.member_left: the player stops being a member of the clan's room and
// gets conversation.removed. The messages stay for the others. When kicked is true (reason kicked)
// the player also gets a clan_kicked notification; that one is sent even when Realtime has no room
// for the clan, because the player was removed from the clan all the same. The members who stay are
// not told: the contract has no frame for it, and their member list is right on the next load.
func (s *Service) ClanMemberLeft(ctx context.Context, gameID, clanID, playerID string, kicked bool) ([]Push, error) {
	gameID, clanID, err := cleanClan(gameID, clanID)
	if err != nil {
		return nil, err
	}
	if playerID, err = cleanID("player.id", playerID); err != nil {
		return nil, err
	}
	id, found, err := s.store.LeaveClanRoom(ctx, gameID, clanID, playerID)
	if err != nil {
		return nil, fmt.Errorf("clan member left: %w", err)
	}

	var pushes []Push
	if found {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{playerID}, Type: frameConversationRemoved,
			Data: map[string]any{"conversationId": id},
		})
	}
	if kicked {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{playerID}, Type: frameNotification,
			Data: notificationData{Kind: kindClanKicked, Payload: map[string]any{"clanId": clanID}},
		})
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
	if m := room.LastMessage; m != nil {
		c.LastMessage = &Message{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, Body: m.Body, CreatedAt: m.CreatedAt.UTC()}
	}
	return Push{
		GameID: gameID, PlayerIDs: []string{to}, Type: frameConversationAdded,
		Data: map[string]any{"conversation": c},
	}
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

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

// ErrInvalidEvent means an event is missing a field or has a malformed one. Handling it again
// would fail again, so the consumer drops it instead of retrying.
var ErrInvalidEvent = errors.New("invalid event")

// Player is a player as the events name them: Social's player ID and the display name.
type Player struct {
	ID   string
	Name string
}

// Push is one frame for the WebSocket clients of some players of one game. The service only
// says what to send and to whom; whoever calls it writes the frame to the sockets.
type Push struct {
	GameID    string
	PlayerIDs []string
	Type      string // the frame type, for example "conversation.added"
	Data      any    // the frame's data, encoded as JSON by the caller
}

// Frame types and notification kinds this file sends (see openapi/realtime-ws.md).
const (
	frameNotification        = "notification"
	frameConversationAdded   = "conversation.added"
	frameConversationRemoved = "conversation.removed"
	frameConversationUpdated = "conversation.updated"
	framePresenceUpdate      = "presence.update"
	frameMessageNew          = "message.new"

	kindDM   = "dm"
	kindClan = "clan"

	kindFriendRequest          = "friend_request"
	kindFriendRequestCancelled = "friend_request_cancelled"
	kindFriendRequestDeclined  = "friend_request_declined"
	kindFriendAccepted         = "friend_accepted"
)

// Presence says who is connected right now.
type Presence interface {
	Online(gameID, playerID string) bool
}

// Option changes how New builds a Service.
type Option func(*Service)

// WithPresence sets where the service learns who is online. Without it everybody counts as offline.
func WithPresence(p Presence) Option {
	return func(s *Service) { s.presence = p }
}

// Conversation is a conversation as the SDK sees it, in REST responses and in conversation.added.
type Conversation struct {
	ID          string               `json:"id"`
	Kind        string               `json:"kind"` // "dm" or "clan"
	ClanID      *string              `json:"clanId"`
	Members     []ConversationMember `json:"members"`
	LastMessage *Message             `json:"lastMessage"`
	Unread      int                  `json:"unread"`
	ReadOnly    bool                 `json:"readOnly,omitempty"` // only on a DM whose two players are not friends any more: nobody can send
}

// ConversationMember is one member of a Conversation.
type ConversationMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// Message is a chat message as the SDK sees it.
type Message struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`     // MessageText, or a member line: MessageMemberJoined, MessageMemberLeft, MessageMemberKicked
	SenderID   string    `json:"senderId"` // for a member line, the player who joined or left
	SenderName string    `json:"senderName"`
	Body       string    `json:"body"` // empty for a member line: the widget writes the sentence
	CreatedAt  time.Time `json:"createdAt"`
}

// The kinds of a Message (openapi/realtime.yaml). Players write MessageText; Realtime writes the
// member lines in a clan room when a player joins, leaves or is kicked. Member lines are never unread.
const (
	MessageText         = "text"
	MessageMemberJoined = "member_joined"
	MessageMemberLeft   = "member_left"
	MessageMemberKicked = "member_kicked"
)

type notificationData struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

type playerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FriendRequestSent handles friend.request_sent: the receiver gets a friend_request notification.
// Nothing is stored but the event's ID (HandleOnce), so a copy of the event shows nothing again.
// requestID may be empty (an older event had none); the notification then has requestId null.
func (s *Service) FriendRequestSent(_ context.Context, gameID, requestID string, sender Player, receiverID string) ([]Push, error) {
	return requestNotice(gameID, kindFriendRequest, requestID, false, sender, receiverID)
}

// FriendRequestCancelled handles friend.request_cancelled: the sender took the request back, so
// the receiver's friend_request notification must go. The receiver gets a friend_request_cancelled
// notification (on every open tab); the SDK removes the friend_request with the same requestId.
func (s *Service) FriendRequestCancelled(_ context.Context, gameID, requestID string, sender Player, receiverID string) ([]Push, error) {
	return requestNotice(gameID, kindFriendRequestCancelled, requestID, true, sender, receiverID)
}

// FriendRequestDeclined handles friend.request_declined: the receiver said no. Only the receiver
// is told (so their other tabs drop the friend_request); the sender is not.
func (s *Service) FriendRequestDeclined(_ context.Context, gameID, requestID string, sender Player, receiverID string) ([]Push, error) {
	return requestNotice(gameID, kindFriendRequestDeclined, requestID, true, sender, receiverID)
}

// requestNotice builds the one notification about a friend request that goes to its receiver.
// The payload is the same for all three kinds: the request and the player who sent it.
func requestNotice(gameID, kind, requestID string, idRequired bool, sender Player, receiverID string) ([]Push, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return nil, err
	}
	reqID, err := cleanRequestID(requestID, idRequired)
	if err != nil {
		return nil, err
	}
	if sender, err = cleanPlayer("sender", sender); err != nil {
		return nil, err
	}
	if receiverID, err = cleanID("receiver.id", receiverID); err != nil {
		return nil, err
	}
	if receiverID == sender.ID {
		return nil, invalid("a player cannot send a friend request to themselves")
	}
	return []Push{{
		GameID:    gameID,
		PlayerIDs: []string{receiverID},
		Type:      frameNotification,
		Data: notificationData{
			Kind:    kind,
			Payload: map[string]any{"requestId": reqID, "from": playerRef(sender)},
		},
	}}, nil
}

// FriendAccepted handles friend.accepted: the pair gets a DM conversation (made once, reused if
// it exists), both get conversation.added and a friend_accepted notification naming the other.
// Each one who is online also gets presence.update "online" for the other one, right after
// conversation.added, so the new friend's dot is right from the start.
// Running it again for the same pair stores nothing new; the frames are sent again, which the
// SDK ignores (it knows the conversation ID already).
// requestID is the request that was accepted; it may be empty (an older event had none). When
// it is there, the notification carries it so the SDK can drop the matching friend_request.
func (s *Service) FriendAccepted(ctx context.Context, gameID, requestID string, a, b Player) ([]Push, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return nil, err
	}
	reqID, err := cleanRequestID(requestID, false)
	if err != nil {
		return nil, err
	}
	if a, err = cleanPlayer("a", a); err != nil {
		return nil, err
	}
	if b, err = cleanPlayer("b", b); err != nil {
		return nil, err
	}
	if a.ID == b.ID {
		return nil, invalid("a player cannot be a friend of themselves")
	}

	dm, err := s.store.UpsertDM(ctx, gameID, dmKey(a.ID, b.ID),
		repository.Member{PlayerID: a.ID, Name: a.Name}, repository.Member{PlayerID: b.ID, Name: b.Name})
	if err != nil {
		return nil, fmt.Errorf("friend accepted: %w", err)
	}

	// Each player gets their own conversation object: the unread count differs.
	pushes := make([]Push, 0, 6)
	for _, p := range []Player{a, b} {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{p.ID}, Type: frameConversationAdded,
			Data: map[string]any{"conversation": s.dmConversation(gameID, dm, a, b, dm.Unread[p.ID])},
		})
	}
	// Whoever is online is told the other one's status, but only if the other one is online: offline
	// is what the SDK assumes, and the offline frame comes later, from the presence tracker.
	for _, pair := range [][2]Player{{a, b}, {b, a}} {
		if s.online(gameID, pair[0].ID) && s.online(gameID, pair[1].ID) {
			pushes = append(pushes, Push{
				GameID: gameID, PlayerIDs: []string{pair[0].ID}, Type: framePresenceUpdate,
				Data: map[string]string{"playerId": pair[1].ID, "status": "online"},
			})
		}
	}
	for _, pair := range [][2]Player{{a, b}, {b, a}} {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{pair[0].ID}, Type: frameNotification,
			Data: notificationData{
				Kind:    kindFriendAccepted,
				Payload: map[string]any{"player": playerRef(pair[1]), "requestId": reqID}, // the other player
			},
		})
	}
	return pushes, nil
}

// FriendRemoved handles friend.removed: the two players stopped being friends, because one removed
// or blocked the other (TEAM-283). Both keep their DM and its history, read-only: nobody can send in
// it (not_friends), and it is not a friendship any more, so no presence goes either way. Each player
// gets conversation.updated with the conversation, readOnly set and both members offline. Nobody is
// told who ended it, and there is no notification. friend.accepted opens it again.
// removedBy is the player who removed the other; it must be one of the two. Without a DM for the pair
// (the accept was never seen), or for a redelivered event, there is nothing to send.
func (s *Service) FriendRemoved(ctx context.Context, gameID string, a, b Player, removedBy string) ([]Push, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return nil, err
	}
	if a, err = cleanPlayer("players[0]", a); err != nil {
		return nil, err
	}
	if b, err = cleanPlayer("players[1]", b); err != nil {
		return nil, err
	}
	if a.ID == b.ID {
		return nil, invalid("a player cannot be a friend of themselves")
	}
	if removedBy, err = cleanID("removed_by", removedBy); err != nil {
		return nil, err
	}
	if removedBy != a.ID && removedBy != b.ID {
		return nil, invalid("removed_by is not one of the two players")
	}

	dm, changed, err := s.store.MakeDMReadOnly(ctx, gameID, dmKey(a.ID, b.ID))
	if err != nil {
		return nil, fmt.Errorf("friend removed: %w", err)
	}
	if !changed {
		return nil, nil
	}
	pushes := make([]Push, 0, 2)
	for _, p := range []Player{a, b} {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{p.ID}, Type: frameConversationUpdated,
			Data: map[string]any{"conversation": s.dmConversation(gameID, dm, a, b, dm.Unread[p.ID])},
		})
	}
	return pushes, nil
}

// PlayerRenamed handles player.renamed: the new name replaces the one stored with the player's
// memberships in this game. No frame is sent; the SDK sees the name the next time it loads the
// conversations. Messages keep the name their sender had when they wrote them.
func (s *Service) PlayerRenamed(ctx context.Context, gameID string, p Player) ([]Push, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return nil, err
	}
	if p, err = cleanPlayer("player", p); err != nil {
		return nil, err
	}
	if err := s.store.RenamePlayer(ctx, gameID, p.ID, p.Name); err != nil {
		return nil, fmt.Errorf("player renamed: %w", err)
	}
	return nil, nil
}

// PlayerBlocked handles player.blocked: Realtime keeps its own copy of who blocked whom, so a message
// to a blocked player can be refused (blocked) without asking Social. Nothing is sent to anybody:
// blocking is private. Running it again changes nothing.
func (s *Service) PlayerBlocked(ctx context.Context, gameID, blockerID, blockedID string) ([]Push, error) {
	gameID, blockerID, blockedID, err := cleanBlock(gameID, blockerID, blockedID)
	if err != nil {
		return nil, err
	}
	if err := s.store.AddBlock(ctx, gameID, blockerID, blockedID); err != nil {
		return nil, fmt.Errorf("player blocked: %w", err)
	}
	return nil, nil
}

// PlayerUnblocked handles player.unblocked: the copy of the block is deleted. Nothing is sent to anybody.
// Running it again, or for a block Realtime never saw, changes nothing.
func (s *Service) PlayerUnblocked(ctx context.Context, gameID, blockerID, blockedID string) ([]Push, error) {
	gameID, blockerID, blockedID, err := cleanBlock(gameID, blockerID, blockedID)
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveBlock(ctx, gameID, blockerID, blockedID); err != nil {
		return nil, fmt.Errorf("player unblocked: %w", err)
	}
	return nil, nil
}

func cleanBlock(gameID, blockerID, blockedID string) (string, string, string, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return "", "", "", err
	}
	if blockerID, err = cleanID("blocker_id", blockerID); err != nil {
		return "", "", "", err
	}
	if blockedID, err = cleanID("blocked_id", blockedID); err != nil {
		return "", "", "", err
	}
	if blockerID == blockedID {
		return "", "", "", invalid("a player cannot block themselves")
	}
	return gameID, blockerID, blockedID, nil
}

// dmConversation builds the conversation object for a freshly upserted DM.
func (s *Service) dmConversation(gameID string, dm repository.DM, a, b Player, unread int) Conversation {
	// A read-only DM is not a friendship: its members' presence is not shown (always offline).
	online := func(id string) bool { return !dm.ReadOnly && s.online(gameID, id) }
	c := Conversation{
		ID:   dm.ConversationID,
		Kind: kindDM,
		Members: []ConversationMember{
			{ID: a.ID, Name: a.Name, Online: online(a.ID)},
			{ID: b.ID, Name: b.Name, Online: online(b.ID)},
		},
		Unread:   unread,
		ReadOnly: dm.ReadOnly,
	}
	c.LastMessage = toMessage(dm.LastMessage)
	return c
}

func (s *Service) online(gameID, playerID string) bool {
	return s.presence != nil && s.presence.Online(gameID, playerID)
}

// dmKey is the key of the one DM conversation of two players: their IDs, the smaller first, so the
// order of the two in an event does not matter. Both must be clean (see cleanID).
func dmKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, fmt.Sprintf(format, args...))
}

func cleanPlayer(field string, p Player) (Player, error) {
	id, err := cleanID(field+".id", p.ID)
	if err != nil {
		return Player{}, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return Player{}, invalid("%s.name is missing", field)
	}
	return Player{ID: id, Name: name}, nil
}

// cleanRequestID returns a friend request ID in lower case, or nil when there is none. An ID that
// is there must be a UUID; a missing one is an error only when required.
func cleanRequestID(s string, required bool) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return nil, invalid("request_id is missing")
		}
		return nil, nil
	}
	id, err := cleanID("request_id", s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// cleanID checks that s is a UUID and returns it in lower case, the form the database and dmKey use.
func cleanID(field, s string) (string, error) {
	if !isUUID(s) {
		return "", invalid("%s is not a UUID", field)
	}
	return strings.ToLower(s), nil
}

// isUUID reports whether s is a UUID in the usual 8-4-4-4-12 form.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

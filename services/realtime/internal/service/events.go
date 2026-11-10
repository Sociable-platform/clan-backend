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
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

type notificationData struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

type playerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FriendRequestSent handles friend.request_sent: the receiver gets a friend_request notification.
// Nothing is stored; a request that arrives twice shows twice (the SDK matches it by requestId).
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
// Running it again for the same pair stores nothing new; the frames are sent again, which the
// SDK ignores (it knows the conversation ID already).
// requestID is the request that was accepted; it may be empty (Social does not send it yet). When
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
	pushes := make([]Push, 0, 4)
	for _, p := range []Player{a, b} {
		pushes = append(pushes, Push{
			GameID: gameID, PlayerIDs: []string{p.ID}, Type: frameConversationAdded,
			Data: map[string]any{"conversation": s.dmConversation(gameID, dm, a, b, dm.Unread[p.ID])},
		})
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

// FriendRemoved handles friend.removed: both players stop being members of their DM
// conversation and get conversation.removed. The messages stay. When the game has no DM for the
// pair (the accept was never seen) there is nothing to do.
// removedBy is the player who removed the other; it must be one of the two. Nobody is told who it
// was: the conversation just disappears from both lists.
func (s *Service) FriendRemoved(ctx context.Context, gameID, aID, bID, removedBy string) ([]Push, error) {
	gameID, err := cleanID("game_id", gameID)
	if err != nil {
		return nil, err
	}
	if aID, err = cleanID("players[0].id", aID); err != nil {
		return nil, err
	}
	if bID, err = cleanID("players[1].id", bID); err != nil {
		return nil, err
	}
	if aID == bID {
		return nil, invalid("a player cannot be a friend of themselves")
	}
	if removedBy, err = cleanID("removed_by", removedBy); err != nil {
		return nil, err
	}
	if removedBy != aID && removedBy != bID {
		return nil, invalid("removed_by is not one of the two players")
	}

	id, found, err := s.store.RemoveDMMembers(ctx, gameID, dmKey(aID, bID))
	if err != nil {
		return nil, fmt.Errorf("friend removed: %w", err)
	}
	if !found {
		return nil, nil
	}
	return []Push{{
		GameID: gameID, PlayerIDs: []string{aID, bID}, Type: frameConversationRemoved,
		Data: map[string]any{"conversationId": id},
	}}, nil
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

// dmConversation builds the conversation object for a freshly upserted DM.
func (s *Service) dmConversation(gameID string, dm repository.DM, a, b Player, unread int) Conversation {
	c := Conversation{
		ID:   dm.ConversationID,
		Kind: "dm",
		Members: []ConversationMember{
			{ID: a.ID, Name: a.Name, Online: s.online(gameID, a.ID)},
			{ID: b.ID, Name: b.Name, Online: s.online(gameID, b.ID)},
		},
		Unread: unread,
	}
	if m := dm.LastMessage; m != nil {
		c.LastMessage = &Message{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, Body: m.Body, CreatedAt: m.CreatedAt.UTC()}
	}
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

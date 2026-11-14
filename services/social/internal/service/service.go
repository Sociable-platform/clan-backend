// Package service holds the social service's business rules.
// It gets data through the Store interface and knows nothing about HTTP.
package service

import (
	"context"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// Store is what the service needs from the repository. Using an interface keeps
// the service testable without a real database.
type Store interface {
	Ping(ctx context.Context) error
	UpsertPlayer(ctx context.Context, gameID, externalID, displayName string) (repository.Player, error)
	ListClans(ctx context.Context, gameID, query string, opts repository.ListClansOptions) ([]repository.Clan, error)
	CreateClan(ctx context.Context, gameID, ownerID string, in repository.NewClan) (repository.Clan, error)
	JoinClan(ctx context.Context, gameID, clanID, playerID string, maxMembers int, roleID string) (repository.Clan, error)
	LeaveClan(ctx context.Context, gameID, clanID, externalID string) (repository.LeaveResult, error)
	KickMember(ctx context.Context, gameID, clanID, actorExternalID, playerID string,
		allowed func(actor, target repository.MemberRole) bool) (repository.LeaveResult, error)
	ChangeRole(ctx context.Context, gameID, clanID, actorExternalID, playerID string,
		decide func(actor, target repository.MemberRole) (string, error)) (repository.RoleChange, error)
	RenameClan(ctx context.Context, gameID, clanID, ownerExternalID, name string) (repository.RenameResult, error)
	MyClan(ctx context.Context, gameID, externalID string) (repository.PlayerClan, error)
	ListMembers(ctx context.Context, gameID, clanID string) ([]repository.Member, error)
	SendFriendRequest(ctx context.Context, gameID, senderID, receiverID string) (repository.SendFriendRequestResult, error)
	AcceptFriendRequest(ctx context.Context, gameID, requestID, playerID string) (repository.Friendship, error)
	ListFriendRequests(ctx context.Context, gameID, playerID string) ([]repository.FriendRequest, error)
	ListFriends(ctx context.Context, gameID, playerID string) ([]repository.Friend, error)
	DeclineFriendRequest(ctx context.Context, gameID, requestID, playerID string) (repository.FriendRequest, error)
	CancelFriendRequest(ctx context.Context, gameID, requestID, playerID string) (repository.FriendRequest, error)
	RemoveFriend(ctx context.Context, gameID, playerID, friendID string) (repository.Friendship, error)
	SearchPlayers(ctx context.Context, gameID, playerID, query string, limit int) ([]repository.PlayerSearchResult, error)
	Block(ctx context.Context, gameID, blockerID, blockedID string) (repository.BlockResult, error)
	Unblock(ctx context.Context, gameID, blockerID, blockedID string) (repository.UnblockResult, error)
	ListBlocks(ctx context.Context, gameID, playerID string) ([]repository.BlockedPlayer, error)
	ListFriendshipsAfter(ctx context.Context, after repository.FriendshipKey, limit int) ([]repository.SyncFriendship, error)
	// InTx runs fn in one transaction; the store methods fn calls with its ctx join it. fn's error
	// is returned as it is, and then nothing fn did is kept.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	// AddEvents writes events to the outbox inside the transaction of InTx; outside it fails.
	AddEvents(ctx context.Context, evs ...events.Event) error
}

// Service is the social service's logic.
type Service struct {
	store Store
	rules gameconfig.Source // the clan rules each game set in Studio
}

// New returns a Service. Every game has the default clan rules (10 members) until WithRules
// sets a source. Events go to the store's outbox (see change).
func New(store Store) *Service {
	return &Service{store: store, rules: gameconfig.Fixed{}}
}

// WithRules sets where the service reads each game's clan rules (see package gameconfig).
// It returns s, so it can follow New.
func (s *Service) WithRules(src gameconfig.Source) *Service {
	s.rules = src
	return s
}

// maxMembers is how many members a clan of gameID may have: clan.maxMembers from the
// game's published config, 10 before the first publish.
func (s *Service) maxMembers(ctx context.Context, gameID string) int {
	return s.rules.Rules(ctx, gameID).MaxMembers
}

// change makes one change and records its events for the other services, in one transaction:
// fn does the change through the store with the ctx it is given and returns the events that
// describe it, which are written to the outbox before the commit. Either both are kept or neither
// is, and the outbox relay publishes the events once the transaction has committed (pkg/events).
// fn's error is returned as it is, so callers map the store's errors as before.
func (s *Service) change(ctx context.Context, fn func(ctx context.Context) ([]events.Event, error)) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		evs, err := fn(ctx)
		if err != nil || len(evs) == 0 {
			return err
		}
		return s.store.AddEvents(ctx, evs...)
	})
}

// Ready reports whether the service can do its work (for now: the database answers).
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// memStore is used by the player tests; it has no blocks.
func (m *memStore) Block(context.Context, string, string, string) (repository.BlockResult, error) {
	return repository.BlockResult{}, errors.New("memStore has no blocks")
}

func (m *memStore) Unblock(context.Context, string, string, string) (repository.UnblockResult, error) {
	return repository.UnblockResult{}, errors.New("memStore has no blocks")
}

func (m *memStore) ListBlocks(context.Context, string, string) ([]repository.BlockedPlayer, error) {
	return []repository.BlockedPlayer{}, nil
}

// blockStore remembers what it was asked and answers with what the test gave it.
type blockStore struct {
	*memStore
	called               int
	game, blocker, other string // arguments of the last call

	block   repository.BlockResult
	unblock repository.UnblockResult
	list    []repository.BlockedPlayer
	err     error
}

func newBlockStore() *blockStore { return &blockStore{memStore: newMemStore()} }

func (b *blockStore) Block(_ context.Context, gameID, blockerID, blockedID string) (repository.BlockResult, error) {
	b.called++
	b.game, b.blocker, b.other = gameID, blockerID, blockedID
	return b.block, b.err
}

func (b *blockStore) Unblock(_ context.Context, gameID, blockerID, blockedID string) (repository.UnblockResult, error) {
	b.called++
	b.game, b.blocker, b.other = gameID, blockerID, blockedID
	return b.unblock, b.err
}

func (b *blockStore) ListBlocks(_ context.Context, gameID, playerID string) ([]repository.BlockedPlayer, error) {
	b.called++
	b.game, b.blocker = gameID, playerID
	return b.list, b.err
}

var (
	blockAli   = repository.FriendPlayer{ID: me, DisplayName: "Ali"}
	blockAysel = repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"}
)

const blockReqID = "aaaaaaaa-0000-4000-8000-000000000002"

func TestBlockPlayerSavesTheBlockAndPublishesPlayerBlocked(t *testing.T) {
	store := newBlockStore()
	store.block = repository.BlockResult{Blocker: blockAli, Blocked: blockAysel, Created: true}
	svc, rec := friendService(store)

	// Upper case and spaces around the ID are accepted; the store gets it clean.
	if err := svc.BlockPlayer(context.Background(), gameA, "u_1", "Ali", "  "+strings.ToUpper(otherID)+" "); err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.blocker != me || store.other != otherID {
		t.Fatalf("store got game=%q blocker=%q blocked=%q", store.game, store.blocker, store.other)
	}
	wantData(t, oneEvent(t, rec, EventPlayerBlocked), map[string]any{"blocker_id": me, "blocked_id": otherID})
}

func TestBlockPlayerTwicePublishesNothing(t *testing.T) {
	store := newBlockStore()
	store.block = repository.BlockResult{Blocker: blockAli, Blocked: blockAysel} // Created false
	svc, rec := friendService(store)
	if err := svc.BlockPlayer(context.Background(), gameA, "u_1", "Ali", otherID); err != nil {
		t.Fatal(err)
	}
	if evs := rec.Events(); len(evs) != 0 {
		t.Fatalf("a repeated block published %+v", evs)
	}
}

// A block ends the pair's friendship or pending request; the other services hear about it
// with the same events as when a player does that by hand.
func TestBlockPlayerPublishesWhatTheBlockEnded(t *testing.T) {
	sentByMe := repository.FriendRequest{ID: blockReqID, Sender: blockAli, Receiver: blockAysel}
	sentToMe := repository.FriendRequest{ID: blockReqID, Sender: blockAysel, Receiver: blockAli}
	blocked := map[string]any{"blocker_id": me, "blocked_id": otherID}

	cases := []struct {
		name     string
		res      repository.BlockResult
		wantType string
		wantData map[string]any
	}{
		{
			name:     "friends",
			res:      repository.BlockResult{RemovedFriendship: &repository.Friendship{PlayerA: blockAysel, PlayerB: blockAli}},
			wantType: EventFriendRemoved,
			wantData: map[string]any{
				"players":    []map[string]any{playerData(blockAysel), playerData(blockAli)},
				"removed_by": me,
			},
		},
		{
			name:     "the caller had sent a request",
			res:      repository.BlockResult{RemovedRequest: &sentByMe},
			wantType: EventFriendRequestCancelled,
			wantData: requestData(sentByMe),
		},
		{
			name:     "the blocked player had sent a request",
			res:      repository.BlockResult{RemovedRequest: &sentToMe},
			wantType: EventFriendRequestDeclined,
			wantData: requestData(sentToMe),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newBlockStore()
			store.block = tc.res
			store.block.Blocker, store.block.Blocked, store.block.Created = blockAli, blockAysel, true
			svc, rec := friendService(store)
			if err := svc.BlockPlayer(context.Background(), gameA, "u_1", "Ali", otherID); err != nil {
				t.Fatal(err)
			}
			evs := rec.Events()
			if len(evs) != 2 {
				t.Fatalf("events = %+v, want player.blocked and %s", evs, tc.wantType)
			}
			if evs[0].Type != EventPlayerBlocked || !reflect.DeepEqual(evs[0].Data, blocked) {
				t.Fatalf("first event = %+v, want %s %v", evs[0], EventPlayerBlocked, blocked)
			}
			if evs[1].Type != tc.wantType || evs[1].GameID != gameA {
				t.Fatalf("second event = %+v, want %s", evs[1], tc.wantType)
			}
			wantData(t, evs[1].Data, tc.wantData)
		})
	}
}

func TestUnblockPlayerPublishesPlayerUnblockedOnlyWhenABlockWasRemoved(t *testing.T) {
	store := newBlockStore()
	store.unblock = repository.UnblockResult{Blocker: blockAli, Blocked: blockAysel, Removed: true}
	svc, rec := friendService(store)
	ctx := context.Background()

	if err := svc.UnblockPlayer(ctx, gameA, "u_1", "Ali", strings.ToUpper(otherID)); err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.blocker != me || store.other != otherID {
		t.Fatalf("store got game=%q blocker=%q blocked=%q", store.game, store.blocker, store.other)
	}
	wantData(t, oneEvent(t, rec, EventPlayerUnblocked), map[string]any{"blocker_id": me, "blocked_id": otherID})

	store.unblock.Removed = false // not blocked (any more)
	if err := svc.UnblockPlayer(ctx, gameA, "u_1", "Ali", otherID); err != nil {
		t.Fatal(err)
	}
	if evs := rec.Events(); len(evs) != 1 {
		t.Fatalf("unblocking a player who is not blocked published %+v", evs[1:])
	}
}

func TestBlockAndUnblockErrors(t *testing.T) {
	boom := errors.New("connection reset")
	cases := []struct {
		name       string
		playerID   string
		storeErr   error
		status     int // 0: not an httpx error, the store's error is returned wrapped
		code       string
		reachStore bool
	}{
		{"no player ID", "  ", nil, http.StatusBadRequest, "invalid_player_id", false},
		{"not a UUID", "abc", nil, http.StatusNotFound, "player_not_found", false},
		{"SQL in the ID", "' or 1=1 --", nil, http.StatusNotFound, "player_not_found", false},
		{"yourself", otherID, repository.ErrBlockSelf, http.StatusBadRequest, "cannot_block_self", true},
		{"unknown player", otherID, repository.ErrPlayerNotFound, http.StatusNotFound, "player_not_found", true},
		{"database down", otherID, boom, 0, "", true},
	}
	actions := map[string]func(*Service, string) error{
		"block": func(s *Service, id string) error {
			return s.BlockPlayer(context.Background(), gameA, "u_1", "Ali", id)
		},
		"unblock": func(s *Service, id string) error {
			return s.UnblockPlayer(context.Background(), gameA, "u_1", "Ali", id)
		},
	}
	for action, act := range actions {
		for _, tc := range cases {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				store := newBlockStore()
				store.err = tc.storeErr
				svc, rec := friendService(store)
				err := act(svc, tc.playerID)
				if tc.status == 0 {
					var he *httpx.Error
					if !errors.Is(err, boom) || errors.As(err, &he) {
						t.Fatalf("err = %v, want the store's error", err)
					}
				} else {
					wantHTTPError(t, err, tc.status, tc.code)
				}
				if reached := store.called != 0; reached != tc.reachStore {
					t.Fatalf("store called = %v, want %v", reached, tc.reachStore)
				}
				if evs := rec.Events(); len(evs) != 0 {
					t.Fatalf("a failed %s published %+v", action, evs)
				}
			})
		}
	}
}

func TestListBlocks(t *testing.T) {
	store := newBlockStore()
	since := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	store.list = []repository.BlockedPlayer{{FriendPlayer: blockAysel, Since: since}}
	got, err := New(store).ListBlocks(context.Background(), gameA, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.blocker != me {
		t.Fatalf("store got game=%q player=%q", store.game, store.blocker)
	}
	want := []BlockedPlayer{{FriendPlayer: FriendPlayer{ID: otherID, DisplayName: "Aysel"}, Since: since}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	store.list = nil
	got, err = New(store).ListBlocks(context.Background(), gameA, "u_1", "Ali")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no blocks: got %#v, %v; want an empty list", got, err)
	}
}

// player.blocked and player.unblocked must have exactly the fields of docs/event-messages.md,
// like the friend events (TestFriendEventsHaveTheFieldsOfTheEventMessagesFile).
func TestBlockEventsHaveTheFieldsOfTheEventMessagesFile(t *testing.T) {
	ctx := context.Background()
	actions := map[string]func(*blockStore, *Service) error{
		EventPlayerBlocked: func(st *blockStore, s *Service) error {
			st.block = repository.BlockResult{Blocker: blockAli, Blocked: blockAysel, Created: true}
			return s.BlockPlayer(ctx, gameA, "u_1", "Ali", otherID)
		},
		EventPlayerUnblocked: func(st *blockStore, s *Service) error {
			st.unblock = repository.UnblockResult{Blocker: blockAli, Blocked: blockAysel, Removed: true}
			return s.UnblockPlayer(ctx, gameA, "u_1", "Ali", otherID)
		},
	}
	want := docShapes(t)
	for typ, act := range actions {
		t.Run(typ, func(t *testing.T) {
			store := newBlockStore()
			svc, rec := friendService(store)
			if err := act(store, svc); err != nil {
				t.Fatal(err)
			}
			evs := rec.Events()
			if len(evs) != 1 || evs[0].Type != typ {
				t.Fatalf("events = %+v, want one %s", evs, typ)
			}
			wantShape, ok := want[typ]
			if !ok {
				t.Fatalf("%s is not in docs/event-messages.md", typ)
			}
			if got := publishedShape(t, evs[0]); got != wantShape {
				t.Errorf("fields of %s:\n  published %s\n  file      %s", typ, got, wantShape)
			}
		})
	}
}

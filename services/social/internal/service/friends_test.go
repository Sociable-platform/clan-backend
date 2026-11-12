package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// memStore is used by the player tests; it has no friends.
func (m *memStore) SendFriendRequest(context.Context, string, string, string) (repository.SendFriendRequestResult, error) {
	return repository.SendFriendRequestResult{}, errors.New("memStore has no friends")
}

func (m *memStore) AcceptFriendRequest(context.Context, string, string, string) (repository.Friendship, error) {
	return repository.Friendship{}, errors.New("memStore has no friends")
}

func (m *memStore) ListFriendRequests(context.Context, string, string) ([]repository.FriendRequest, error) {
	return []repository.FriendRequest{}, nil
}

func (m *memStore) ListFriends(context.Context, string, string) ([]repository.Friend, error) {
	return []repository.Friend{}, nil
}

func (m *memStore) DeclineFriendRequest(context.Context, string, string, string) (repository.FriendRequest, error) {
	return repository.FriendRequest{}, errors.New("memStore has no friends")
}

func (m *memStore) CancelFriendRequest(context.Context, string, string, string) (repository.FriendRequest, error) {
	return repository.FriendRequest{}, errors.New("memStore has no friends")
}

func (m *memStore) RemoveFriend(context.Context, string, string, string) (repository.Friendship, error) {
	return repository.Friendship{}, errors.New("memStore has no friends")
}

// friendStore remembers what it was asked and answers with what the test gave it.
// The calling player is always "player-1" (the first player memStore creates).
type friendStore struct {
	*memStore
	called              int
	game, player, other string // arguments of the last call

	send     repository.SendFriendRequestResult
	accept   repository.Friendship
	requests []repository.FriendRequest
	friends  []repository.Friend
	removed  repository.Friendship
	ended    repository.FriendRequest // what Decline and Cancel return
	err      error
}

func newFriendStore() *friendStore { return &friendStore{memStore: newMemStore()} }

func (f *friendStore) SendFriendRequest(_ context.Context, gameID, senderID, receiverID string) (repository.SendFriendRequestResult, error) {
	f.called++
	f.game, f.player, f.other = gameID, senderID, receiverID
	return f.send, f.err
}

func (f *friendStore) AcceptFriendRequest(_ context.Context, gameID, requestID, playerID string) (repository.Friendship, error) {
	f.called++
	f.game, f.other, f.player = gameID, requestID, playerID
	return f.accept, f.err
}

func (f *friendStore) ListFriendRequests(_ context.Context, gameID, playerID string) ([]repository.FriendRequest, error) {
	f.called++
	f.game, f.player = gameID, playerID
	return f.requests, f.err
}

func (f *friendStore) ListFriends(_ context.Context, gameID, playerID string) ([]repository.Friend, error) {
	f.called++
	f.game, f.player = gameID, playerID
	return f.friends, f.err
}

func (f *friendStore) DeclineFriendRequest(_ context.Context, gameID, requestID, playerID string) (repository.FriendRequest, error) {
	f.called++
	f.game, f.other, f.player = gameID, requestID, playerID
	return f.ended, f.err
}

func (f *friendStore) CancelFriendRequest(_ context.Context, gameID, requestID, playerID string) (repository.FriendRequest, error) {
	f.called++
	f.game, f.other, f.player = gameID, requestID, playerID
	return f.ended, f.err
}

func (f *friendStore) RemoveFriend(_ context.Context, gameID, playerID, friendID string) (repository.Friendship, error) {
	f.called++
	f.game, f.player, f.other = gameID, playerID, friendID
	return f.removed, f.err
}

const (
	me      = "player-1" // what memStore calls the first player it sees
	otherID = "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"
)

var when = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

// friendService returns a service on store that records its events.
func friendService(store Store) (*Service, *events.Recorder) {
	rec := &events.Recorder{}
	return New(store).WithPublisher(rec), rec
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func wantHTTPError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status || he.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

func TestSendFriendRequestCreatesARequestAndPublishesIt(t *testing.T) {
	store := newFriendStore()
	store.send = repository.SendFriendRequestResult{Request: &repository.FriendRequest{
		ID: "req-1", CreatedAt: when,
		Sender:   repository.FriendPlayer{ID: me, DisplayName: "Ali"},
		Receiver: repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"},
	}}
	svc, rec := friendService(store)

	// upper case and spaces are cleaned before the store sees the ID
	got, err := svc.SendFriendRequest(context.Background(), gameA, "u_1", "Ali", "  3B0C7A52-6D1E-4F0A-9C2B-1E2D3C4B5A69 ")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.player != me || store.other != otherID {
		t.Errorf("store got game=%q sender=%q receiver=%q", store.game, store.player, store.other)
	}
	want := FriendRequest{ID: "req-1", Player: FriendPlayer{otherID, "Aysel"}, CreatedAt: when}
	if got.Friend != nil || got.Request == nil || *got.Request != want {
		t.Fatalf("result = %+v, want request %+v", got, want)
	}

	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != EventFriendRequestSent || evs[0].GameID != gameA {
		t.Fatalf("events = %+v, want one %s", evs, EventFriendRequestSent)
	}
	if evs[0].Data["request_id"] != "req-1" {
		t.Errorf("event request_id = %v, want req-1", evs[0].Data["request_id"])
	}
	sender, _ := evs[0].Data["sender"].(map[string]any)
	receiver, _ := evs[0].Data["receiver"].(map[string]any)
	if sender["id"] != me || sender["name"] != "Ali" || receiver["id"] != otherID || receiver["name"] != "Aysel" {
		t.Errorf("event data = %+v", evs[0].Data)
	}
}

func TestSendFriendRequestBackMakesFriendsAndPublishesAccepted(t *testing.T) {
	store := newFriendStore()
	store.send = repository.SendFriendRequestResult{Accepted: &repository.Friendship{
		CreatedAt: when,
		RequestID: "req-old",
		PlayerA:   repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"},
		PlayerB:   repository.FriendPlayer{ID: me, DisplayName: "Ali"},
	}}
	svc, rec := friendService(store)

	got, err := svc.SendFriendRequest(context.Background(), gameA, "u_1", "Ali", otherID)
	if err != nil {
		t.Fatal(err)
	}
	want := Friend{FriendPlayer: FriendPlayer{otherID, "Aysel"}, Since: when}
	if got.Request != nil || got.Friend == nil || *got.Friend != want {
		t.Fatalf("result = %+v, want friend %+v", got, want)
	}
	checkAcceptedEvent(t, rec, "req-old", otherID, me)
}

func checkAcceptedEvent(t *testing.T, rec *events.Recorder, requestID, a, b string) {
	t.Helper()
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != EventFriendAccepted || evs[0].GameID != gameA {
		t.Fatalf("events = %+v, want one %s", evs, EventFriendAccepted)
	}
	if evs[0].Data["request_id"] != requestID {
		t.Errorf("event request_id = %v, want %s (the request that was accepted)", evs[0].Data["request_id"], requestID)
	}
	players, _ := evs[0].Data["players"].([]map[string]any)
	if len(players) != 2 || players[0]["id"] != a || players[1]["id"] != b ||
		players[0]["name"] == "" || players[1]["name"] == "" {
		t.Errorf("event data = %+v, want players %s and %s with names", evs[0].Data, a, b)
	}
}

func TestSendFriendRequestErrors(t *testing.T) {
	cases := []struct {
		name     string
		playerID string
		storeErr error
		status   int
		code     string
		reaches  bool // whether the store is asked at all
	}{
		{"no player ID", "  ", nil, http.StatusBadRequest, "invalid_player_id", false},
		{"not a UUID", "u_42", nil, http.StatusNotFound, "player_not_found", false},
		{"yourself", otherID, repository.ErrFriendRequestToSelf, http.StatusBadRequest, "cannot_friend_yourself", true},
		{"unknown player", otherID, repository.ErrPlayerNotFound, http.StatusNotFound, "player_not_found", true},
		{"already friends", otherID, repository.ErrAlreadyFriends, http.StatusConflict, "already_friends", true},
		{"request exists", otherID, repository.ErrFriendRequestExists, http.StatusConflict, "friend_request_exists", true},
		{"the caller blocked them", otherID, repository.ErrPlayerBlocked, http.StatusConflict, "player_blocked", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFriendStore()
			store.err = c.storeErr
			svc, rec := friendService(store)
			_, err := svc.SendFriendRequest(context.Background(), gameA, "u_1", "Ali", c.playerID)
			wantHTTPError(t, err, c.status, c.code)
			if (store.called > 0) != c.reaches {
				t.Errorf("store called %d times, reaches = %v", store.called, c.reaches)
			}
			if len(rec.Events()) != 0 {
				t.Errorf("published %+v after a failure, want nothing", rec.Events())
			}
		})
	}
}

func TestSendFriendRequestDatabaseFailure(t *testing.T) {
	store := newFriendStore()
	store.err = errors.New("connection lost")
	svc, rec := friendService(store)
	_, err := svc.SendFriendRequest(context.Background(), gameA, "u_1", "Ali", otherID)
	var he *httpx.Error
	if err == nil || errors.As(err, &he) {
		t.Fatalf("err = %v, want a plain error (500)", err)
	}
	if len(rec.Events()) != 0 {
		t.Error("published an event although nothing was saved")
	}
}

// A broker that is down must not fail the request: main wraps the publisher in events.Safe.
func TestSendFriendRequestSucceedsWhenPublishingFails(t *testing.T) {
	store := newFriendStore()
	store.send = repository.SendFriendRequestResult{Request: &repository.FriendRequest{ID: "req-1"}}
	svc := New(store).WithPublisher(events.Safe(&events.Recorder{Err: errors.New("broker down")}, quietLog()))
	if _, err := svc.SendFriendRequest(context.Background(), gameA, "u_1", "Ali", otherID); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestAcceptFriendRequestMakesFriendsAndPublishes(t *testing.T) {
	store := newFriendStore()
	store.accept = repository.Friendship{
		CreatedAt: when,
		RequestID: "aaaaaaaa-0000-4000-8000-000000000001",
		PlayerA:   repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"},
		PlayerB:   repository.FriendPlayer{ID: me, DisplayName: "Ali"},
	}
	svc, rec := friendService(store)

	got, err := svc.AcceptFriendRequest(context.Background(), gameA, "u_1", "Ali", "AAAAAAAA-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.player != me || store.other != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Errorf("store got game=%q player=%q request=%q", store.game, store.player, store.other)
	}
	if want := (Friend{FriendPlayer: FriendPlayer{otherID, "Aysel"}, Since: when}); got != want {
		t.Errorf("friend = %+v, want %+v", got, want)
	}
	checkAcceptedEvent(t, rec, "aaaaaaaa-0000-4000-8000-000000000001", otherID, me)
}

func TestAcceptFriendRequestErrors(t *testing.T) {
	cases := []struct {
		name      string
		requestID string
		storeErr  error
		status    int
		code      string
	}{
		{"not a UUID", "req-1", nil, http.StatusNotFound, "friend_request_not_found"},
		{"unknown request", otherID, repository.ErrFriendRequestNotFound, http.StatusNotFound, "friend_request_not_found"},
		{"the sender", otherID, repository.ErrNotFriendRequestReceiver, http.StatusForbidden, "not_request_receiver"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFriendStore()
			store.err = c.storeErr
			svc, rec := friendService(store)
			_, err := svc.AcceptFriendRequest(context.Background(), gameA, "u_1", "Ali", c.requestID)
			wantHTTPError(t, err, c.status, c.code)
			if len(rec.Events()) != 0 {
				t.Errorf("published %+v after a failure, want nothing", rec.Events())
			}
		})
	}
}

func TestListFriendRequestsSplitsIncomingAndOutgoing(t *testing.T) {
	store := newFriendStore()
	store.requests = []repository.FriendRequest{
		{ID: "in-1", CreatedAt: when, Sender: repository.FriendPlayer{ID: "p-2", DisplayName: "Murad"},
			Receiver: repository.FriendPlayer{ID: me, DisplayName: "Ali"}},
		{ID: "out-1", CreatedAt: when, Sender: repository.FriendPlayer{ID: me, DisplayName: "Ali"},
			Receiver: repository.FriendPlayer{ID: "p-3", DisplayName: "Nuray"}},
	}
	svc, _ := friendService(store)

	got, err := svc.ListFriendRequests(context.Background(), gameA, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.player != me {
		t.Errorf("store got game=%q player=%q", store.game, store.player)
	}
	if len(got.Incoming) != 1 || got.Incoming[0] != (FriendRequest{"in-1", FriendPlayer{"p-2", "Murad"}, when}) {
		t.Errorf("incoming = %+v", got.Incoming)
	}
	if len(got.Outgoing) != 1 || got.Outgoing[0] != (FriendRequest{"out-1", FriendPlayer{"p-3", "Nuray"}, when}) {
		t.Errorf("outgoing = %+v", got.Outgoing)
	}
}

func TestListFriendRequestsAndFriendsEmptyAreNotNil(t *testing.T) {
	svc, _ := friendService(newFriendStore())
	reqs, err := svc.ListFriendRequests(context.Background(), gameA, "u_1", "Ali")
	if err != nil || reqs.Incoming == nil || reqs.Outgoing == nil {
		t.Errorf("requests = %+v, %v; want two empty lists", reqs, err)
	}
	friends, err := svc.ListFriends(context.Background(), gameA, "u_1", "Ali")
	if err != nil || friends == nil || len(friends) != 0 {
		t.Errorf("friends = %+v, %v; want an empty list", friends, err)
	}
}

func TestListFriends(t *testing.T) {
	store := newFriendStore()
	store.friends = []repository.Friend{{FriendPlayer: repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"}, Since: when}}
	svc, _ := friendService(store)
	got, err := svc.ListFriends(context.Background(), gameA, "u_1", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if store.player != me || len(got) != 1 || got[0] != (Friend{FriendPlayer{otherID, "Aysel"}, when}) {
		t.Errorf("friends = %+v (store player %q)", got, store.player)
	}
}

func TestDeclineAndCancelFriendRequestPublishTheirEvent(t *testing.T) {
	type call func(*Service) error
	const reqID = "AAAAAAAA-0000-4000-8000-000000000001"
	cases := []struct {
		name      string
		fn        call
		eventType string
	}{
		{"decline", func(s *Service) error {
			return s.DeclineFriendRequest(context.Background(), gameA, "u_1", "Ali", reqID)
		}, EventFriendRequestDeclined},
		{"cancel", func(s *Service) error {
			return s.CancelFriendRequest(context.Background(), gameA, "u_1", "Ali", reqID)
		}, EventFriendRequestCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFriendStore()
			// What the repository returns: the request that was deleted, from otherID to me.
			store.ended = repository.FriendRequest{
				ID:       "aaaaaaaa-0000-4000-8000-000000000001",
				Sender:   repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"},
				Receiver: repository.FriendPlayer{ID: me, DisplayName: "Ali"},
			}
			svc, rec := friendService(store)
			if err := c.fn(svc); err != nil {
				t.Fatal(err)
			}
			if store.game != gameA || store.player != me || store.other != "aaaaaaaa-0000-4000-8000-000000000001" {
				t.Errorf("store got game=%q player=%q request=%q", store.game, store.player, store.other)
			}
			evs := rec.Events()
			if len(evs) != 1 || evs[0].Type != c.eventType || evs[0].GameID != gameA {
				t.Fatalf("events = %+v, want one %s", evs, c.eventType)
			}
			sender, _ := evs[0].Data["sender"].(map[string]any)
			receiver, _ := evs[0].Data["receiver"].(map[string]any)
			if evs[0].Data["request_id"] != "aaaaaaaa-0000-4000-8000-000000000001" ||
				sender["id"] != otherID || sender["name"] != "Aysel" || receiver["id"] != me || receiver["name"] != "Ali" {
				t.Errorf("event data = %+v", evs[0].Data)
			}
			if len(evs[0].Data) != 3 {
				t.Errorf("event data has %d fields, want exactly request_id, sender and receiver", len(evs[0].Data))
			}
		})
	}
}

func TestDeclineAndCancelErrors(t *testing.T) {
	decline := func(s *Service, id string) error {
		return s.DeclineFriendRequest(context.Background(), gameA, "u_1", "Ali", id)
	}
	cancel := func(s *Service, id string) error {
		return s.CancelFriendRequest(context.Background(), gameA, "u_1", "Ali", id)
	}
	cases := []struct {
		name     string
		fn       func(*Service, string) error
		id       string
		storeErr error
		status   int
		code     string
	}{
		{"decline: not a UUID", decline, "req-1", nil, http.StatusNotFound, "friend_request_not_found"},
		{"decline: unknown", decline, otherID, repository.ErrFriendRequestNotFound, http.StatusNotFound, "friend_request_not_found"},
		{"decline: the sender", decline, otherID, repository.ErrNotFriendRequestReceiver, http.StatusForbidden, "not_request_receiver"},
		{"cancel: not a UUID", cancel, "req-1", nil, http.StatusNotFound, "friend_request_not_found"},
		{"cancel: unknown", cancel, otherID, repository.ErrFriendRequestNotFound, http.StatusNotFound, "friend_request_not_found"},
		{"cancel: the receiver", cancel, otherID, repository.ErrNotFriendRequestSender, http.StatusForbidden, "not_request_sender"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFriendStore()
			store.err = c.storeErr
			svc, rec := friendService(store)
			wantHTTPError(t, c.fn(svc, c.id), c.status, c.code)
			if len(rec.Events()) != 0 {
				t.Errorf("published %+v after a failure, want nothing", rec.Events())
			}
		})
	}
}

func TestRemoveFriendPublishesRemoved(t *testing.T) {
	store := newFriendStore()
	store.removed = repository.Friendship{
		CreatedAt: when,
		PlayerA:   repository.FriendPlayer{ID: otherID, DisplayName: "Aysel"},
		PlayerB:   repository.FriendPlayer{ID: me, DisplayName: "Ali"},
	}
	svc, rec := friendService(store)

	if err := svc.RemoveFriend(context.Background(), gameA, "u_1", "Ali", strings.ToUpper(otherID)); err != nil {
		t.Fatal(err)
	}
	if store.game != gameA || store.player != me || store.other != otherID {
		t.Errorf("store got game=%q player=%q friend=%q", store.game, store.player, store.other)
	}
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != EventFriendRemoved || evs[0].GameID != gameA {
		t.Fatalf("events = %+v, want one %s", evs, EventFriendRemoved)
	}
	players, _ := evs[0].Data["players"].([]map[string]any)
	if len(players) != 2 || players[0]["id"] != otherID || players[0]["name"] != "Aysel" ||
		players[1]["id"] != me || players[1]["name"] != "Ali" {
		t.Errorf("event players = %+v", evs[0].Data["players"])
	}
	if evs[0].Data["removed_by"] != me {
		t.Errorf("removed_by = %v, want %s", evs[0].Data["removed_by"], me)
	}
}

func TestRemoveFriendErrors(t *testing.T) {
	cases := []struct {
		name     string
		playerID string
		storeErr error
		reaches  bool
	}{
		{"not a UUID", "u_42", nil, false},
		{"not friends", otherID, repository.ErrNotFriends, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFriendStore()
			store.err = c.storeErr
			svc, rec := friendService(store)
			err := svc.RemoveFriend(context.Background(), gameA, "u_1", "Ali", c.playerID)
			wantHTTPError(t, err, http.StatusNotFound, "not_friends")
			if (store.called > 0) != c.reaches {
				t.Errorf("store called %d times, reaches = %v", store.called, c.reaches)
			}
			if len(rec.Events()) != 0 {
				t.Errorf("published %+v after a failure, want nothing", rec.Events())
			}
		})
	}
}

package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
	checkAcceptedEvent(t, rec, otherID, me)
}

func checkAcceptedEvent(t *testing.T, rec *events.Recorder, a, b string) {
	t.Helper()
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != EventFriendAccepted || evs[0].GameID != gameA {
		t.Fatalf("events = %+v, want one %s", evs, EventFriendAccepted)
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
	checkAcceptedEvent(t, rec, otherID, me)
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

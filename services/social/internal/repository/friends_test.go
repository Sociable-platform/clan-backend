package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// friendsOf returns the IDs of playerID's friends, in ListFriends order.
func friendsOf(t *testing.T, r *Repository, gameID, playerID string) []string {
	t.Helper()
	friends, err := r.ListFriends(context.Background(), gameID, playerID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(friends))
	for _, f := range friends {
		ids = append(ids, f.ID)
	}
	return ids
}

// pairState counts the rows a pair has in each table. A consistent pair has at most one row in total.
func pairState(t *testing.T, r *Repository, gameID, a, b string) (requests, friendships int) {
	t.Helper()
	ctx := context.Background()
	if err := r.pool.QueryRow(ctx,
		`select count(*) from friend_requests where game_id = $1
		 and least(sender_id, receiver_id) = least($2::uuid, $3::uuid)
		 and greatest(sender_id, receiver_id) = greatest($2::uuid, $3::uuid)`,
		gameID, a, b).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := r.pool.QueryRow(ctx,
		`select count(*) from friendships where game_id = $1
		 and player_a_id = least($2::uuid, $3::uuid) and player_b_id = greatest($2::uuid, $3::uuid)`,
		gameID, a, b).Scan(&friendships); err != nil {
		t.Fatal(err)
	}
	return requests, friendships
}

func sendRequest(t *testing.T, r *Repository, gameID, from, to string) FriendRequest {
	t.Helper()
	res, err := r.SendFriendRequest(context.Background(), gameID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if res.Request == nil {
		t.Fatalf("SendFriendRequest = %+v, want a new request", res)
	}
	return *res.Request
}

func TestSendFriendRequestCreatesAPendingRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")

	req := sendRequest(t, r, game, ali, aysel)
	if req.ID == "" || req.CreatedAt.IsZero() {
		t.Errorf("request = %+v, want an ID and a time", req)
	}
	if req.Sender != (FriendPlayer{ali, "ali"}) || req.Receiver != (FriendPlayer{aysel, "aysel"}) {
		t.Errorf("request = %+v, want ali -> aysel with names", req)
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 1 || friends != 0 {
		t.Errorf("rows: %d requests, %d friendships; want 1, 0", reqs, friends)
	}
}

func TestSendFriendRequestErrors(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	stranger := newPlayer(t, r, otherGame, "stranger")

	cases := []struct {
		name string
		to   string
		want error
	}{
		{"yourself", ali, ErrFriendRequestToSelf},
		{"yourself in upper case", strings.ToUpper(ali), ErrFriendRequestToSelf},
		{"player of another game", stranger, ErrPlayerNotFound},
		{"unknown player", "00000000-0000-4000-8000-000000000000", ErrPlayerNotFound},
		{"not a UUID", "not-a-uuid", ErrPlayerNotFound},
	}
	for _, c := range cases {
		if _, err := r.SendFriendRequest(ctx, game, ali, c.to); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	sendRequest(t, r, game, ali, aysel)
	if _, err := r.SendFriendRequest(ctx, game, ali, aysel); !errors.Is(err, ErrFriendRequestExists) {
		t.Errorf("same request again: err = %v, want ErrFriendRequestExists", err)
	}
	if n := countRows(t, r, "friend_requests", game); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestSendFriendRequestBackAcceptsTheFirstOne(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	first := sendRequest(t, r, game, ali, aysel)

	// Aysel sends one back, with Ali's ID in upper case: it still counts as the same player.
	res, err := r.SendFriendRequest(context.Background(), game, aysel, strings.ToUpper(ali))
	if err != nil {
		t.Fatal(err)
	}
	if res.Request != nil || res.Accepted == nil {
		t.Fatalf("result = %+v, want the first request accepted", res)
	}
	if res.Accepted.RequestID != first.ID {
		t.Errorf("accepted request = %q, want the first request %q", res.Accepted.RequestID, first.ID)
	}
	got := []FriendPlayer{res.Accepted.PlayerA, res.Accepted.PlayerB}
	want := []FriendPlayer{{ali, "ali"}, {aysel, "aysel"}}
	if aysel < ali {
		want[0], want[1] = want[1], want[0]
	}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("friendship players = %+v, want %+v (smaller ID first)", got, want)
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 0 || friends != 1 {
		t.Errorf("rows: %d requests, %d friendships; want 0, 1", reqs, friends)
	}
}

func TestSendFriendRequestToAFriend(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	req := sendRequest(t, r, game, ali, aysel)
	if _, err := r.AcceptFriendRequest(ctx, game, req.ID, aysel); err != nil {
		t.Fatal(err)
	}

	for _, from := range [][2]string{{ali, aysel}, {aysel, ali}} {
		if _, err := r.SendFriendRequest(ctx, game, from[0], from[1]); !errors.Is(err, ErrAlreadyFriends) {
			t.Errorf("request between friends: err = %v, want ErrAlreadyFriends", err)
		}
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 0 || friends != 1 {
		t.Errorf("rows: %d requests, %d friendships; want 0, 1", reqs, friends)
	}
}

func TestAcceptFriendRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	req := sendRequest(t, r, game, ali, aysel)

	f, err := r.AcceptFriendRequest(ctx, game, req.ID, aysel)
	if err != nil {
		t.Fatal(err)
	}
	if f.CreatedAt.IsZero() || f.PlayerA.ID >= f.PlayerB.ID {
		t.Errorf("friendship = %+v, want a time and the smaller ID first", f)
	}
	if f.RequestID != req.ID {
		t.Errorf("friendship request = %q, want %q (the request that was accepted)", f.RequestID, req.ID)
	}
	if got := friendsOf(t, r, game, ali); len(got) != 1 || got[0] != aysel {
		t.Errorf("ali's friends = %v, want [aysel]", got)
	}
	if got := friendsOf(t, r, game, aysel); len(got) != 1 || got[0] != ali {
		t.Errorf("aysel's friends = %v, want [ali]", got)
	}
	if _, err := r.AcceptFriendRequest(ctx, game, req.ID, aysel); !errors.Is(err, ErrFriendRequestNotFound) {
		t.Errorf("accept twice: err = %v, want ErrFriendRequestNotFound", err)
	}
}

func TestAcceptFriendRequestErrors(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	ali, aysel, third := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel"), newPlayer(t, r, game, "third")
	req := sendRequest(t, r, game, ali, aysel)

	cases := []struct {
		name          string
		game, id, who string
		want          error
	}{
		{"the sender", game, req.ID, ali, ErrNotFriendRequestReceiver},
		{"a third player", game, req.ID, third, ErrFriendRequestNotFound},
		{"from another game", otherGame, req.ID, aysel, ErrFriendRequestNotFound},
		{"unknown request", game, "00000000-0000-4000-8000-000000000000", aysel, ErrFriendRequestNotFound},
		{"not a UUID", game, "nope", aysel, ErrFriendRequestNotFound},
	}
	for _, c := range cases {
		if _, err := r.AcceptFriendRequest(ctx, c.game, c.id, c.who); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 1 || friends != 0 {
		t.Errorf("rows after failed accepts: %d requests, %d friendships; want 1, 0", reqs, friends)
	}
}

func TestListFriendRequests(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	ali, aysel, murad, nuray := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel"),
		newPlayer(t, r, game, "murad"), newPlayer(t, r, game, "nuray")
	o1, o2 := newPlayer(t, r, otherGame, "o1"), newPlayer(t, r, otherGame, "o2")

	out := sendRequest(t, r, game, ali, aysel) // ali sent
	in := sendRequest(t, r, game, murad, ali)  // ali received
	sendRequest(t, r, game, murad, nuray)      // not ali's
	sendRequest(t, r, otherGame, o1, o2)       // another game

	reqs, err := r.ListFriendRequests(ctx, game, ali)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2: %+v", len(reqs), reqs)
	}
	byID := map[string]FriendRequest{reqs[0].ID: reqs[0], reqs[1].ID: reqs[1]}
	if got := byID[out.ID]; got.Sender.ID != ali || got.Receiver != (FriendPlayer{aysel, "aysel"}) {
		t.Errorf("outgoing = %+v", got)
	}
	if got := byID[in.ID]; got.Receiver.ID != ali || got.Sender != (FriendPlayer{murad, "murad"}) {
		t.Errorf("incoming = %+v", got)
	}
	if reqs[0].CreatedAt.Before(reqs[1].CreatedAt) {
		t.Error("requests are not newest first")
	}

	none, err := r.ListFriendRequests(ctx, game, newPlayer(t, r, game, "lonely"))
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("player without requests: %v, %v; want an empty list", none, err)
	}
}

func TestListFriendsSortedByName(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	me := newPlayer(t, r, game, "me")
	zara, bob, anna := newPlayer(t, r, game, "Zara"), newPlayer(t, r, game, "bob"), newPlayer(t, r, game, "Anna")
	for _, p := range []string{zara, bob, anna} {
		req := sendRequest(t, r, game, me, p)
		if _, err := r.AcceptFriendRequest(ctx, game, req.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	sendRequest(t, r, game, me, newPlayer(t, r, game, "pending")) // not a friend yet

	friends, err := r.ListFriends(ctx, game, me)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range friends {
		names = append(names, f.DisplayName)
		if f.Since.IsZero() {
			t.Errorf("%s has no Since", f.DisplayName)
		}
	}
	if strings.Join(names, ",") != "Anna,bob,Zara" {
		t.Errorf("friends = %v, want Anna, bob, Zara", names)
	}
	if got := friendsOf(t, r, game, newPlayer(t, r, game, "lonely")); got == nil || len(got) != 0 {
		t.Errorf("player without friends: %v, want an empty list", got)
	}
}

// Both players press "add friend" at the same moment, many times over: each pair must end as
// exactly one request or one friendship, never two rows.
func TestSendFriendRequestsToEachOtherAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a, b := newPlayer(t, r, game, "a"+string(rune('a'+i))), newPlayer(t, r, game, "b"+string(rune('a'+i)))
		ok, _ := race(t, 2, func(n int) error {
			from, to := a, b
			if n == 1 {
				from, to = b, a
			}
			_, err := r.SendFriendRequest(ctx, game, from, to)
			return err
		}, ErrFriendRequestExists)
		if ok != 2 {
			t.Errorf("pair %d: %d sends succeeded, want both (one creates, the other accepts)", i, ok)
		}
		if reqs, friends := pairState(t, r, game, a, b); reqs != 0 || friends != 1 {
			t.Errorf("pair %d: %d requests, %d friendships; want 0, 1", i, reqs, friends)
		}
	}
}

func TestSameFriendRequestTwiceAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")

	ok, exists := race(t, 5, func(int) error {
		_, err := r.SendFriendRequest(context.Background(), game, ali, aysel)
		return err
	}, ErrFriendRequestExists)
	if ok != 1 || exists != 4 {
		t.Errorf("ok = %d, exists = %d; want 1 and 4", ok, exists)
	}
	if reqs, _ := pairState(t, r, game, ali, aysel); reqs != 1 {
		t.Errorf("%d requests, want 1", reqs)
	}
}

// The receiver accepts while they also send a request back: one friendship, nothing pending.
func TestAcceptWhileSendingBackAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a, b := newPlayer(t, r, game, "x"+string(rune('a'+i))), newPlayer(t, r, game, "y"+string(rune('a'+i)))
		req := sendRequest(t, r, game, a, b)
		race(t, 2, func(n int) error {
			var err error
			if n == 0 {
				_, err = r.AcceptFriendRequest(ctx, game, req.ID, b)
			} else {
				_, err = r.SendFriendRequest(ctx, game, b, a)
			}
			// whichever runs second finds the pair already friends or the request gone
			if errors.Is(err, ErrAlreadyFriends) {
				return ErrFriendRequestNotFound
			}
			return err
		}, ErrFriendRequestNotFound)
		if reqs, friends := pairState(t, r, game, a, b); reqs != 0 || friends != 1 {
			t.Errorf("pair %d: %d requests, %d friendships; want 0, 1", i, reqs, friends)
		}
	}
}

// makeFriends makes a and b friends through a request and its accept.
func makeFriends(t *testing.T, r *Repository, gameID, a, b string) {
	t.Helper()
	req := sendRequest(t, r, gameID, a, b)
	if _, err := r.AcceptFriendRequest(context.Background(), gameID, req.ID, b); err != nil {
		t.Fatal(err)
	}
}

func TestDeclineFriendRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	req := sendRequest(t, r, game, ali, aysel)

	got, err := r.DeclineFriendRequest(ctx, game, req.ID, aysel)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != req.ID || got.Sender.ID != ali || got.Sender.DisplayName != "ali" || got.Receiver.ID != aysel || got.Receiver.DisplayName != "aysel" || got.CreatedAt.IsZero() {
		t.Errorf("declined request = %+v, want the request %s from ali to aysel with names", got, req.ID)
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 0 || friends != 0 {
		t.Errorf("rows: %d requests, %d friendships; want 0, 0", reqs, friends)
	}
	if _, err := r.DeclineFriendRequest(ctx, game, req.ID, aysel); !errors.Is(err, ErrFriendRequestNotFound) {
		t.Errorf("decline twice: err = %v, want ErrFriendRequestNotFound", err)
	}
	// strangers again: Ali may ask once more
	sendRequest(t, r, game, ali, aysel)
}

func TestCancelFriendRequest(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	req := sendRequest(t, r, game, ali, aysel)

	got, err := r.CancelFriendRequest(ctx, game, req.ID, ali)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != req.ID || got.Sender.ID != ali || got.Receiver.ID != aysel || got.Sender.DisplayName == "" || got.Receiver.DisplayName == "" {
		t.Errorf("cancelled request = %+v, want the request %s from ali to aysel with names", got, req.ID)
	}
	if reqs, _ := pairState(t, r, game, ali, aysel); reqs != 0 {
		t.Errorf("%d requests after cancel, want 0", reqs)
	}
	sendRequest(t, r, game, aysel, ali) // either of them may ask again
}

func TestDeclineAndCancelErrors(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	ali, aysel, third := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel"), newPlayer(t, r, game, "third")
	req := sendRequest(t, r, game, ali, aysel)

	cases := []struct {
		name string
		fn   func(ctx context.Context, gameID, requestID, playerID string) (FriendRequest, error)
		game string
		id   string
		who  string
		want error
	}{
		{"sender declines", r.DeclineFriendRequest, game, req.ID, ali, ErrNotFriendRequestReceiver},
		{"receiver cancels", r.CancelFriendRequest, game, req.ID, aysel, ErrNotFriendRequestSender},
		{"third player declines", r.DeclineFriendRequest, game, req.ID, third, ErrFriendRequestNotFound},
		{"third player cancels", r.CancelFriendRequest, game, req.ID, third, ErrFriendRequestNotFound},
		{"decline from another game", r.DeclineFriendRequest, otherGame, req.ID, aysel, ErrFriendRequestNotFound},
		{"cancel from another game", r.CancelFriendRequest, otherGame, req.ID, ali, ErrFriendRequestNotFound},
		{"unknown request", r.DeclineFriendRequest, game, "00000000-0000-4000-8000-000000000000", aysel, ErrFriendRequestNotFound},
		{"not a UUID", r.CancelFriendRequest, game, "nope", ali, ErrFriendRequestNotFound},
	}
	for _, c := range cases {
		if _, err := c.fn(ctx, c.game, c.id, c.who); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if reqs, _ := pairState(t, r, game, ali, aysel); reqs != 1 {
		t.Errorf("%d requests after failed attempts, want 1", reqs)
	}
}

func TestRemoveFriend(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	makeFriends(t, r, game, ali, aysel)

	f, err := r.RemoveFriend(ctx, game, aysel, ali) // either friend may remove
	if err != nil {
		t.Fatal(err)
	}
	want := []FriendPlayer{{ali, "ali"}, {aysel, "aysel"}}
	if aysel < ali {
		want[0], want[1] = want[1], want[0]
	}
	if f.PlayerA != want[0] || f.PlayerB != want[1] || f.CreatedAt.IsZero() {
		t.Errorf("removed friendship = %+v, want players %+v (smaller ID first)", f, want)
	}
	if got := friendsOf(t, r, game, ali); len(got) != 0 {
		t.Errorf("ali still has friends %v", got)
	}
	if got := friendsOf(t, r, game, aysel); len(got) != 0 {
		t.Errorf("aysel still has friends %v", got)
	}
	if _, err := r.RemoveFriend(ctx, game, ali, aysel); !errors.Is(err, ErrNotFriends) {
		t.Errorf("remove twice: err = %v, want ErrNotFriends", err)
	}
	sendRequest(t, r, game, ali, aysel) // strangers again
}

func TestRemoveFriendErrors(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	pending := newPlayer(t, r, game, "pending")
	sendRequest(t, r, game, ali, pending)
	makeFriends(t, r, game, ali, aysel)
	o1, o2 := newPlayer(t, r, otherGame, "o1"), newPlayer(t, r, otherGame, "o2")
	makeFriends(t, r, otherGame, o1, o2)

	cases := []struct {
		name, game, who, friend string
	}{
		{"only a pending request", game, ali, pending},
		{"yourself", game, ali, ali},
		{"unknown player", game, ali, "00000000-0000-4000-8000-000000000000"},
		{"not a UUID", game, ali, "nope"},
		{"friends of another game", game, o1, o2},
	}
	for _, c := range cases {
		if _, err := r.RemoveFriend(ctx, c.game, c.who, c.friend); !errors.Is(err, ErrNotFriends) {
			t.Errorf("%s: err = %v, want ErrNotFriends", c.name, err)
		}
	}
	if reqs, friends := pairState(t, r, game, ali, aysel); reqs != 0 || friends != 1 {
		t.Errorf("ali and aysel: %d requests, %d friendships; want 0, 1", reqs, friends)
	}
	if _, friends := pairState(t, r, otherGame, o1, o2); friends != 1 {
		t.Error("the other game's friendship was touched")
	}
}

// Accept and cancel of the same request at the same moment: exactly one of them wins.
func TestAcceptAndCancelAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a, b := newPlayer(t, r, game, "a"+string(rune('a'+i))), newPlayer(t, r, game, "b"+string(rune('a'+i)))
		req := sendRequest(t, r, game, a, b)
		ok, lost := race(t, 2, func(n int) error {
			if n == 0 {
				_, err := r.AcceptFriendRequest(ctx, game, req.ID, b)
				return err
			}
			_, err := r.CancelFriendRequest(ctx, game, req.ID, a)
			return err
		}, ErrFriendRequestNotFound)
		if ok != 1 || lost != 1 {
			t.Errorf("pair %d: %d succeeded, %d found nothing; want 1 and 1", i, ok, lost)
		}
		reqs, friends := pairState(t, r, game, a, b)
		if reqs != 0 || friends > 1 {
			t.Errorf("pair %d: %d requests, %d friendships; want no request and at most one friendship", i, reqs, friends)
		}
	}
}

func TestRemoveFriendTwiceAtTheSameTime(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ali, aysel := newPlayer(t, r, game, "ali"), newPlayer(t, r, game, "aysel")
	makeFriends(t, r, game, ali, aysel)

	ok, notFriends := race(t, 2, func(n int) error {
		who, other := ali, aysel
		if n == 1 {
			who, other = aysel, ali
		}
		_, err := r.RemoveFriend(context.Background(), game, who, other)
		return err
	}, ErrNotFriends)
	if ok != 1 || notFriends != 1 {
		t.Errorf("ok = %d, not friends = %d; want 1 and 1", ok, notFriends)
	}
}

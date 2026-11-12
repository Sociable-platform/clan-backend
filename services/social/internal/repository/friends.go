package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrPlayerNotFound means there is no player with this ID in the game.
	// A player of another game is "not found" too, so players cannot reach other games.
	ErrPlayerNotFound = errors.New("player not found")
	// ErrFriendRequestToSelf means a player sent a friend request to themselves.
	ErrFriendRequestToSelf = errors.New("friend request to yourself")
	// ErrAlreadyFriends means the two players are friends already.
	ErrAlreadyFriends = errors.New("already friends")
	// ErrFriendRequestExists means the player already sent a pending request to this player.
	ErrFriendRequestExists = errors.New("friend request already sent")
	// ErrFriendRequestNotFound means there is no pending request with this ID that the player
	// may see: it does not exist, belongs to another game, or is between two other players.
	ErrFriendRequestNotFound = errors.New("friend request not found")
	// ErrNotFriendRequestReceiver means the player sent the request, so they cannot accept
	// (or decline) it.
	ErrNotFriendRequestReceiver = errors.New("only the receiver can accept or decline a friend request")
	// ErrNotFriendRequestSender means the player received the request, so they cannot cancel it.
	ErrNotFriendRequestSender = errors.New("only the sender can cancel a friend request")
	// ErrNotFriends means the two players are not friends (or the other one does not exist in the game).
	ErrNotFriends = errors.New("not friends")
	// ErrPlayerBlocked means the sender blocked the receiver: they unblock first.
	ErrPlayerBlocked = errors.New("you blocked this player")
)

// FriendPlayer is a player as the friends feature shows them.
type FriendPlayer struct {
	ID          string
	DisplayName string
}

// FriendRequest is a pending friend request.
type FriendRequest struct {
	ID        string
	Sender    FriendPlayer
	Receiver  FriendPlayer
	CreatedAt time.Time
}

// Friendship is two players who are friends. PlayerA has the smaller ID, as in the table.
type Friendship struct {
	PlayerA   FriendPlayer
	PlayerB   FriendPlayer
	CreatedAt time.Time
	RequestID string // the friend request that became this friendship; empty when it did not come from one (RemoveFriend)
}

// Friend is one of a player's friends.
type Friend struct {
	FriendPlayer
	Since time.Time
}

// SendFriendRequestResult tells what SendFriendRequest did. Exactly one field is set.
type SendFriendRequestResult struct {
	Request  *FriendRequest // a new pending request was created
	Accepted *Friendship    // the receiver had already asked the sender, so that request was accepted
}

// lockPair takes a transaction-level lock on the pair (gameID, a, b), whichever order a and b
// come in. Every change to a pair's requests or friendship takes it first, so two of them can
// never run at the same time for one pair; the lock is released when the transaction ends.
// Different pairs never wait for each other (except, very rarely, when two keys share a hash).
const lockPairSQL = `select pg_advisory_xact_lock(hashtextextended(
	$1::text || ':' || least($2::uuid, $3::uuid)::text || ':' || greatest($2::uuid, $3::uuid)::text, 0))`

// SendFriendRequest sends a friend request from senderID to receiverID in gameID.
//
// If receiverID already sent senderID a pending request, that request is accepted instead and
// the two become friends (Accepted is set). Otherwise a new pending request is created
// (Request is set). Everything happens in one transaction, with the pair locked, so requests
// sent by both players at the same moment end as one request or as friends, never as two rows.
//
// A block stops it: if the receiver blocked the sender, the answer is ErrPlayerNotFound, as if
// the receiver did not exist (the block stays hidden); if the sender blocked the receiver, it
// is ErrPlayerBlocked (they unblock first).
//
// Errors: ErrPlayerNotFound (receiver missing, in another game, not a UUID, or has blocked the
// sender), ErrFriendRequestToSelf, ErrPlayerBlocked, ErrAlreadyFriends, ErrFriendRequestExists.
func (r *Repository) SendFriendRequest(ctx context.Context, gameID, senderID, receiverID string) (SendFriendRequestResult, error) {
	if r.pool == nil {
		return SendFriendRequestResult{}, ErrNoDatabase
	}
	if senderID == receiverID {
		return SendFriendRequestResult{}, ErrFriendRequestToSelf
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return SendFriendRequestResult{}, fmt.Errorf("send friend request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Both players, with their names. "and game_id = $2" makes another game's player look
	//    like it does not exist.
	sender, err := friendPlayer(ctx, tx, gameID, senderID)
	if err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	receiver, err := friendPlayer(ctx, tx, gameID, receiverID)
	if err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	if sender.ID == receiver.ID { // the same UUID written differently
		return SendFriendRequestResult{}, ErrFriendRequestToSelf
	}
	senderID, receiverID = sender.ID, receiver.ID

	// 2. Lock the pair: from here on nobody else changes these two players' requests or friendship.
	if _, err := tx.Exec(ctx, lockPairSQL, gameID, senderID, receiverID); err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}

	// 3. A block either way? Checked under the lock, so a block at the same moment cannot slip by.
	var receiverBlocked, senderBlocked bool
	err = tx.QueryRow(ctx,
		`select exists (select 1 from blocks where game_id = $1 and blocker_id = $3 and blocked_id = $2),
		        exists (select 1 from blocks where game_id = $1 and blocker_id = $2 and blocked_id = $3)`,
		gameID, senderID, receiverID).Scan(&receiverBlocked, &senderBlocked)
	if err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	if receiverBlocked {
		return SendFriendRequestResult{}, ErrPlayerNotFound
	}
	if senderBlocked {
		return SendFriendRequestResult{}, ErrPlayerBlocked
	}

	// 4. Already friends? No table rule can check this across the two tables; the lock makes it safe.
	var friends bool
	err = tx.QueryRow(ctx,
		`select exists (select 1 from friendships
		 where game_id = $1 and player_a_id = least($2::uuid, $3::uuid) and player_b_id = greatest($2::uuid, $3::uuid))`,
		gameID, senderID, receiverID).Scan(&friends)
	if err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	if friends {
		return SendFriendRequestResult{}, ErrAlreadyFriends
	}

	// 5. A pending request between the two, in either direction?
	var existingID, existingSender string
	err = tx.QueryRow(ctx,
		`select id::text, sender_id::text from friend_requests
		 where game_id = $1
		   and least(sender_id, receiver_id) = least($2::uuid, $3::uuid)
		   and greatest(sender_id, receiver_id) = greatest($2::uuid, $3::uuid)`,
		gameID, senderID, receiverID).Scan(&existingID, &existingSender)
	switch {
	case err == nil && existingSender == senderID:
		return SendFriendRequestResult{}, ErrFriendRequestExists
	case err == nil:
		// 6a. The receiver asked first: accept their request.
		f, err := acceptLocked(ctx, tx, gameID, existingID, receiver, sender)
		if err != nil {
			return SendFriendRequestResult{}, sendFriendRequestError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return SendFriendRequestResult{}, sendFriendRequestError(err)
		}
		return SendFriendRequestResult{Accepted: &f}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}

	// 6b. No request yet: create one.
	req := FriendRequest{Sender: sender, Receiver: receiver}
	err = tx.QueryRow(ctx,
		`insert into friend_requests (game_id, sender_id, receiver_id) values ($1, $2, $3)
		 returning id::text, created_at`,
		gameID, senderID, receiverID).Scan(&req.ID, &req.CreatedAt)
	if err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SendFriendRequestResult{}, sendFriendRequestError(err)
	}
	return SendFriendRequestResult{Request: &req}, nil
}

// friendPlayer reads one player of gameID. The returned ID is the database's own spelling
// (lower case), even if playerID was sent in upper case.
// Errors: pgx.ErrNoRows, or 22P02 for an ID that is not a UUID.
func friendPlayer(ctx context.Context, tx pgx.Tx, gameID, playerID string) (FriendPlayer, error) {
	var p FriendPlayer
	err := tx.QueryRow(ctx,
		`select id::text, display_name from players where id = $1 and game_id = $2`,
		playerID, gameID).Scan(&p.ID, &p.DisplayName)
	return p, err
}

// acceptLocked turns request requestID (from sender to receiver) into a friendship.
// The caller holds the pair lock and commits.
func acceptLocked(ctx context.Context, tx pgx.Tx, gameID, requestID string, sender, receiver FriendPlayer) (Friendship, error) {
	if _, err := tx.Exec(ctx, `delete from friend_requests where id = $1`, requestID); err != nil {
		return Friendship{}, err
	}
	a, b := sender, receiver
	if b.ID < a.ID { // UUIDs compare the same way as their lower-case text
		a, b = b, a
	}
	f := Friendship{PlayerA: a, PlayerB: b, RequestID: requestID}
	err := tx.QueryRow(ctx,
		`insert into friendships (game_id, player_a_id, player_b_id) values ($1, $2, $3) returning created_at`,
		gameID, a.ID, b.ID).Scan(&f.CreatedAt)
	return f, err
}

// sendFriendRequestError turns database errors into our own errors where it can.
func sendFriendRequestError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPlayerNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "22P02": // invalid_text_representation: the player ID is not a UUID
			return ErrPlayerNotFound
		case "23505": // unique_violation: only possible without the pair lock, kept as a safety net
			return ErrFriendRequestExists
		}
	}
	return fmt.Errorf("send friend request: %w", err)
}

// AcceptFriendRequest accepts request requestID for playerID, who must be its receiver,
// and returns the new friendship. The request is deleted and the friendship created in one
// transaction, with the pair locked.
//
// Errors: ErrFriendRequestNotFound (missing, another game, not a UUID, or between two other
// players), ErrNotFriendRequestReceiver (playerID sent it).
func (r *Repository) AcceptFriendRequest(ctx context.Context, gameID, requestID, playerID string) (Friendship, error) {
	if r.pool == nil {
		return Friendship{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Friendship{}, fmt.Errorf("accept friend request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Find the pair, so it can be locked first, like every other change to it.
	var senderID, receiverID string
	err = tx.QueryRow(ctx,
		`select sender_id::text, receiver_id::text from friend_requests where id = $1 and game_id = $2`,
		requestID, gameID).Scan(&senderID, &receiverID)
	if err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	if _, err := tx.Exec(ctx, lockPairSQL, gameID, senderID, receiverID); err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}

	// 2. Read it again under the lock: it may have been accepted or cancelled while we waited.
	err = tx.QueryRow(ctx,
		`select sender_id::text, receiver_id::text from friend_requests where id = $1 and game_id = $2`,
		requestID, gameID).Scan(&senderID, &receiverID)
	if err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	switch playerID {
	case receiverID:
	case senderID:
		return Friendship{}, ErrNotFriendRequestReceiver
	default:
		return Friendship{}, ErrFriendRequestNotFound
	}

	// 3. Names for the result, then the request becomes a friendship.
	sender, err := friendPlayer(ctx, tx, gameID, senderID)
	if err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	receiver, err := friendPlayer(ctx, tx, gameID, receiverID)
	if err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	f, err := acceptLocked(ctx, tx, gameID, requestID, sender, receiver)
	if err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Friendship{}, acceptFriendRequestError(err)
	}
	return f, nil
}

// acceptFriendRequestError turns database errors into our own errors where it can.
func acceptFriendRequestError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFriendRequestNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // the request ID is not a UUID
		return ErrFriendRequestNotFound
	}
	return fmt.Errorf("accept friend request: %w", err)
}

// ListFriendRequests returns the pending requests playerID sent or received in gameID,
// newest first (ties: by ID). The caller tells incoming from outgoing by Sender.ID.
func (r *Repository) ListFriendRequests(ctx context.Context, gameID, playerID string) ([]FriendRequest, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.pool.Query(ctx,
		`select fr.id::text, fr.created_at,
		        s.id::text, s.display_name, rc.id::text, rc.display_name
		 from friend_requests fr
		 join players s  on s.id = fr.sender_id
		 join players rc on rc.id = fr.receiver_id
		 where fr.game_id = $1 and (fr.sender_id = $2 or fr.receiver_id = $2)
		 order by fr.created_at desc, fr.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("list friend requests: %w", err)
	}
	defer rows.Close()

	requests := make([]FriendRequest, 0)
	for rows.Next() {
		var q FriendRequest
		if err := rows.Scan(&q.ID, &q.CreatedAt, &q.Sender.ID, &q.Sender.DisplayName,
			&q.Receiver.ID, &q.Receiver.DisplayName); err != nil {
			return nil, fmt.Errorf("list friend requests: %w", err)
		}
		requests = append(requests, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list friend requests: %w", err)
	}
	return requests, nil
}

// ListFriends returns playerID's friends in gameID, sorted by name ignoring upper/lower case
// (ties: by ID).
func (r *Repository) ListFriends(ctx context.Context, gameID, playerID string) ([]Friend, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.pool.Query(ctx,
		`select p.id::text, p.display_name, f.created_at
		 from friendships f
		 join players p on p.id = case when f.player_a_id = $2 then f.player_b_id else f.player_a_id end
		 where f.game_id = $1 and (f.player_a_id = $2 or f.player_b_id = $2)
		 order by lower(p.display_name), p.id`,
		gameID, playerID)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	defer rows.Close()

	friends := make([]Friend, 0)
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.ID, &f.DisplayName, &f.Since); err != nil {
			return nil, fmt.Errorf("list friends: %w", err)
		}
		friends = append(friends, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	return friends, nil
}

// DeclineFriendRequest deletes request requestID, which must have been sent to playerID, and
// returns it (with both names, for the event).
//
// Errors: ErrFriendRequestNotFound (missing, another game, not a UUID, between two other
// players, or already accepted or cancelled), ErrNotFriendRequestReceiver (playerID sent it).
func (r *Repository) DeclineFriendRequest(ctx context.Context, gameID, requestID, playerID string) (FriendRequest, error) {
	return r.deleteFriendRequest(ctx, gameID, requestID, playerID, true)
}

// CancelFriendRequest deletes request requestID, which playerID must have sent, and returns it
// (with both names, for the event).
//
// Errors: ErrFriendRequestNotFound (as for DeclineFriendRequest), ErrNotFriendRequestSender
// (playerID received it).
func (r *Repository) CancelFriendRequest(ctx context.Context, gameID, requestID, playerID string) (FriendRequest, error) {
	return r.deleteFriendRequest(ctx, gameID, requestID, playerID, false)
}

// deleteFriendRequest deletes a pending request for its receiver (asReceiver) or its sender and
// returns it. Like every change to a pair, it locks the pair first, so a decline or cancel and an
// accept of the same request never both succeed.
func (r *Repository) deleteFriendRequest(ctx context.Context, gameID, requestID, playerID string, asReceiver bool) (FriendRequest, error) {
	if r.pool == nil {
		return FriendRequest{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return FriendRequest{}, fmt.Errorf("delete friend request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Find the pair and lock it.
	var senderID, receiverID string
	err = tx.QueryRow(ctx,
		`select sender_id::text, receiver_id::text from friend_requests where id = $1 and game_id = $2`,
		requestID, gameID).Scan(&senderID, &receiverID)
	if err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	if _, err := tx.Exec(ctx, lockPairSQL, gameID, senderID, receiverID); err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}

	// 2. Under the lock: is it still there, and is the player allowed to delete it?
	req := FriendRequest{ID: requestID}
	err = tx.QueryRow(ctx,
		`select sender_id::text, receiver_id::text, created_at from friend_requests where id = $1 and game_id = $2`,
		requestID, gameID).Scan(&senderID, &receiverID, &req.CreatedAt)
	if err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	switch {
	case asReceiver && playerID == receiverID, !asReceiver && playerID == senderID:
	case asReceiver && playerID == senderID:
		return FriendRequest{}, ErrNotFriendRequestReceiver
	case !asReceiver && playerID == receiverID:
		return FriendRequest{}, ErrNotFriendRequestSender
	default:
		return FriendRequest{}, ErrFriendRequestNotFound
	}

	// 3. Names for the result, then the request is deleted.
	if req.Sender, err = friendPlayer(ctx, tx, gameID, senderID); err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	if req.Receiver, err = friendPlayer(ctx, tx, gameID, receiverID); err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	if _, err := tx.Exec(ctx, `delete from friend_requests where id = $1`, requestID); err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FriendRequest{}, acceptFriendRequestError(err)
	}
	return req, nil
}

// RemoveFriend ends the friendship of playerID and friendID in gameID, for both of them, and
// returns it (with both names, for the event). Afterwards the two are strangers again and either
// may send a new request. The pair is locked first, like every other change to it.
//
// Errors: ErrNotFriends (not friends, friendID missing, in another game, or not a UUID).
func (r *Repository) RemoveFriend(ctx context.Context, gameID, playerID, friendID string) (Friendship, error) {
	if r.pool == nil {
		return Friendship{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Friendship{}, fmt.Errorf("remove friend: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, lockPairSQL, gameID, playerID, friendID); err != nil {
		return Friendship{}, removeFriendError(err)
	}

	f := Friendship{}
	err = tx.QueryRow(ctx,
		`delete from friendships f
		 using players a, players b
		 where f.game_id = $1
		   and f.player_a_id = least($2::uuid, $3::uuid) and f.player_b_id = greatest($2::uuid, $3::uuid)
		   and a.id = f.player_a_id and b.id = f.player_b_id
		 returning a.id::text, a.display_name, b.id::text, b.display_name, f.created_at`,
		gameID, playerID, friendID).Scan(&f.PlayerA.ID, &f.PlayerA.DisplayName,
		&f.PlayerB.ID, &f.PlayerB.DisplayName, &f.CreatedAt)
	if err != nil {
		return Friendship{}, removeFriendError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Friendship{}, removeFriendError(err)
	}
	return f, nil
}

// removeFriendError turns database errors into our own errors where it can.
func removeFriendError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFriends
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // the friend's ID is not a UUID
		return ErrNotFriends
	}
	return fmt.Errorf("remove friend: %w", err)
}

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
	// ErrClanOpen means the clan is open: a player joins it directly, there is nothing to ask.
	ErrClanOpen = errors.New("clan is open")
	// ErrAlreadyRequested means the player already has a pending request to this clan.
	ErrAlreadyRequested = errors.New("join request already sent")
	// ErrTooManyRequests means the player already has as many pending requests as one player may have.
	ErrTooManyRequests = errors.New("too many pending join requests")
	// ErrClanRequestsFull means the clan already has as many pending requests as one clan may have.
	ErrClanRequestsFull = errors.New("clan has too many pending join requests")
	// ErrRequestNotFound means there is no such pending request in this clan (it was accepted, declined,
	// taken back or dropped, or the ID is not one).
	ErrRequestNotFound = errors.New("join request not found")
)

// uniqueJoinRequest is the unique rule of 000011: one pending request per player and clan.
const uniqueJoinRequest = "clan_join_requests_clan_player_key"

// JoinRequest is a player's pending request to join a closed clan.
type JoinRequest struct {
	ID        string
	Player    ClanPlayer // who asks
	CreatedAt time.Time
}

// dropJoinRequests deletes every pending request of the player (a player who has a clan asks nothing
// more). It runs in the caller's transaction.
func dropJoinRequests(ctx context.Context, tx pgx.Tx, gameID, playerID string) error {
	_, err := tx.Exec(ctx,
		`delete from clan_join_requests where game_id = $1 and player_id = $2`, gameID, playerID)
	if err != nil {
		return fmt.Errorf("drop join requests: %w", err)
	}
	return nil
}

// SetJoinPolicy opens or closes clan clanID; only its owner may. Closing a clan changes nothing for its
// members. Opening a clan that has pending requests keeps them (the leaders can still accept them, and
// a player who joins directly loses theirs). It returns the clan as it is now.
//
// Errors: ErrClanNotFound, ErrNotClanOwner.
func (r *Repository) SetJoinPolicy(ctx context.Context, gameID, clanID, ownerExternalID, policy string) (Clan, error) {
	if r.pool == nil {
		return Clan{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return Clan{}, fmt.Errorf("set join policy: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var c Clan
	err = tx.QueryRow(ctx,
		`select id::text, name, description, badge, member_count, join_policy from clans
		 where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&c.ID, &c.Name, &c.Description, &c.Badge, &c.MemberCount, &c.JoinPolicy)
	if err != nil {
		return Clan{}, leaveClanError(err)
	}
	if _, err := clanOwner(ctx, tx, gameID, clanID, ownerExternalID); err != nil {
		return Clan{}, err
	}
	if c.JoinPolicy != policy {
		if _, err := tx.Exec(ctx, `update clans set join_policy = $2 where id = $1`, clanID, policy); err != nil {
			return Clan{}, fmt.Errorf("set join policy: %w", err)
		}
		c.JoinPolicy = policy
	}
	if err := tx.Commit(ctx); err != nil {
		return Clan{}, fmt.Errorf("set join policy: %w", err)
	}
	return c, nil
}

// JoinRequestResult is what RequestJoin saved.
type JoinRequestResult struct {
	Request JoinRequest
	Clan    ClanRef
	// Members are the clan's members with their roles, so the service can tell who may decide.
	Members []MemberRole
}

// RequestJoin saves a request of player playerID to join the closed clan clanID of gameID. The clan row
// is locked first, like a join, so the checks hold until the commit. maxMembers is the game's clan size
// (a full clan takes no requests: there would be nothing to accept), perClan how many pending requests a
// clan may have and perPlayer how many a player may.
//
// Errors, in this order: ErrClanNotFound, ErrClanOpen, ErrAlreadyInClan, ErrAlreadyRequested,
// ErrClanFull, ErrClanRequestsFull, ErrTooManyRequests.
func (r *Repository) RequestJoin(ctx context.Context, gameID, clanID, playerID string, maxMembers, perClan, perPlayer int) (JoinRequestResult, error) {
	if r.pool == nil {
		return JoinRequestResult{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return JoinRequestResult{}, fmt.Errorf("request join: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var res JoinRequestResult
	var policy string
	var members int
	err = tx.QueryRow(ctx,
		`select id::text, name, join_policy, member_count from clans
		 where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&res.Clan.ID, &res.Clan.Name, &policy, &members)
	if err != nil {
		return JoinRequestResult{}, joinClanError(err)
	}
	if policy != PolicyClosed {
		return JoinRequestResult{}, ErrClanOpen
	}

	var inClan, requested bool
	var clanPending, playerPending int
	err = tx.QueryRow(ctx,
		`select exists (select 1 from clan_members where game_id = $1 and player_id = $2),
		        exists (select 1 from clan_join_requests where clan_id = $3 and player_id = $2),
		        (select count(*) from clan_join_requests where clan_id = $3),
		        (select count(*) from clan_join_requests where game_id = $1 and player_id = $2)`,
		gameID, playerID, clanID).Scan(&inClan, &requested, &clanPending, &playerPending)
	if err != nil {
		return JoinRequestResult{}, fmt.Errorf("request join: %w", err)
	}
	switch {
	case inClan:
		return JoinRequestResult{}, ErrAlreadyInClan
	case requested:
		return JoinRequestResult{}, ErrAlreadyRequested
	case members >= maxMembers:
		return JoinRequestResult{}, ErrClanFull
	case clanPending >= perClan:
		return JoinRequestResult{}, ErrClanRequestsFull
	case playerPending >= perPlayer:
		return JoinRequestResult{}, ErrTooManyRequests
	}

	err = tx.QueryRow(ctx,
		`insert into clan_join_requests (clan_id, player_id, game_id) values ($1, $2, $3)
		 returning id::text, created_at`,
		clanID, playerID, gameID).Scan(&res.Request.ID, &res.Request.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == uniqueJoinRequest {
			return JoinRequestResult{}, ErrAlreadyRequested
		}
		return JoinRequestResult{}, fmt.Errorf("request join: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`select display_name from players where id = $1 and game_id = $2`, playerID, gameID).Scan(&res.Request.Player.DisplayName); err != nil {
		return JoinRequestResult{}, fmt.Errorf("request join: %w", err)
	}
	res.Request.Player.ID = playerID

	if res.Members, err = memberRoles(ctx, tx, gameID, clanID); err != nil {
		return JoinRequestResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return JoinRequestResult{}, fmt.Errorf("request join: %w", err)
	}
	return res, nil
}

// memberRoles reads every member of clan clanID with their stored role and whether they own it.
func memberRoles(ctx context.Context, tx pgx.Tx, gameID, clanID string) ([]MemberRole, error) {
	rows, err := tx.Query(ctx,
		`select m.player_id::text, p.display_name, m.role, c.owner_id = m.player_id
		 from clan_members m
		 join players p on p.id = m.player_id and p.game_id = m.game_id
		 join clans c   on c.id = m.clan_id
		 where m.clan_id = $1 and m.game_id = $2
		 order by m.joined_at, m.player_id`, clanID, gameID)
	if err != nil {
		return nil, fmt.Errorf("list the clan's members: %w", err)
	}
	defer rows.Close()
	var out []MemberRole
	for rows.Next() {
		var m MemberRole
		if err := rows.Scan(&m.Player.ID, &m.Player.DisplayName, &m.RoleID, &m.IsOwner); err != nil {
			return nil, fmt.Errorf("read a member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the clan's members: %w", err)
	}
	return out, nil
}

// CancelJoinRequest takes back the pending request of the player (gameID, externalID) to clan clanID.
// Errors: ErrClanNotFound (also another game's clan), ErrRequestNotFound.
func (r *Repository) CancelJoinRequest(ctx context.Context, gameID, clanID, externalID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	var clanExists bool
	if err := r.db(ctx).QueryRow(ctx,
		`select exists (select 1 from clans where id = $1 and game_id = $2)`, clanID, gameID).Scan(&clanExists); err != nil {
		return joinClanError(err) // also turns a clan ID that is not a UUID into ErrClanNotFound
	}
	if !clanExists {
		return ErrClanNotFound
	}
	tag, err := r.db(ctx).Exec(ctx,
		`delete from clan_join_requests q
		 using players p
		 where q.clan_id = $1 and q.game_id = $2 and p.id = q.player_id and p.game_id = q.game_id and p.external_id = $3`,
		clanID, gameID, externalID)
	if err != nil {
		return fmt.Errorf("cancel join request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRequestNotFound
	}
	return nil
}

// ListJoinRequests returns the pending requests of clan clanID, oldest first, to the member
// (gameID, actorExternalID) when allowed says their role may decide on them.
//
// Errors: ErrClanNotFound, ErrNotAllowed (the caller is not in the clan, or allowed said no).
func (r *Repository) ListJoinRequests(ctx context.Context, gameID, clanID, actorExternalID string, allowed func(actor MemberRole) bool) ([]JoinRequest, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("list join requests: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockClanFor(ctx, tx, gameID, clanID, false); err != nil {
		return nil, err
	}
	actor, err := clanMember(ctx, tx, gameID, clanID, memberByExternalID, actorExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotAllowed
	}
	if err != nil {
		return nil, fmt.Errorf("find the acting member: %w", err)
	}
	if !allowed(actor) {
		return nil, ErrNotAllowed
	}

	rows, err := tx.Query(ctx,
		`select q.id::text, q.player_id::text, p.display_name, q.created_at
		 from clan_join_requests q
		 join players p on p.id = q.player_id and p.game_id = q.game_id
		 where q.clan_id = $1 and q.game_id = $2
		 order by q.created_at, q.id`, clanID, gameID)
	if err != nil {
		return nil, fmt.Errorf("list join requests: %w", err)
	}
	defer rows.Close()
	out := make([]JoinRequest, 0)
	for rows.Next() {
		var q JoinRequest
		if err := rows.Scan(&q.ID, &q.Player.ID, &q.Player.DisplayName, &q.CreatedAt); err != nil {
			return nil, fmt.Errorf("read a join request: %w", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list join requests: %w", err)
	}
	return out, nil
}

// lockClanFor checks that clan clanID exists in gameID, locking its row when lock is true.
func lockClanFor(ctx context.Context, tx pgx.Tx, gameID, clanID string, lock bool) error {
	q := `select 1 from clans where id = $1 and game_id = $2`
	if lock {
		q += ` for update`
	}
	var one int
	if err := tx.QueryRow(ctx, q, clanID, gameID).Scan(&one); err != nil {
		return leaveClanError(err)
	}
	return nil
}

// RequestDecision is what accepting or declining a request did.
type RequestDecision struct {
	Clan   ClanRef
	Player ClanPlayer // whose request it was
	By     ClanPlayer // who decided
	// Joined is the clan with its new member count; only for an accepted request.
	Joined Clan
}

// decideRequest locks the clan, checks the caller may decide and loads and deletes the request, in the
// caller's transaction. The request's player is returned; ErrRequestNotFound when there is no such request.
func decideRequest(ctx context.Context, tx pgx.Tx, gameID, clanID, requestID, actorExternalID string,
	allowed func(actor MemberRole) bool) (RequestDecision, error) {
	var d RequestDecision
	err := tx.QueryRow(ctx,
		`select id::text, name from clans where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&d.Clan.ID, &d.Clan.Name)
	if err != nil {
		return RequestDecision{}, leaveClanError(err)
	}
	actor, err := clanMember(ctx, tx, gameID, clanID, memberByExternalID, actorExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestDecision{}, ErrNotAllowed
	}
	if err != nil {
		return RequestDecision{}, fmt.Errorf("find the acting member: %w", err)
	}
	if !allowed(actor) {
		return RequestDecision{}, ErrNotAllowed
	}
	d.By = actor.Player

	err = tx.QueryRow(ctx,
		`delete from clan_join_requests q
		 using players p
		 where q.id = $1 and q.clan_id = $2 and q.game_id = $3 and p.id = q.player_id and p.game_id = q.game_id
		 returning q.player_id::text, p.display_name`,
		requestID, clanID, gameID).Scan(&d.Player.ID, &d.Player.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestDecision{}, ErrRequestNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // the request ID is not a UUID
		return RequestDecision{}, ErrRequestNotFound
	}
	if err != nil {
		return RequestDecision{}, fmt.Errorf("find the join request: %w", err)
	}
	return d, nil
}

// AcceptJoinRequest lets the member (gameID, actorExternalID) accept request requestID of clan clanID, when
// allowed says their role may: the requester becomes a member with role roleID, in one transaction with
// the clan row locked (so the last free place goes to one player), and all their pending requests end.
// If the clan is full the request stays and the call fails; if the player has a clan by now, the request
// is dropped and the call fails.
//
// Errors: ErrClanNotFound, ErrNotAllowed, ErrRequestNotFound, ErrClanFull, ErrAlreadyInClan.
func (r *Repository) AcceptJoinRequest(ctx context.Context, gameID, clanID, requestID, actorExternalID string,
	maxMembers int, roleID string, allowed func(actor MemberRole) bool) (RequestDecision, error) {
	if r.pool == nil {
		return RequestDecision{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return RequestDecision{}, fmt.Errorf("accept join request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The request is deleted by decideRequest; a failure below rolls that back, so a full clan keeps it.
	d, err := decideRequest(ctx, tx, gameID, clanID, requestID, actorExternalID, allowed)
	if err != nil {
		return RequestDecision{}, err
	}

	var members int
	if err := tx.QueryRow(ctx, `select member_count from clans where id = $1`, clanID).Scan(&members); err != nil {
		return RequestDecision{}, fmt.Errorf("accept join request: %w", err)
	}
	var inClan bool
	if err := tx.QueryRow(ctx,
		`select exists (select 1 from clan_members where game_id = $1 and player_id = $2)`,
		gameID, d.Player.ID).Scan(&inClan); err != nil {
		return RequestDecision{}, fmt.Errorf("accept join request: %w", err)
	}
	if inClan {
		// Joining a clan drops the player's requests, so this is a race that lost; the request is gone.
		if err := tx.Commit(ctx); err != nil {
			return RequestDecision{}, fmt.Errorf("accept join request: %w", err)
		}
		return RequestDecision{}, ErrAlreadyInClan
	}
	if members >= maxMembers {
		return RequestDecision{}, ErrClanFull
	}

	if _, err := tx.Exec(ctx,
		`insert into clan_members (clan_id, player_id, game_id, role) values ($1, $2, $3, $4)`,
		clanID, d.Player.ID, gameID, roleID); err != nil {
		return RequestDecision{}, joinClanError(err)
	}
	if err := tx.QueryRow(ctx,
		`update clans set member_count = member_count + 1 where id = $1
		 returning id::text, name, description, badge, member_count, join_policy`,
		clanID).Scan(&d.Joined.ID, &d.Joined.Name, &d.Joined.Description, &d.Joined.Badge, &d.Joined.MemberCount, &d.Joined.JoinPolicy); err != nil {
		return RequestDecision{}, joinClanError(err)
	}
	if err := dropJoinRequests(ctx, tx, gameID, d.Player.ID); err != nil {
		return RequestDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RequestDecision{}, joinClanError(err)
	}
	return d, nil
}

// DeclineJoinRequest lets the member (gameID, actorExternalID) decline request requestID of clan clanID, when
// allowed says their role may. The request is deleted.
//
// Errors: ErrClanNotFound, ErrNotAllowed, ErrRequestNotFound.
func (r *Repository) DeclineJoinRequest(ctx context.Context, gameID, clanID, requestID, actorExternalID string,
	allowed func(actor MemberRole) bool) (RequestDecision, error) {
	if r.pool == nil {
		return RequestDecision{}, ErrNoDatabase
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return RequestDecision{}, fmt.Errorf("decline join request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d, err := decideRequest(ctx, tx, gameID, clanID, requestID, actorExternalID, allowed)
	if err != nil {
		return RequestDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RequestDecision{}, fmt.Errorf("decline join request: %w", err)
	}
	return d, nil
}

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Clan is a row of the clans table, with the fields shown in clan lists. How many members a
// clan may have is not stored per clan: it is the game's rule (clan.maxMembers in its
// published config), which the service passes in where it matters.
type Clan struct {
	ID   string
	Name string
	// Description is free text the owner wrote about the clan; empty when they wrote none.
	Description string
	// Badge is the id of a badge from the game's widget config, never a picture; empty
	// when no badge was chosen.
	Badge string
	// Tag is the clan's short tag, shown above its members' names; empty when the game
	// does not use tags.
	Tag         string
	MemberCount int
}

// ListClansOptions say how many clans ListClans returns, in which order, and which ones.
type ListClansOptions struct {
	Limit         int  // at most this many clans
	ByMembers     bool // most members first; otherwise sorted by name
	OnlyWithSpace bool // only clans that still have a free place
	MaxMembers    int  // the game's clan size; with OnlyWithSpace, a clan with this many members is full
}

// Two fixed orders. Nothing from the request is put into the SQL text: the options only
// choose between these two.
const (
	orderClansByName    = "lower(name), id"
	orderClansByMembers = "member_count desc, lower(name), id"
)

// ListClans returns up to opts.Limit clans of gameID, sorted by name (or by member count,
// biggest first, with ByMembers). Ties are broken by name and then ID, so the order is
// always the same.
// When query is not empty, only clans whose name contains it are returned, ignoring
// upper/lower case. strpos looks for the exact text, so characters like % and _ in the
// search are not wildcards (as they would be with LIKE).
// With OnlyWithSpace, full clans (opts.MaxMembers members or more) are left out.
func (r *Repository) ListClans(ctx context.Context, gameID, query string, opts ListClansOptions) ([]Clan, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	order := orderClansByName
	if opts.ByMembers {
		order = orderClansByMembers
	}
	rows, err := r.pool.Query(ctx,
		`select id::text, name, description, badge, tag, member_count
		 from clans
		 where game_id = $1
		   and ($2 = '' or strpos(lower(name), lower($2)) > 0)
		   and (not $3 or member_count < $5)
		 order by `+order+`
		 limit $4`,
		gameID, query, opts.OnlyWithSpace, opts.Limit, opts.MaxMembers,
	)
	if err != nil {
		return nil, fmt.Errorf("list clans: %w", err)
	}
	defer rows.Close()

	clans := make([]Clan, 0)
	for rows.Next() {
		var c Clan
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Badge, &c.Tag, &c.MemberCount); err != nil {
			return nil, fmt.Errorf("list clans: %w", err)
		}
		clans = append(clans, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list clans: %w", err)
	}
	return clans, nil
}

var (
	// ErrClanNameTaken means the game already has a clan with this name (ignoring upper/lower case).
	ErrClanNameTaken = errors.New("clan name taken")
	// ErrAlreadyInClan means the player is already a member of a clan in this game.
	ErrAlreadyInClan = errors.New("player already in a clan")
)

// Names of the unique rules in the migrations. When two requests race, the database
// is what stops the second one, and these names tell us which rule it broke.
const (
	uniqueClanName      = "clans_game_name_key"                // 000002: one name per game
	uniqueClanPerPlayer = "clan_members_game_id_player_id_key" // 000003: one clan per player per game
)

// NewClan is what a player chose for the clan they are creating. The service has already
// cleaned and checked all three; Description and Badge may be empty.
type NewClan struct {
	Name        string
	Description string
	Badge       string
	Tag         string
}

// CreateClan creates a clan in gameID owned by ownerID (social's player ID) and adds the
// owner as its first member, in one transaction: either both rows are saved or neither is.
//
// It returns ErrAlreadyInClan if the owner is already in a clan of this game, and
// ErrClanNameTaken if the game already has a clan with this name.
func (r *Repository) CreateClan(ctx context.Context, gameID, ownerID string, in NewClan) (Clan, error) {
	if r.pool == nil {
		return Clan{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}
	// Rollback does nothing after a successful Commit, so this only undoes failed attempts.
	defer func() { _ = tx.Rollback(ctx) }()

	// Friendly early check. It is not enough on its own (two requests can pass it at the
	// same moment), so the unique rule on clan_members is the real guard below.
	var inClan bool
	err = tx.QueryRow(ctx,
		`select exists (select 1 from clan_members where game_id = $1 and player_id = $2)`,
		gameID, ownerID).Scan(&inClan)
	if err != nil {
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}
	if inClan {
		return Clan{}, ErrAlreadyInClan
	}

	c := Clan{Name: in.Name, Description: in.Description, Badge: in.Badge, Tag: in.Tag}
	err = tx.QueryRow(ctx,
		`insert into clans (game_id, name, description, badge, tag, owner_id)
		 values ($1, $2, $3, $4, $5, $6)
		 returning id::text, member_count`,
		gameID, in.Name, in.Description, in.Badge, in.Tag, ownerID).Scan(&c.ID, &c.MemberCount)
	if err != nil {
		return Clan{}, createClanError(err)
	}

	_, err = tx.Exec(ctx,
		`insert into clan_members (clan_id, player_id, game_id, role) values ($1, $2, $3, 'owner')`,
		c.ID, ownerID, gameID)
	if err != nil {
		return Clan{}, createClanError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Clan{}, createClanError(err)
	}
	return c, nil
}

// createClanError turns "duplicate" errors from the database into our own errors.
func createClanError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // 23505 = unique_violation
		switch pgErr.ConstraintName {
		case uniqueClanName:
			return ErrClanNameTaken
		case uniqueClanPerPlayer:
			return ErrAlreadyInClan
		}
	}
	return fmt.Errorf("create clan: %w", err)
}

var (
	// ErrClanNotFound means there is no clan with this ID in the player's game.
	// A clan of another game is "not found" too, so players cannot see other games' clans.
	ErrClanNotFound = errors.New("clan not found")
	// ErrClanFull means the clan already has as many members as the game allows.
	ErrClanFull = errors.New("clan is full")
)

// JoinClan adds playerID to clan clanID of gameID with role roleID (the first role of the
// game's config: what a new member gets) and returns the
// clan with its new member count. maxMembers is the game's clan size (clan.maxMembers):
// a clan with that many members or more is full. A clan over a lowered limit keeps its
// members; it just takes no one new. Everything happens in one transaction.
//
// The clan row is locked first (SELECT ... FOR UPDATE). Other joins of the same clan
// wait at that line until this transaction ends, so two players can never both take
// the last free place.
//
// Errors: ErrClanNotFound, ErrAlreadyInClan, ErrClanFull.
func (r *Repository) JoinClan(ctx context.Context, gameID, clanID, playerID string, maxMembers int, roleID string) (Clan, error) {
	if r.pool == nil {
		return Clan{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Clan{}, fmt.Errorf("join clan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Lock the clan. "and game_id = $2" makes another game's clan look like it does not exist.
	c := Clan{ID: clanID}
	err = tx.QueryRow(ctx,
		`select name, description, badge, tag, member_count
		 from clans
		 where id = $1 and game_id = $2
		 for update`,
		clanID, gameID).Scan(&c.Name, &c.Description, &c.Badge, &c.Tag, &c.MemberCount)
	if err != nil {
		return Clan{}, joinClanError(err)
	}

	// 2. One clan per player per game (the unique rule below is the real guard).
	var inClan bool
	err = tx.QueryRow(ctx,
		`select exists (select 1 from clan_members where game_id = $1 and player_id = $2)`,
		gameID, playerID).Scan(&inClan)
	if err != nil {
		return Clan{}, joinClanError(err)
	}
	if inClan {
		return Clan{}, ErrAlreadyInClan
	}

	// 3. Room left? Safe to trust member_count here because we hold the lock.
	if c.MemberCount >= maxMembers {
		return Clan{}, ErrClanFull
	}

	// 4. Add the member and count them.
	_, err = tx.Exec(ctx,
		`insert into clan_members (clan_id, player_id, game_id, role) values ($1, $2, $3, $4)`,
		clanID, playerID, gameID, roleID)
	if err != nil {
		return Clan{}, joinClanError(err)
	}
	err = tx.QueryRow(ctx,
		`update clans set member_count = member_count + 1 where id = $1 returning member_count`,
		clanID).Scan(&c.MemberCount)
	if err != nil {
		return Clan{}, joinClanError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Clan{}, joinClanError(err)
	}
	return c, nil
}

// joinClanError turns database errors into our own errors where it can.
func joinClanError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrClanNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "22P02": // invalid_text_representation: the clan ID is not a UUID
			return ErrClanNotFound
		case pgErr.Code == "23505" && pgErr.ConstraintName == uniqueClanPerPlayer:
			return ErrAlreadyInClan
		}
	}
	return fmt.Errorf("join clan: %w", err)
}

// ErrNotInClan means the player is not a member of the clan asked about (MyClan: of any
// clan of the game; LeaveClan: of that clan).
var ErrNotInClan = errors.New("player is not in a clan")

// PlayerClan is the clan a player is in, and their role in it ("owner" or "member").
type PlayerClan struct {
	Clan Clan
	Role string
}

// MyClan returns the clan the player (gameID, externalID) is in, with their role.
// It only reads: a player we have never seen is simply not in a clan (ErrNotInClan).
func (r *Repository) MyClan(ctx context.Context, gameID, externalID string) (PlayerClan, error) {
	if r.pool == nil {
		return PlayerClan{}, ErrNoDatabase
	}
	var pc PlayerClan
	err := r.pool.QueryRow(ctx,
		`select c.id::text, c.name, c.description, c.badge, c.tag, c.member_count, m.role
		 from players p
		 join clan_members m on m.player_id = p.id and m.game_id = p.game_id
		 join clans c        on c.id = m.clan_id
		 where p.game_id = $1 and p.external_id = $2`,
		gameID, externalID,
	).Scan(&pc.Clan.ID, &pc.Clan.Name, &pc.Clan.Description, &pc.Clan.Badge, &pc.Clan.Tag,
		&pc.Clan.MemberCount, &pc.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlayerClan{}, ErrNotInClan
	}
	if err != nil {
		return PlayerClan{}, fmt.Errorf("my clan: %w", err)
	}
	return pc, nil
}

// Member is one member of a clan, as shown in the member list.
type Member struct {
	PlayerID    string // social's own player ID (not the game's ID such as "u_42")
	DisplayName string
	Role        string // the stored role id: "owner" for the owner, else a role id of the game's config (or "member")
	JoinedAt    time.Time
}

// ListMembers returns the members of clan clanID of gameID: the owner first, then the
// others in the order they joined. A clan of another game gives ErrClanNotFound, the
// same as a clan that does not exist.
func (r *Repository) ListMembers(ctx context.Context, gameID, clanID string) ([]Member, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	var exists bool
	err := r.pool.QueryRow(ctx,
		`select exists (select 1 from clans where id = $1 and game_id = $2)`,
		clanID, gameID).Scan(&exists)
	if err != nil {
		return nil, joinClanError(err) // also turns a non-UUID clanID into ErrClanNotFound
	}
	if !exists {
		return nil, ErrClanNotFound
	}

	rows, err := r.pool.Query(ctx,
		`select p.id::text, p.display_name, m.role, m.joined_at
		 from clan_members m
		 join players p on p.id = m.player_id and p.game_id = m.game_id
		 where m.clan_id = $1 and m.game_id = $2
		 order by m.role <> 'owner', m.joined_at, p.id`,
		clanID, gameID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]Member, 0)
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PlayerID, &m.DisplayName, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return members, nil
}

// ClanPlayer is a player as the clan events name them: social's player ID and display name.
type ClanPlayer struct {
	ID          string
	DisplayName string
}

// LeaveResult is what LeaveClan did.
type LeaveResult struct {
	ClanID   string
	ClanName string
	Player   ClanPlayer // who left
	// NewOwner is the member who took over because the owner left; nil when the owner
	// did not change (a normal member left, or the clan was disbanded).
	NewOwner  *ClanPlayer
	Disbanded bool // the player was the last member, so the clan was deleted
	// KickedBy is the owner who removed the player (KickMember); nil when the player left.
	KickedBy *ClanPlayer
}

// LeaveClan removes the player (gameID, externalID) from clan clanID. Everything happens in
// one transaction, and the clan row is locked first (like JoinClan), so member_count and
// the owner cannot get out of step when members join or leave at the same time.
//
//   - A normal member leaves: the membership is deleted and member_count goes down by one.
//   - The owner leaves and others remain: the member who joined first (ties: the lower
//     player ID) becomes the owner, in clans.owner_id and in clan_members.role, and the
//     old owner's membership is deleted.
//   - The owner is the only member: the clan is deleted.
//
// It never creates a player record: a player we have never seen is simply not a member.
// It returns what happened (who left, the new owner, whether the clan is gone), read in
// the same transaction, so the clan events tell exactly what was saved.
//
// Errors: ErrClanNotFound (also for another game's clan and for an ID that is not a UUID),
// ErrNotInClan.
func (r *Repository) LeaveClan(ctx context.Context, gameID, clanID, externalID string) (LeaveResult, error) {
	if r.pool == nil {
		return LeaveResult{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return LeaveResult{}, fmt.Errorf("leave clan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Lock the clan. "and game_id = $2" makes another game's clan look like it does not exist.
	var res LeaveResult
	err = tx.QueryRow(ctx,
		`select id::text, name from clans where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&res.ClanID, &res.ClanName)
	if err != nil {
		return LeaveResult{}, leaveClanError(err)
	}

	// 2. Delete the membership. No row means the player is not in this clan.
	var role string
	err = tx.QueryRow(ctx,
		`delete from clan_members m
		 using players p
		 where m.clan_id = $1 and m.game_id = $2
		   and p.id = m.player_id and p.game_id = m.game_id and p.external_id = $3
		 returning m.role, m.player_id::text, p.display_name`,
		clanID, gameID, externalID).Scan(&role, &res.Player.ID, &res.Player.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeaveResult{}, ErrNotInClan
	}
	if err != nil {
		return LeaveResult{}, leaveClanError(err)
	}

	// 3. Count down. Safe to trust member_count because we hold the lock.
	var left int
	err = tx.QueryRow(ctx,
		`update clans set member_count = member_count - 1 where id = $1 returning member_count`,
		clanID).Scan(&left)
	if err != nil {
		return LeaveResult{}, leaveClanError(err)
	}

	switch {
	case left == 0:
		// 4a. Nobody is left: the clan goes away (its member rows are already gone).
		if _, err := tx.Exec(ctx, `delete from clans where id = $1`, clanID); err != nil {
			return LeaveResult{}, leaveClanError(err)
		}
		res.Disbanded = true
	case role == "owner":
		// 4b. The owner left and others remain: the member who joined first takes over.
		var heir ClanPlayer
		err = tx.QueryRow(ctx,
			`select m.player_id::text, p.display_name
			 from clan_members m
			 join players p on p.id = m.player_id and p.game_id = m.game_id
			 where m.clan_id = $1
			 order by m.joined_at, m.player_id
			 limit 1`,
			clanID).Scan(&heir.ID, &heir.DisplayName)
		if err != nil {
			return LeaveResult{}, leaveClanError(err)
		}
		if _, err := tx.Exec(ctx,
			`update clan_members set role = 'owner' where clan_id = $1 and player_id = $2`,
			clanID, heir.ID); err != nil {
			return LeaveResult{}, leaveClanError(err)
		}
		if _, err := tx.Exec(ctx,
			`update clans set owner_id = $2 where id = $1`, clanID, heir.ID); err != nil {
			return LeaveResult{}, leaveClanError(err)
		}
		res.NewOwner = &heir
	}

	if err := tx.Commit(ctx); err != nil {
		return LeaveResult{}, leaveClanError(err)
	}
	return res, nil
}

// leaveClanError turns database errors into our own errors where it can.
func leaveClanError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrClanNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // the clan ID is not a UUID
		return ErrClanNotFound
	}
	return fmt.Errorf("leave clan: %w", err)
}

var (
	// ErrNotClanOwner means the caller is not the owner of the clan (or not in it at all).
	ErrNotClanOwner = errors.New("not the clan owner")
	// ErrKickSelf means the owner tried to kick themselves; they leave instead.
	ErrKickSelf = errors.New("cannot kick yourself")
	// ErrMemberNotFound means the player to kick is not a member of this clan.
	ErrMemberNotFound = errors.New("member not found")
)

// MemberRole is a clan member as the role rules see them.
type MemberRole struct {
	Player  ClanPlayer
	RoleID  string // as stored in clan_members.role
	IsOwner bool   // the clan's owner (clans.owner_id)
}

// ErrNotAllowed means the clan roles do not let the caller do this (or the caller is not a
// member of the clan at all).
var ErrNotAllowed = errors.New("not allowed by the clan roles")

// The two ways a member is looked up. Fixed texts: nothing from the request goes into the SQL.
const (
	memberByExternalID = "p.external_id = $3"
	memberByPlayerID   = "m.player_id = $3"
)

// clanMember reads one member of clan clanID with their stored role and whether they own it.
// pgx.ErrNoRows when there is no such member. Call it inside a transaction that has locked
// the clan row, so the answer stays true until the commit.
func clanMember(ctx context.Context, tx pgx.Tx, gameID, clanID, by, value string) (MemberRole, error) {
	var m MemberRole
	err := tx.QueryRow(ctx,
		`select m.player_id::text, p.display_name, m.role, c.owner_id = m.player_id
		 from clan_members m
		 join players p on p.id = m.player_id and p.game_id = m.game_id
		 join clans c   on c.id = m.clan_id
		 where m.clan_id = $1 and m.game_id = $2 and `+by,
		clanID, gameID, value).Scan(&m.Player.ID, &m.Player.DisplayName, &m.RoleID, &m.IsOwner)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // the player ID is not a UUID
		return MemberRole{}, pgx.ErrNoRows
	}
	return m, err
}

// lockClanAndMembers locks clan clanID of gameID and reads the acting member (by their
// game's player ID) and the target member (by social's player ID). Shared by kick and role
// changes, which check the same things in the same order:
// ErrClanNotFound, ErrNotAllowed (the actor is not in the clan), errSelf (the same player),
// ErrMemberNotFound.
func lockClanAndMembers(ctx context.Context, tx pgx.Tx, gameID, clanID, actorExternalID, playerID string) (ClanRef, MemberRole, MemberRole, error) {
	var clan ClanRef
	err := tx.QueryRow(ctx,
		`select id::text, name from clans where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&clan.ID, &clan.Name)
	if err != nil {
		return ClanRef{}, MemberRole{}, MemberRole{}, leaveClanError(err)
	}
	actor, err := clanMember(ctx, tx, gameID, clanID, memberByExternalID, actorExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClanRef{}, MemberRole{}, MemberRole{}, ErrNotAllowed
	}
	if err != nil {
		return ClanRef{}, MemberRole{}, MemberRole{}, fmt.Errorf("find the acting member: %w", err)
	}
	if strings.EqualFold(playerID, actor.Player.ID) {
		return ClanRef{}, MemberRole{}, MemberRole{}, errSelf
	}
	target, err := clanMember(ctx, tx, gameID, clanID, memberByPlayerID, playerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClanRef{}, MemberRole{}, MemberRole{}, ErrMemberNotFound
	}
	if err != nil {
		return ClanRef{}, MemberRole{}, MemberRole{}, fmt.Errorf("find the member: %w", err)
	}
	return clan, actor, target, nil
}

// ownerRole is the role stored for the clan's owner. Who owns the clan is clans.owner_id;
// the owner always has the last role of the game's config, whatever its id.
const ownerRole = "owner"

// errSelf is the caller naming themselves; each action turns it into its own error.
var errSelf = errors.New("the caller named themselves")

// ClanRef is a clan as events name it.
type ClanRef struct {
	ID   string
	Name string
}

// KickMember removes player playerID from clan clanID; the player (gameID, actorExternalID)
// does it, and allowed decides, by the clan roles, whether they may (see package roles). It
// runs in one transaction with the clan row locked, like join and leave, so a kick and a
// leave, a join or a role change at the same moment cannot get the clan out of step. Nobody
// can kick the owner (no role is above the owner's), so the owner never changes and the clan
// is never disbanded by a kick.
//
// Errors, checked in this order: ErrClanNotFound (also another game's clan or an ID that is
// not a UUID), ErrNotAllowed (the caller is not in the clan), ErrKickSelf, ErrMemberNotFound
// (also an ID that is not a UUID), ErrNotAllowed (the roles say no).
func (r *Repository) KickMember(ctx context.Context, gameID, clanID, actorExternalID, playerID string,
	allowed func(actor, target MemberRole) bool) (LeaveResult, error) {
	if r.pool == nil {
		return LeaveResult{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return LeaveResult{}, fmt.Errorf("kick member: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	clan, actor, target, err := lockClanAndMembers(ctx, tx, gameID, clanID, actorExternalID, playerID)
	if errors.Is(err, errSelf) {
		return LeaveResult{}, ErrKickSelf
	}
	if err != nil {
		return LeaveResult{}, err
	}
	if !allowed(actor, target) {
		return LeaveResult{}, ErrNotAllowed
	}

	if _, err := tx.Exec(ctx,
		`delete from clan_members where clan_id = $1 and player_id = $2`, clanID, target.Player.ID); err != nil {
		return LeaveResult{}, fmt.Errorf("kick member: %w", err)
	}
	// Count down. The owner is still a member, so the clan is never empty here.
	if _, err := tx.Exec(ctx,
		`update clans set member_count = member_count - 1 where id = $1`, clanID); err != nil {
		return LeaveResult{}, fmt.Errorf("kick member: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LeaveResult{}, fmt.Errorf("kick member: %w", err)
	}
	kickedBy := actor.Player
	return LeaveResult{ClanID: clan.ID, ClanName: clan.Name, Player: target.Player, KickedBy: &kickedBy}, nil
}

// RoleChange is what ChangeRole did.
type RoleChange struct {
	Clan           ClanRef
	Player         ClanPlayer // whose role changed
	PreviousRoleID string     // as it was stored
	NewRoleID      string
	ChangedBy      ClanPlayer
}

// ChangeRole gives player playerID of clan clanID a new role; the player (gameID,
// actorExternalID) does it. decide gets both members as they are now (inside the lock) and
// returns the new role id, or an error that ChangeRole returns as it is (see package roles:
// promote and demote). One transaction with the clan row locked, like join, leave and kick.
//
// Errors: ErrClanNotFound, ErrNotAllowed (the caller is not in the clan, or names
// themselves), ErrMemberNotFound, or decide's error.
func (r *Repository) ChangeRole(ctx context.Context, gameID, clanID, actorExternalID, playerID string,
	decide func(actor, target MemberRole) (string, error)) (RoleChange, error) {
	if r.pool == nil {
		return RoleChange{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return RoleChange{}, fmt.Errorf("change role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	clan, actor, target, err := lockClanAndMembers(ctx, tx, gameID, clanID, actorExternalID, playerID)
	if errors.Is(err, errSelf) {
		return RoleChange{}, ErrNotAllowed
	}
	if err != nil {
		return RoleChange{}, err
	}
	newRole, err := decide(actor, target)
	if err != nil {
		return RoleChange{}, err
	}
	// "owner" marks the owner's row; nobody else may get it, even if a game names a role so.
	if newRole == ownerRole || target.IsOwner {
		return RoleChange{}, ErrNotAllowed
	}
	if _, err := tx.Exec(ctx,
		`update clan_members set role = $3 where clan_id = $1 and player_id = $2`,
		clanID, target.Player.ID, newRole); err != nil {
		return RoleChange{}, fmt.Errorf("change role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RoleChange{}, fmt.Errorf("change role: %w", err)
	}
	return RoleChange{Clan: clan, Player: target.Player, PreviousRoleID: target.RoleID, NewRoleID: newRole,
		ChangedBy: actor.Player}, nil
}

// clanOwner returns the owner of clan clanID if that is the player (gameID, externalID), and
// ErrNotClanOwner otherwise (also for a player who is not in the clan). Call it inside a
// transaction that has locked the clan row, so the owner cannot change before the commit.
func clanOwner(ctx context.Context, tx pgx.Tx, gameID, clanID, externalID string) (ClanPlayer, error) {
	var owner ClanPlayer
	err := tx.QueryRow(ctx,
		`select m.player_id::text, p.display_name
		 from clan_members m
		 join players p on p.id = m.player_id and p.game_id = m.game_id
		 join clans c   on c.id = m.clan_id and c.owner_id = m.player_id
		 where m.clan_id = $1 and m.game_id = $2 and p.external_id = $3`,
		clanID, gameID, externalID).Scan(&owner.ID, &owner.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClanPlayer{}, ErrNotClanOwner
	}
	if err != nil {
		return ClanPlayer{}, fmt.Errorf("find clan owner: %w", err)
	}
	return owner, nil
}

// RenameResult is what RenameClan did.
type RenameResult struct {
	Clan         Clan   // the clan with its name now
	PreviousName string // the name before; equal to Clan.Name when nothing changed
	Changed      bool   // false when the clan already had exactly this name
}

// RenameClan gives clan clanID the name name; only the clan's owner, the player
// (gameID, ownerExternalID), may do it. The name must already be cleaned (service does it).
// The clan row is locked, like for join, leave and kick, so the owner cannot change while
// the name does. Changing only upper/lower case ("wolves" to "Wolves") is a change; the
// exact same name is not, and nothing is written.
//
// Errors, checked in this order: ErrClanNotFound (also another game's clan or an ID that is
// not a UUID), ErrNotClanOwner, ErrClanNameTaken (another clan of the game has the name,
// ignoring upper/lower case).
func (r *Repository) RenameClan(ctx context.Context, gameID, clanID, ownerExternalID, name string) (RenameResult, error) {
	if r.pool == nil {
		return RenameResult{}, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return RenameResult{}, fmt.Errorf("rename clan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Lock the clan.
	var res RenameResult
	c := &res.Clan
	err = tx.QueryRow(ctx,
		`select id::text, name, description, badge, tag, member_count from clans
		 where id = $1 and game_id = $2 for update`,
		clanID, gameID).Scan(&c.ID, &c.Name, &c.Description, &c.Badge, &c.Tag, &c.MemberCount)
	if err != nil {
		return RenameResult{}, leaveClanError(err)
	}
	res.PreviousName = c.Name

	// 2. The caller must be this clan's owner.
	if _, err := clanOwner(ctx, tx, gameID, clanID, ownerExternalID); err != nil {
		return RenameResult{}, err
	}
	if name == c.Name {
		return res, nil // nothing to do; the rollback only ends the transaction
	}

	// 3. Rename. The unique index on (game_id, lower(name)) refuses a name another clan has.
	if _, err := tx.Exec(ctx, `update clans set name = $2 where id = $1`, clanID, name); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == uniqueClanName {
			return RenameResult{}, ErrClanNameTaken
		}
		return RenameResult{}, fmt.Errorf("rename clan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == uniqueClanName {
			return RenameResult{}, ErrClanNameTaken
		}
		return RenameResult{}, fmt.Errorf("rename clan: %w", err)
	}
	c.Name = name
	res.Changed = true
	return res, nil
}

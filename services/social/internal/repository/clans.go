package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Clan is a row of the clans table, with the fields shown in clan lists.
type Clan struct {
	ID          string
	Name        string
	MemberCount int
	MaxMembers  int
}

// ListClans returns up to limit clans of gameID, sorted by name.
// When query is not empty, only clans whose name contains it are returned, ignoring
// upper/lower case. strpos looks for the exact text, so characters like % and _ in the
// search are not wildcards (as they would be with LIKE).
func (r *Repository) ListClans(ctx context.Context, gameID, query string, limit int) ([]Clan, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.pool.Query(ctx,
		`select id::text, name, member_count, max_members
		 from clans
		 where game_id = $1
		   and ($2 = '' or strpos(lower(name), lower($2)) > 0)
		 order by lower(name), id
		 limit $3`,
		gameID, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list clans: %w", err)
	}
	defer rows.Close()

	clans := make([]Clan, 0)
	for rows.Next() {
		var c Clan
		if err := rows.Scan(&c.ID, &c.Name, &c.MemberCount, &c.MaxMembers); err != nil {
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

// CreateClan creates a clan in gameID owned by ownerID (social's player ID) and adds the
// owner as its first member, in one transaction: either both rows are saved or neither is.
//
// It returns ErrAlreadyInClan if the owner is already in a clan of this game, and
// ErrClanNameTaken if the game already has a clan with this name.
func (r *Repository) CreateClan(ctx context.Context, gameID, ownerID, name string) (Clan, error) {
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

	c := Clan{Name: name}
	err = tx.QueryRow(ctx,
		`insert into clans (game_id, name, owner_id)
		 values ($1, $2, $3)
		 returning id::text, member_count, max_members`,
		gameID, name, ownerID).Scan(&c.ID, &c.MemberCount, &c.MaxMembers)
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
	// ErrClanFull means the clan already has max_members members.
	ErrClanFull = errors.New("clan is full")
)

// JoinClan adds playerID to clan clanID of gameID with role "member" and returns the
// clan with its new member count. Everything happens in one transaction.
//
// The clan row is locked first (SELECT ... FOR UPDATE). Other joins of the same clan
// wait at that line until this transaction ends, so two players can never both take
// the last free place.
//
// Errors: ErrClanNotFound, ErrAlreadyInClan, ErrClanFull.
func (r *Repository) JoinClan(ctx context.Context, gameID, clanID, playerID string) (Clan, error) {
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
		`select name, member_count, max_members
		 from clans
		 where id = $1 and game_id = $2
		 for update`,
		clanID, gameID).Scan(&c.Name, &c.MemberCount, &c.MaxMembers)
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
	if c.MemberCount >= c.MaxMembers {
		return Clan{}, ErrClanFull
	}

	// 4. Add the member and count them.
	_, err = tx.Exec(ctx,
		`insert into clan_members (clan_id, player_id, game_id, role) values ($1, $2, $3, 'member')`,
		clanID, playerID, gameID)
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

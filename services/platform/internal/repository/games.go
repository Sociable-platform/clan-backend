package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// foreignKeyViolation is the Postgres error code for a reference to a row that does not exist.
const foreignKeyViolation = "23503"

// invalidTextRepresentation is the Postgres error code for text that is not a valid value, here an ID
// that is not a UUID.
const invalidTextRepresentation = "22P02"

// isInvalidText reports whether err is Postgres refusing text that is not a valid value, here an ID that is
// not a UUID. Such an ID matches nothing, so callers answer it like a missing row.
func isInvalidText(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == invalidTextRepresentation
}

// The two roles a developer has on a game (game_members.role). The owner may do everything; a member
// works on the game but does not manage it (TEAM-306).
const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

// Game is a row of the games table.
type Game struct {
	ID string
	// DeveloperID is the developer who registered the game, kept while games.developer_id exists. Who may
	// use the game is decided by game_members (Role), not by this.
	DeveloperID string
	// Role is the asking developer's role on the game (RoleOwner or RoleMember); empty when the game was
	// read without a developer (GameByID).
	Role        string
	Name        string
	Description string // empty when the database value is null
	// AllowedOrigins are the websites that may open a WebSocket for this game. Never nil.
	AllowedOrigins []string
	CreatedAt      time.Time
}

// CreateGame inserts a game registered by developerID and returns it with its generated ID. The developer
// becomes its owner: the game and the membership are one statement, so there is never a game nobody
// belongs to. It returns ErrNotFound when the developer does not exist.
func (r *Repository) CreateGame(ctx context.Context, developerID, name, description string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	g := Game{DeveloperID: developerID, Role: RoleOwner, Name: name, Description: description}
	// The second insert is not read by the final select, but a data-modifying WITH always runs to the end.
	err := r.db(ctx).QueryRow(ctx,
		`with g as (
		     insert into games (id, developer_id, name, description)
		     values (gen_random_uuid(), $1, $2, nullif($3, ''))
		     returning id, allowed_origins, created_at),
		 m as (
		     insert into game_members (game_id, developer_id, role)
		     select id, $1, 'owner' from g)
		 select id::text, allowed_origins, created_at from g`,
		developerID, name, description,
	).Scan(&g.ID, &g.AllowedOrigins, &g.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return Game{}, ErrNotFound
		}
		return Game{}, fmt.Errorf("insert game: %w", err)
	}
	return g, nil
}

// GamesOfMember returns the games developerID belongs to, with their role on each, newest first.
// A developer without games gets an empty, non-nil slice.
func (r *Repository) GamesOfMember(ctx context.Context, developerID string) ([]Game, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select g.id::text, g.developer_id::text, m.role, g.name, coalesce(g.description, ''), g.allowed_origins, g.created_at
		 from games g join game_members m on m.game_id = g.id
		 where m.developer_id = $1
		 order by g.created_at desc, g.id`, developerID)
	if err != nil {
		return nil, fmt.Errorf("select games: %w", err)
	}
	defer rows.Close()

	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.DeveloperID, &g.Role, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		games = append(games, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read games: %w", err)
	}
	return games, nil
}

// GameOfMember returns the game with that ID, with developerID's role on it, only if developerID belongs
// to it. A game that does not exist and a game the developer is not in both give ErrNotFound, so the
// answer never tells whether somebody else's game exists. An ID that is not a UUID is ErrNotFound too.
func (r *Repository) GameOfMember(ctx context.Context, gameID, developerID string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`select g.id::text, g.developer_id::text, m.role, g.name, coalesce(g.description, ''), g.allowed_origins, g.created_at
		 from games g join game_members m on m.game_id = g.id
		 where g.id = $1 and m.developer_id = $2`, gameID, developerID,
	).Scan(&g.ID, &g.DeveloperID, &g.Role, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if isInvalidText(err) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("select game: %w", err)
	}
	return g, nil
}

// GameByID returns the game with that ID whoever belongs to it, or ErrNotFound. Only for callers that
// are not a developer (the internal endpoint); every developer-facing lookup uses GameOfMember.
func (r *Repository) GameByID(ctx context.Context, gameID string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, developer_id::text, name, coalesce(description, ''), allowed_origins, created_at
		 from games where id = $1`, gameID,
	).Scan(&g.ID, &g.DeveloperID, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("select game by id: %w", err)
	}
	return g, nil
}

// SetAllowedOrigins replaces the allowed origins of a game that developerID owns and returns the game. Only
// an owner may: the service turns a member away with ErrOwnerRequired before it gets here, and the role is
// checked again in this statement, so a role that changes at the same moment cannot let a member through (that
// race answers ErrNotFound). A game that does not exist and a game the developer is not in give ErrNotFound too.
func (r *Repository) SetAllowedOrigins(ctx context.Context, gameID, developerID string, origins []string) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	if origins == nil {
		origins = []string{} // a null would break the not null column
	}
	var g Game
	err := r.db(ctx).QueryRow(ctx,
		`update games g set allowed_origins = $3
		 from game_members m
		 where g.id = $1 and m.game_id = g.id and m.developer_id = $2 and m.role = 'owner'
		 returning g.id::text, g.developer_id::text, m.role, g.name, coalesce(g.description, ''), g.allowed_origins, g.created_at`,
		gameID, developerID, origins,
	).Scan(&g.ID, &g.DeveloperID, &g.Role, &g.Name, &g.Description, &g.AllowedOrigins, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("update allowed origins: %w", err)
	}
	return g, nil
}

// GameMember is a developer on a game, as the team list shows them.
type GameMember struct {
	DeveloperID string
	Name        string
	Email       string
	Role        string // RoleOwner or RoleMember
	JoinedAt    time.Time
}

// GameMembers returns everybody who belongs to the game: the owners first, then the members, each group by when
// they joined (ties by ID, so the order is always the same). The caller has already checked that the developer
// asking belongs to the game; a game that does not exist gives an empty list.
func (r *Repository) GameMembers(ctx context.Context, gameID string) ([]GameMember, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select d.id::text, d.name, d.email, m.role, m.created_at
		 from game_members m join developers d on d.id = m.developer_id
		 where m.game_id = $1
		 order by (m.role = 'owner') desc, m.created_at, d.id`, gameID)
	if err != nil {
		return nil, fmt.Errorf("select game members: %w", err)
	}
	defer rows.Close()

	members := []GameMember{}
	for rows.Next() {
		var m GameMember
		if err := rows.Scan(&m.DeveloperID, &m.Name, &m.Email, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan game member: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read game members: %w", err)
	}
	return members, nil
}

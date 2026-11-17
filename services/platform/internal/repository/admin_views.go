package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The admin panel reads across every developer, which no other query in this package does.
// Callers must have checked an admin token first (the service does, through requireAdmin).

// DeveloperSummary is a developer as the admin list shows it.
type DeveloperSummary struct {
	ID        string
	Email     string
	Name      string
	Status    string // active or suspended
	GameCount int
	CreatedAt time.Time
}

// AdminDeveloper is one developer as the admin detail shows it.
type AdminDeveloper struct {
	ID              string
	Email           string
	Name            string
	Status          string
	StatusReason    string     // empty when never suspended
	StatusChangedAt *time.Time // nil when the status never changed
	CreatedAt       time.Time
}

// AdminGame is a game as the admin detail shows it.
type AdminGame struct {
	ID              string
	Name            string
	Description     string
	Status          string
	StatusReason    string
	StatusChangedAt *time.Time
	CreatedAt       time.Time
	// Role is the role of the developer the list is for (AdminGamesByDeveloper); empty elsewhere.
	Role string
	// OtherOwnerActive says that another owner of the game has an active account, so suspending or resuming the
	// developer the list is for does not change whether the game is served (AdminGamesByDeveloper only).
	OtherOwnerActive bool
}

// AuditEntry is a row of admin_audit_log.
type AuditEntry struct {
	ID         string
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Reason     string
	Note       string // empty when there is none
	CreatedAt  time.Time
}

// Cursor is the position after which the next page starts: the created time and ID of the last
// row of the previous page. Both lists are ordered newest first, ties broken by ID.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// DeveloperFilter selects a page of developers. Zero values mean "no filter".
type DeveloperFilter struct {
	Query  string  // part of the email or name, matched without regard to case
	Status string  // active or suspended
	After  *Cursor // nil for the first page
	Limit  int
}

// AuditFilter selects a page of audit log entries.
type AuditFilter struct {
	TargetID string  // only entries about this developer or game; empty for all
	After    *Cursor // nil for the first page
	Limit    int
}

// ListDevelopers returns developers newest first, at most f.Limit of them. The caller asks for
// one more than it shows to learn whether another page exists.
func (r *Repository) ListDevelopers(ctx context.Context, f DeveloperFilter) ([]DeveloperSummary, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	pattern := ""
	if f.Query != "" {
		pattern = "%" + escapeLike(f.Query) + "%"
	}
	var afterTime *time.Time
	var afterID *string
	if f.After != nil {
		afterTime, afterID = &f.After.CreatedAt, &f.After.ID
	}
	rows, err := r.db(ctx).Query(ctx,
		`select d.id::text, d.email, d.name, d.status, d.created_at,
		        (select count(*) from game_members m where m.developer_id = d.id)
		 from developers d
		 where ($1 = '' or d.status = $1)
		   and ($2 = '' or d.email ilike $2 or d.name ilike $2)
		   and ($3::timestamptz is null or (d.created_at, d.id) < ($3::timestamptz, $4::uuid))
		 order by d.created_at desc, d.id desc
		 limit $5`,
		f.Status, pattern, afterTime, afterID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("select developers: %w", err)
	}
	defer rows.Close()

	out := []DeveloperSummary{}
	for rows.Next() {
		var d DeveloperSummary
		if err := rows.Scan(&d.ID, &d.Email, &d.Name, &d.Status, &d.CreatedAt, &d.GameCount); err != nil {
			return nil, fmt.Errorf("scan developer: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read developers: %w", err)
	}
	return out, nil
}

// AdminDeveloperByID returns the developer with that ID, or ErrNotFound.
func (r *Repository) AdminDeveloperByID(ctx context.Context, id string) (AdminDeveloper, error) {
	if r.pool == nil {
		return AdminDeveloper{}, ErrNoDatabase
	}
	var d AdminDeveloper
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, email, name, status, coalesce(status_reason, ''), status_changed_at, created_at
		 from developers where id = $1`, id,
	).Scan(&d.ID, &d.Email, &d.Name, &d.Status, &d.StatusReason, &d.StatusChangedAt, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminDeveloper{}, ErrNotFound
	}
	if err != nil {
		return AdminDeveloper{}, fmt.Errorf("select developer for admin: %w", err)
	}
	return d, nil
}

// AdminGamesByDeveloper returns every game the developer is on, as owner or member, with their role and its status,
// newest first. A developer without games gets an empty, non-nil slice.
func (r *Repository) AdminGamesByDeveloper(ctx context.Context, developerID string) ([]AdminGame, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select g.id::text, g.name, coalesce(g.description, ''), g.status, coalesce(g.status_reason, ''), g.status_changed_at,
		        g.created_at, m.role,
		        exists (select 1 from game_members o join developers od on od.id = o.developer_id
		                where o.game_id = g.id and o.role = 'owner' and o.developer_id <> m.developer_id and od.status = 'active')
		 from game_members m join games g on g.id = m.game_id
		 where m.developer_id = $1
		 order by g.created_at desc, g.id desc`, developerID)
	if err != nil {
		return nil, fmt.Errorf("select games for admin: %w", err)
	}
	defer rows.Close()

	out := []AdminGame{}
	for rows.Next() {
		var g AdminGame
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Status, &g.StatusReason, &g.StatusChangedAt, &g.CreatedAt, &g.Role, &g.OtherOwnerActive); err != nil {
			return nil, fmt.Errorf("scan game for admin: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read games for admin: %w", err)
	}
	return out, nil
}

// AuditEntries returns audit log entries newest first, at most f.Limit of them.
func (r *Repository) AuditEntries(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	var targetID *string
	if f.TargetID != "" {
		targetID = &f.TargetID
	}
	var afterTime *time.Time
	var afterID *string
	if f.After != nil {
		afterTime, afterID = &f.After.CreatedAt, &f.After.ID
	}
	rows, err := r.db(ctx).Query(ctx,
		`select id::text, actor, action, target_type, target_id::text, reason, coalesce(note, ''), created_at
		 from admin_audit_log
		 where ($1::uuid is null or target_id = $1::uuid)
		   and ($2::timestamptz is null or (created_at, id) < ($2::timestamptz, $3::uuid))
		 order by created_at desc, id desc
		 limit $4`,
		targetID, afterTime, afterID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("select audit log: %w", err)
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Reason, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return out, nil
}

// escapeLike makes s safe to put inside an ilike pattern: %, _ and the escape character \ lose
// their special meaning, so a search for "50%" finds "50%" and not everything.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// AdminGameByID returns one game with its status, or ErrNotFound (also for an ID that is not a UUID).
func (r *Repository) AdminGameByID(ctx context.Context, gameID string) (AdminGame, error) {
	if r.pool == nil {
		return AdminGame{}, ErrNoDatabase
	}
	var g AdminGame
	err := r.db(ctx).QueryRow(ctx,
		`select id::text, name, coalesce(description, ''), status, coalesce(status_reason, ''), status_changed_at, created_at
		 from games where id = $1`, gameID,
	).Scan(&g.ID, &g.Name, &g.Description, &g.Status, &g.StatusReason, &g.StatusChangedAt, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidText(err) {
		return AdminGame{}, ErrNotFound
	}
	if err != nil {
		return AdminGame{}, fmt.Errorf("select game for admin: %w", err)
	}
	return g, nil
}

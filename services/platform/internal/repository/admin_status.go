package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Account statuses stored in developers.status and games.status.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// StatusChange describes one change of a developer's or game's status and the audit row that goes
// with it. The service has validated every field.
type StatusChange struct {
	Actor  string // admin ID, or "system:billing"
	Action string // developer.suspended, developer.resumed, game.suspended or game.resumed
	Status string // the wanted status: StatusActive or StatusSuspended
	// Reason is why access is switched off (non_payment, abuse, other). When resuming it may be
	// empty: the audit row then records the reason of the suspension being lifted.
	Reason string
	Note   string // internal text for the audit log only; may be empty
}

// SetDeveloperStatus moves a developer to c.Status and writes the audit row in one transaction:
// both happen or neither does. The developer row is locked meanwhile, so two admins pressing the
// same button at once produce one change and one audit row. When the developer already has that
// status nothing is written and changed is false. It returns the developer as it is afterwards,
// or ErrNotFound.
func (r *Repository) SetDeveloperStatus(ctx context.Context, developerID string, c StatusChange) (d AdminDeveloper, changed bool, err error) {
	if r.pool == nil {
		return AdminDeveloper{}, false, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return AdminDeveloper{}, false, fmt.Errorf("begin developer status change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no effect once committed

	// "no key update" makes concurrent changes wait for each other without blocking inserts of
	// games, which only reference the developer.
	err = tx.QueryRow(ctx,
		`select id::text, email, name, status, coalesce(status_reason, ''), status_changed_at, created_at
		 from developers where id = $1 for no key update`, developerID,
	).Scan(&d.ID, &d.Email, &d.Name, &d.Status, &d.StatusReason, &d.StatusChangedAt, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminDeveloper{}, false, ErrNotFound
	}
	if err != nil {
		return AdminDeveloper{}, false, fmt.Errorf("lock developer: %w", err)
	}
	if d.Status == c.Status {
		return d, false, nil
	}

	reason, rowReason := auditReason(c, d.StatusReason)
	err = tx.QueryRow(ctx,
		`update developers set status = $2, status_reason = $3, status_changed_at = now()
		 where id = $1
		 returning status, coalesce(status_reason, ''), status_changed_at`,
		developerID, c.Status, rowReason,
	).Scan(&d.Status, &d.StatusReason, &d.StatusChangedAt)
	if err != nil {
		return AdminDeveloper{}, false, fmt.Errorf("update developer status: %w", err)
	}
	if err := insertAudit(ctx, tx, "developer", developerID, c, reason); err != nil {
		return AdminDeveloper{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminDeveloper{}, false, fmt.Errorf("commit developer status change: %w", err)
	}
	return d, true, nil
}

// SetGameStatus does for one game what SetDeveloperStatus does for a developer. Suspending a
// developer does not touch their games' own status: whether a game is served depends on both.
func (r *Repository) SetGameStatus(ctx context.Context, gameID string, c StatusChange) (g AdminGame, changed bool, err error) {
	if r.pool == nil {
		return AdminGame{}, false, ErrNoDatabase
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return AdminGame{}, false, fmt.Errorf("begin game status change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no effect once committed

	// "no key update" like in AddWidgetVersion: api_keys and widget rows reference the game and
	// must stay writable.
	err = tx.QueryRow(ctx,
		`select id::text, name, coalesce(description, ''), status, coalesce(status_reason, ''), status_changed_at, created_at
		 from games where id = $1 for no key update`, gameID,
	).Scan(&g.ID, &g.Name, &g.Description, &g.Status, &g.StatusReason, &g.StatusChangedAt, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminGame{}, false, ErrNotFound
	}
	if err != nil {
		return AdminGame{}, false, fmt.Errorf("lock game: %w", err)
	}
	if g.Status == c.Status {
		return g, false, nil
	}

	reason, rowReason := auditReason(c, g.StatusReason)
	err = tx.QueryRow(ctx,
		`update games set status = $2, status_reason = $3, status_changed_at = now()
		 where id = $1
		 returning status, coalesce(status_reason, ''), status_changed_at`,
		gameID, c.Status, rowReason,
	).Scan(&g.Status, &g.StatusReason, &g.StatusChangedAt)
	if err != nil {
		return AdminGame{}, false, fmt.Errorf("update game status: %w", err)
	}
	if err := insertAudit(ctx, tx, "game", gameID, c, reason); err != nil {
		return AdminGame{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminGame{}, false, fmt.Errorf("commit game status change: %w", err)
	}
	return g, true, nil
}

// auditReason returns the reason for the audit row, and the value for the row's status_reason
// column: the reason while suspended, null (nil) once active again. A resume without a reason
// records the reason of the suspension it lifts.
func auditReason(c StatusChange, current string) (audit string, column *string) {
	audit = c.Reason
	if audit == "" {
		audit = current
	}
	if audit == "" {
		audit = "other" // an active target is never resumed, so this only guards the not null column
	}
	if c.Status == StatusSuspended {
		return audit, &audit
	}
	return audit, nil
}

// insertAudit appends the audit row of a status change inside the caller's transaction. now() is
// the start of that transaction, the same instant as the row's status_changed_at.
func insertAudit(ctx context.Context, tx pgx.Tx, targetType, targetID string, c StatusChange, reason string) error {
	_, err := tx.Exec(ctx,
		`insert into admin_audit_log (id, actor, action, target_type, target_id, reason, note, created_at)
		 values (gen_random_uuid(), $1, $2, $3, $4, $5, nullif($6, ''), now())`,
		c.Actor, c.Action, targetType, targetID, reason, c.Note)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// GameAccess is the status of a game and of the developer who owns it. A game is served only when
// both are StatusActive.
type GameAccess struct {
	GameStatus      string
	DeveloperStatus string
}

// GameAccessOf returns the statuses that decide whether a game is served, or ErrNotFound for a
// game that does not exist. It reads the database on every call, so a suspension or a resume
// takes effect at once.
func (r *Repository) GameAccessOf(ctx context.Context, gameID string) (GameAccess, error) {
	if r.pool == nil {
		return GameAccess{}, ErrNoDatabase
	}
	var a GameAccess
	err := r.pool.QueryRow(ctx,
		`select g.status, d.status
		 from games g join developers d on d.id = g.developer_id
		 where g.id = $1`, gameID,
	).Scan(&a.GameStatus, &a.DeveloperStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return GameAccess{}, ErrNotFound
	}
	if err != nil {
		return GameAccess{}, fmt.Errorf("select game access: %w", err)
	}
	return a, nil
}

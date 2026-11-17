package repository

import (
	"context"
	"fmt"
	"time"
)

// What happened to a game's team, as game_team_log.action says it.
const (
	TeamInviteSent           = "invite_sent"
	TeamInviteAccepted       = "invite_accepted"
	TeamInviteDeclined       = "invite_declined"
	TeamInviteCancelled      = "invite_cancelled"
	TeamMemberRemoved        = "member_removed"
	TeamMemberLeft           = "member_left"
	TeamRoleChanged          = "role_changed"
	TeamOwnershipTransferred = "ownership_transferred"
)

// TeamLogInput is one change to record. ActorID is who did it. SubjectID is the developer it was done to, when
// there is one; SubjectEmail is the address of an invitation. Role is the new role of a role change.
type TeamLogInput struct {
	Action       string
	ActorID      string
	SubjectID    string
	SubjectEmail string
	Role         string
}

// TeamLogEntry is a row of a game's history with the names the admin panel shows.
type TeamLogEntry struct {
	ID           string
	Action       string
	ActorID      string
	ActorName    string
	ActorEmail   string
	SubjectID    string // empty when the row is about an address only
	SubjectName  string
	SubjectEmail string // the account's email when there is one, else the address of the invitation
	Role         string // empty unless the action is a role change
	CreatedAt    time.Time
}

// AddTeamLog records a change to a game's team. Call it in the transaction of the change: it joins it, so the change
// and its row are both kept or both lost.
func (r *Repository) AddTeamLog(ctx context.Context, gameID string, in TeamLogInput) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	var subjectID, subjectEmail, role *string
	if in.SubjectID != "" {
		subjectID = &in.SubjectID
	}
	if in.SubjectEmail != "" {
		subjectEmail = &in.SubjectEmail
	}
	if in.Role != "" {
		role = &in.Role
	}
	if _, err := r.db(ctx).Exec(ctx,
		`insert into game_team_log (game_id, action, actor_id, subject_id, subject_email, role)
		 values ($1, $2, $3, $4, $5, $6)`,
		gameID, in.Action, in.ActorID, subjectID, subjectEmail, role); err != nil {
		return fmt.Errorf("insert team log: %w", err)
	}
	return nil
}

// TeamLogOfGame returns a page of a game's history, newest first: at most limit rows after the cursor (nil for the
// first page). A game with no history, and a game that does not exist, give an empty, non-nil slice.
func (r *Repository) TeamLogOfGame(ctx context.Context, gameID string, after *Cursor, limit int) ([]TeamLogEntry, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	var afterTime *time.Time
	var afterID *string
	if after != nil {
		afterTime, afterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.db(ctx).Query(ctx,
		`select l.id::text, l.action, a.id::text, a.name, a.email,
		        coalesce(s.id::text, ''), coalesce(s.name, ''), coalesce(s.email, l.subject_email, ''),
		        coalesce(l.role, ''), l.created_at
		 from game_team_log l
		 join developers a on a.id = l.actor_id
		 left join developers s on s.id = l.subject_id
		 where l.game_id = $1
		   and ($2::timestamptz is null or (l.created_at, l.id) < ($2::timestamptz, $3::uuid))
		 order by l.created_at desc, l.id desc
		 limit $4`, gameID, afterTime, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("select team log: %w", err)
	}
	defer rows.Close()

	out := []TeamLogEntry{}
	for rows.Next() {
		var e TeamLogEntry
		if err := rows.Scan(&e.ID, &e.Action, &e.ActorID, &e.ActorName, &e.ActorEmail,
			&e.SubjectID, &e.SubjectName, &e.SubjectEmail, &e.Role, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan team log: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read team log: %w", err)
	}
	return out, nil
}

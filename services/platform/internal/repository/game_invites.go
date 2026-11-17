package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sentinel errors of the invitations.
var (
	// ErrInviteExpired means the invite exists and is for this person, but its time has passed.
	ErrInviteExpired = errors.New("invite expired")
)

// GameInvite is a pending invitation, with the names the lists show.
type GameInvite struct {
	ID            string
	GameID        string
	GameName      string
	Email         string // lower case
	InvitedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// InviteCounts is how many invites were sent lately, for the rate limits.
type InviteCounts struct {
	GameLastHour   int // by this game, to anybody, in the last hour
	PairLastDay    int // by this game to this address, in the last day
	AddressLastDay int // to this address, by any game, in the last day
}

// PurgeGameInvites deletes the invites that expired before now and the sent-invite log older than a day.
func (r *Repository) PurgeGameInvites(ctx context.Context, now time.Time) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	if _, err := r.db(ctx).Exec(ctx, `delete from game_invites where expires_at <= $1`, now); err != nil {
		return fmt.Errorf("delete expired game invites: %w", err)
	}
	if _, err := r.db(ctx).Exec(ctx, `delete from game_invite_log where created_at <= $1`, now.Add(-24*time.Hour)); err != nil {
		return fmt.Errorf("delete old game invite log: %w", err)
	}
	return nil
}

// EmailIsMember reports whether a developer with this email already belongs to the game, in any role.
func (r *Repository) EmailIsMember(ctx context.Context, gameID, email string) (bool, error) {
	if r.pool == nil {
		return false, ErrNoDatabase
	}
	var is bool
	err := r.db(ctx).QueryRow(ctx,
		`select exists (select 1 from game_members m join developers d on d.id = m.developer_id
		                where m.game_id = $1 and d.email = $2)`, gameID, email).Scan(&is)
	if err != nil {
		return false, fmt.Errorf("check game member by email: %w", err)
	}
	return is, nil
}

// InviteCountsSince counts the invites sent lately. It locks the game's row until the transaction ends,
// so two requests inviting for one game cannot both pass the same limit: call it in the transaction that
// then saves the invite.
func (r *Repository) InviteCountsSince(ctx context.Context, gameID, email string, now time.Time) (InviteCounts, error) {
	if r.pool == nil {
		return InviteCounts{}, ErrNoDatabase
	}
	if _, err := r.db(ctx).Exec(ctx, `select 1 from games where id = $1 for update`, gameID); err != nil {
		return InviteCounts{}, fmt.Errorf("lock game: %w", err)
	}
	var c InviteCounts
	err := r.db(ctx).QueryRow(ctx,
		`select count(*) filter (where game_id = $1 and created_at > $3),
		        count(*) filter (where game_id = $1 and email = $2 and created_at > $4),
		        count(*) filter (where email = $2 and created_at > $4)
		 from game_invite_log where created_at > $4`,
		gameID, email, now.Add(-time.Hour), now.Add(-24*time.Hour)).Scan(&c.GameLastHour, &c.PairLastDay, &c.AddressLastDay)
	if err != nil {
		return InviteCounts{}, fmt.Errorf("count game invites: %w", err)
	}
	return c, nil
}

// SaveGameInvite creates the invite for the email, or renews the one that is pending (a new expiry, the
// new inviter), and records it in the sent-invite log. It returns the invite with its names.
func (r *Repository) SaveGameInvite(ctx context.Context, gameID, email, invitedBy string, now, expiresAt time.Time) (GameInvite, error) {
	if r.pool == nil {
		return GameInvite{}, ErrNoDatabase
	}
	var inv GameInvite
	err := r.db(ctx).QueryRow(ctx,
		`with i as (
		     insert into game_invites (game_id, email, invited_by, created_at, expires_at)
		     values ($1, $2, $3, $4, $5)
		     on conflict (game_id, email) do update
		         set invited_by = excluded.invited_by, created_at = excluded.created_at, expires_at = excluded.expires_at
		     returning id, game_id, email, invited_by, created_at, expires_at),
		 l as (insert into game_invite_log (game_id, email, created_at) values ($1, $2, $4))
		 select i.id::text, i.game_id::text, g.name, i.email, d.name, i.created_at, i.expires_at
		 from i join games g on g.id = i.game_id join developers d on d.id = i.invited_by`,
		gameID, email, invitedBy, now, expiresAt).Scan(
		&inv.ID, &inv.GameID, &inv.GameName, &inv.Email, &inv.InvitedByName, &inv.CreatedAt, &inv.ExpiresAt)
	if err != nil {
		return GameInvite{}, fmt.Errorf("save game invite: %w", err)
	}
	return inv, nil
}

const gameInviteColumns = `i.id::text, i.game_id::text, g.name, i.email, d.name, i.created_at, i.expires_at`

func scanGameInvites(rows pgx.Rows) ([]GameInvite, error) {
	defer rows.Close()
	out := []GameInvite{}
	for rows.Next() {
		var inv GameInvite
		if err := rows.Scan(&inv.ID, &inv.GameID, &inv.GameName, &inv.Email, &inv.InvitedByName, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan game invite: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read game invites: %w", err)
	}
	return out, nil
}

// GameInvitesOfGame returns the pending invites of a game that have not expired, newest first.
func (r *Repository) GameInvitesOfGame(ctx context.Context, gameID string, now time.Time) ([]GameInvite, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select `+gameInviteColumns+`
		 from game_invites i join games g on g.id = i.game_id join developers d on d.id = i.invited_by
		 where i.game_id = $1 and i.expires_at > $2 order by i.created_at desc, i.id`, gameID, now)
	if err != nil {
		return nil, fmt.Errorf("select game invites: %w", err)
	}
	return scanGameInvites(rows)
}

// GameInvitesForEmail returns the invites sent to the address that have not expired, newest first.
func (r *Repository) GameInvitesForEmail(ctx context.Context, email string, now time.Time) ([]GameInvite, error) {
	if r.pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := r.db(ctx).Query(ctx,
		`select `+gameInviteColumns+`
		 from game_invites i join games g on g.id = i.game_id join developers d on d.id = i.invited_by
		 where i.email = $1 and i.expires_at > $2 order by i.created_at desc, i.id`, email, now)
	if err != nil {
		return nil, fmt.Errorf("select invites of email: %w", err)
	}
	return scanGameInvites(rows)
}

// DeleteGameInvite removes a pending invite of the game (an owner cancelling it). An invite that does not
// exist, one of another game and an ID that is not a UUID all give ErrNotFound.
func (r *Repository) DeleteGameInvite(ctx context.Context, gameID, inviteID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	tag, err := r.db(ctx).Exec(ctx, `delete from game_invites where id = $1 and game_id = $2`, inviteID, gameID)
	if isInvalidText(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete game invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeclineGameInvite removes an invite sent to email (the invited person saying no). Somebody else's invite,
// one that does not exist and an ID that is not a UUID all give ErrNotFound; an expired one is removed too.
func (r *Repository) DeclineGameInvite(ctx context.Context, inviteID, email string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	tag, err := r.db(ctx).Exec(ctx, `delete from game_invites where id = $1 and email = $2`, inviteID, email)
	if isInvalidText(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("decline game invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AcceptGameInvite turns an invite sent to email into a membership of developerID, with role member, and
// deletes the invite, in one statement: both happen or neither. Being a member already is fine (the invite
// is used up and the existing role stays). It returns the game as the developer sees it.
//
// An invite that does not exist, one sent to another address and an ID that is not a UUID give ErrNotFound; one
// that is for this address but has expired gives ErrInviteExpired (and is deleted, no member is made).
func (r *Repository) AcceptGameInvite(ctx context.Context, inviteID, developerID, email string, now time.Time) (Game, error) {
	if r.pool == nil {
		return Game{}, ErrNoDatabase
	}
	var (
		gameID  *string
		expires *time.Time
	)
	err := r.db(ctx).QueryRow(ctx,
		`with inv as (
		     delete from game_invites where id = $1 and email = $2 returning game_id, expires_at),
		 m as (
		     insert into game_members (game_id, developer_id, role)
		     select game_id, $3, 'member' from inv where expires_at > $4
		     on conflict do nothing)
		 select (select game_id::text from inv), (select expires_at from inv)`,
		inviteID, email, developerID, now).Scan(&gameID, &expires)
	if isInvalidText(err) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, fmt.Errorf("accept game invite: %w", err)
	}
	switch {
	case gameID == nil:
		return Game{}, ErrNotFound
	case !expires.After(now):
		return Game{}, ErrInviteExpired
	}
	return r.GameOfMember(ctx, *gameID, developerID)
}

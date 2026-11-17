package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// lockGameTeam locks the game's row until the transaction ends, so that changes to the team of one game run
// one after the other. Every change that can take an owner away takes this lock first and then counts the
// owners, which is what makes "a game keeps at least one owner" hold when two requests race: the second one
// waits, then counts again and sees what the first one did. The lock is "no key update", like the status changes
// of the admin panel: api_keys and widget rows that reference the game stay writable. A game that does not exist
// gives ErrNotFound.
func (r *Repository) lockGameTeam(ctx context.Context, gameID string) error {
	var id string
	err := r.db(ctx).QueryRow(ctx, `select id::text from games where id = $1 for no key update`, gameID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidText(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock game team: %w", err)
	}
	return nil
}

// roleOf returns the role developerID has on the game, or ErrNotFound when they are not on it.
func (r *Repository) roleOf(ctx context.Context, gameID, developerID string) (string, error) {
	var role string
	err := r.db(ctx).QueryRow(ctx,
		`select role from game_members where game_id = $1 and developer_id = $2`, gameID, developerID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidText(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select member role: %w", err)
	}
	return role, nil
}

// ownerCount is how many owners the game has now; call it after lockGameTeam.
func (r *Repository) ownerCount(ctx context.Context, gameID string) (int, error) {
	var n int
	if err := r.db(ctx).QueryRow(ctx,
		`select count(*) from game_members where game_id = $1 and role = 'owner'`, gameID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count owners: %w", err)
	}
	return n, nil
}

// RemoveGameMember takes developerID off the game: the member loses access at once, because every request
// looks the membership up. Removing the last owner gives ErrLastOwner and changes nothing; a developer who is
// not on the game gives ErrNotFound. It is used for an owner removing somebody and for somebody leaving.
func (r *Repository) RemoveGameMember(ctx context.Context, gameID, developerID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.InTx(ctx, func(ctx context.Context) error {
		if err := r.lockGameTeam(ctx, gameID); err != nil {
			return err
		}
		role, err := r.roleOf(ctx, gameID, developerID)
		if err != nil {
			return err
		}
		if role == RoleOwner {
			n, err := r.ownerCount(ctx, gameID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastOwner
			}
		}
		if _, err := r.db(ctx).Exec(ctx,
			`delete from game_members where game_id = $1 and developer_id = $2`, gameID, developerID); err != nil {
			return fmt.Errorf("delete game member: %w", err)
		}
		return nil
	})
}

// SetGameMemberRole makes developerID an owner or a member of the game and returns their row. A role they have
// already is not a change. Demoting the last owner gives ErrLastOwner and changes nothing; a developer who is not
// on the game gives ErrNotFound.
func (r *Repository) SetGameMemberRole(ctx context.Context, gameID, developerID, role string) (GameMember, error) {
	if r.pool == nil {
		return GameMember{}, ErrNoDatabase
	}
	var out GameMember
	err := r.InTx(ctx, func(ctx context.Context) error {
		if err := r.lockGameTeam(ctx, gameID); err != nil {
			return err
		}
		current, err := r.roleOf(ctx, gameID, developerID)
		if err != nil {
			return err
		}
		if current == RoleOwner && role != RoleOwner {
			n, err := r.ownerCount(ctx, gameID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastOwner
			}
		}
		return r.db(ctx).QueryRow(ctx,
			`with m as (update game_members set role = $3 where game_id = $1 and developer_id = $2
			            returning developer_id, role, created_at)
			 select d.id::text, d.name, d.email, m.role, d.status, m.created_at
			 from m join developers d on d.id = m.developer_id`, gameID, developerID, role,
		).Scan(&out.DeveloperID, &out.Name, &out.Email, &out.Role, &out.Status, &out.JoinedAt)
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrLastOwner) {
			return GameMember{}, err
		}
		return GameMember{}, fmt.Errorf("set game member role: %w", err)
	}
	return out, nil
}

// TransferGameOwnership hands the game from one owner to another member in one transaction: toID becomes an owner and
// fromID a member, so the number of owners does not change. fromID has to be an owner (else ErrOwnerRequired, which can
// only happen when they were demoted or removed since the request was checked); toID has to be on the game (else
// ErrNotFound) and not an owner already (else ErrAlreadyOwner).
func (r *Repository) TransferGameOwnership(ctx context.Context, gameID, fromID, toID string) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.InTx(ctx, func(ctx context.Context) error {
		if err := r.lockGameTeam(ctx, gameID); err != nil {
			return err
		}
		fromRole, err := r.roleOf(ctx, gameID, fromID)
		if errors.Is(err, ErrNotFound) || (err == nil && fromRole != RoleOwner) {
			return ErrOwnerRequired
		}
		if err != nil {
			return err
		}
		toRole, err := r.roleOf(ctx, gameID, toID)
		if err != nil {
			return err
		}
		if toRole == RoleOwner {
			return ErrAlreadyOwner
		}
		if _, err := r.db(ctx).Exec(ctx,
			`update game_members set role = case developer_id when $3 then 'owner' else 'member' end
			 where game_id = $1 and developer_id in ($2, $3)`, gameID, fromID, toID); err != nil {
			return fmt.Errorf("swap game owners: %w", err)
		}
		return nil
	})
}

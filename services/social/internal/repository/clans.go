package repository

import (
	"context"
	"fmt"
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

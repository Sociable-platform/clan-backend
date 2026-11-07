package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
)

const (
	// maxClanSearchLen is the longest search text accepted by ListClans.
	maxClanSearchLen = 50
	// clanListLimit is the most clans one list returns (paging can come later).
	clanListLimit = 100
)

// Clan is a clan as shown in clan lists.
type Clan struct {
	ID          string
	Name        string
	MemberCount int
	MaxMembers  int
}

// ListClans returns the clans of gameID, sorted by name, at most clanListLimit.
// query (optional) keeps only clans whose name contains it, ignoring upper/lower case.
// gameID comes from the verified player token, so a player only ever sees their own game's clans.
func (s *Service) ListClans(ctx context.Context, gameID, query string) ([]Clan, error) {
	q := strings.TrimSpace(query)
	if utf8.RuneCountInString(q) > maxClanSearchLen {
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_query",
			fmt.Sprintf("Search text must be at most %d characters", maxClanSearchLen))
	}

	rows, err := s.store.ListClans(ctx, gameID, q, clanListLimit)
	if err != nil {
		return nil, fmt.Errorf("list clans: %w", err)
	}
	clans := make([]Clan, 0, len(rows))
	for _, c := range rows {
		clans = append(clans, Clan{ID: c.ID, Name: c.Name, MemberCount: c.MemberCount, MaxMembers: c.MaxMembers})
	}
	return clans, nil
}

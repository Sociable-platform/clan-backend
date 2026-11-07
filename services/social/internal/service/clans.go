package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

const (
	// maxClanSearchLen is the longest search text accepted by ListClans.
	maxClanSearchLen = 50
	// clanListLimit is the most clans one list returns (paging can come later).
	clanListLimit = 100
	// minClanNameLen and maxClanNameLen limit clan names, counted in characters (not bytes).
	minClanNameLen = 3
	maxClanNameLen = 20
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

// CreateClan creates a clan named name in gameID. The calling player (from the token)
// becomes its owner and first member. The player record is created first if this is
// the player's first request.
//
// Errors the client can act on:
//   - 400 invalid_clan_name: the name is not 3 to 20 characters, or has invisible characters
//   - 409 clan_name_taken: the game already has a clan with this name (ignoring upper/lower case)
//   - 409 already_in_clan: the player is already in a clan of this game
func (s *Service) CreateClan(ctx context.Context, gameID, externalID, displayName, name string) (Clan, error) {
	clean, err := cleanClanName(name)
	if err != nil {
		return Clan{}, err
	}

	owner, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}

	row, err := s.store.CreateClan(ctx, gameID, owner.ID, clean)
	switch {
	case errors.Is(err, repository.ErrClanNameTaken):
		return Clan{}, httpx.NewError(http.StatusConflict, "clan_name_taken", "A clan with this name already exists")
	case errors.Is(err, repository.ErrAlreadyInClan):
		return Clan{}, httpx.NewError(http.StatusConflict, "already_in_clan", "You are already in a clan; leave it first")
	case err != nil:
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}
	return Clan{ID: row.ID, Name: row.Name, MemberCount: row.MemberCount, MaxMembers: row.MaxMembers}, nil
}

// cleanClanName trims the name and turns any run of spaces inside it into one space,
// so "  Wolf   Pack " is saved as "Wolf Pack". It then checks the length (3 to 20
// characters) and refuses invisible characters such as tabs or control codes.
func cleanClanName(name string) (string, error) {
	clean := strings.Join(strings.Fields(name), " ")
	n := utf8.RuneCountInString(clean)
	if n < minClanNameLen || n > maxClanNameLen {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_name",
			fmt.Sprintf("Clan name must be %d to %d characters", minClanNameLen, maxClanNameLen))
	}
	for _, r := range clean {
		if !unicode.IsPrint(r) {
			return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_name",
				"Clan name can only have visible characters")
		}
	}
	return clean, nil
}

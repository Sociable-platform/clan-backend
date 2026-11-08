package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

const (
	// maxClanSearchLen is the longest search text accepted by ListClans.
	maxClanSearchLen = 50
	// clanListLimit is the most clans one list returns, and how many it returns when
	// the caller does not ask for fewer (paging can come later).
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

// limitText is a positive whole number written with plain digits.
var limitText = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)

// ListClansInput is what a caller can ask of the clan list. Everything is text, as it comes
// from the URL, and an empty value means "not given". ListClans checks it.
type ListClansInput struct {
	Query    string // name must contain it, ignoring upper/lower case; at most 50 characters
	Limit    string // 1 to 100; default 100
	Sort     string // "name" (default) or "members" (most members first)
	HasSpace string // "true": only clans with a free place; "false" or empty: all clans
}

// ListClans returns the clans of gameID, by default sorted by name, at most clanListLimit.
// A search (Query) looks through all clans, full ones too. To show "featured" clans a page
// asks for the biggest clans that still have room: Sort "members", HasSpace "true", a small Limit.
// gameID comes from the verified player token, so a player only ever sees their own game's clans.
//
// Wrong input is a 400: invalid_query, invalid_limit, invalid_sort or invalid_has_space.
func (s *Service) ListClans(ctx context.Context, gameID string, in ListClansInput) ([]Clan, error) {
	q := strings.TrimSpace(in.Query)
	if utf8.RuneCountInString(q) > maxClanSearchLen {
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_query",
			fmt.Sprintf("Search text must be at most %d characters", maxClanSearchLen))
	}

	opts := repository.ListClansOptions{Limit: clanListLimit}
	if in.Limit != "" {
		// plain digits only: no sign, no decimals, no leading zero
		n, err := strconv.Atoi(in.Limit)
		if !limitText.MatchString(in.Limit) || err != nil || n > clanListLimit {
			return nil, httpx.NewError(http.StatusBadRequest, "invalid_limit",
				fmt.Sprintf("limit must be a whole number from 1 to %d", clanListLimit))
		}
		opts.Limit = n
	}
	switch in.Sort {
	case "", "name":
	case "members":
		opts.ByMembers = true
	default:
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_sort", "sort must be name or members")
	}
	switch in.HasSpace {
	case "", "false":
	case "true":
		opts.OnlyWithSpace = true
	default:
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_has_space", "hasSpace must be true or false")
	}

	rows, err := s.store.ListClans(ctx, gameID, q, opts)
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

// uuidPattern matches a UUID such as 3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// JoinClan adds the calling player to clan clanID of their game with role "member" and
// returns the clan with its new member count. Every clan is open for now (no invite-only
// clans yet). The player record is created first if this is the player's first request.
//
// Errors the client can act on:
//   - 404 clan_not_found: no such clan in the player's game (a clan of another game counts
//     as not found, so players cannot learn about other games' clans)
//   - 409 already_in_clan: the player is already in a clan of this game
//   - 409 clan_full: the clan has no free place
func (s *Service) JoinClan(ctx context.Context, gameID, externalID, displayName, clanID string) (Clan, error) {
	if !uuidPattern.MatchString(clanID) {
		return Clan{}, errClanNotFound()
	}

	player, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return Clan{}, fmt.Errorf("join clan: %w", err)
	}

	row, err := s.store.JoinClan(ctx, gameID, strings.ToLower(clanID), player.ID)
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return Clan{}, errClanNotFound()
	case errors.Is(err, repository.ErrAlreadyInClan):
		return Clan{}, httpx.NewError(http.StatusConflict, "already_in_clan", "You are already in a clan; leave it first")
	case errors.Is(err, repository.ErrClanFull):
		return Clan{}, httpx.NewError(http.StatusConflict, "clan_full", "This clan has no free places")
	case err != nil:
		return Clan{}, fmt.Errorf("join clan: %w", err)
	}
	return Clan{ID: row.ID, Name: row.Name, MemberCount: row.MemberCount, MaxMembers: row.MaxMembers}, nil
}

// LeaveClan removes the calling player from clan clanID of their game. If the caller is
// the owner and others remain, the member who joined first becomes the owner; if the
// caller is the only member, the clan is deleted. It does not create a player record: a
// player we have never seen is simply not a member.
//
// Errors the client can act on:
//   - 404 clan_not_found: no such clan in the player's game (a clan of another game counts
//     as not found)
//   - 409 not_in_clan: the player is not a member of this clan
func (s *Service) LeaveClan(ctx context.Context, gameID, externalID, clanID string) error {
	if !uuidPattern.MatchString(clanID) {
		return errClanNotFound()
	}
	err := s.store.LeaveClan(ctx, gameID, strings.ToLower(clanID), externalID)
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return errClanNotFound()
	case errors.Is(err, repository.ErrNotInClan):
		return httpx.NewError(http.StatusConflict, "not_in_clan", "You are not a member of this clan")
	case err != nil:
		return fmt.Errorf("leave clan: %w", err)
	}
	return nil
}

func errClanNotFound() error {
	return httpx.NewError(http.StatusNotFound, "clan_not_found", "Clan not found")
}

// PlayerClan is the clan a player is in, and their role in it ("owner" or "member").
type PlayerClan struct {
	Clan Clan
	Role string
}

// MyClan returns the calling player's clan and role, or 404 not_in_clan.
// It only reads, so it does not create the player record.
func (s *Service) MyClan(ctx context.Context, gameID, externalID string) (PlayerClan, error) {
	row, err := s.store.MyClan(ctx, gameID, externalID)
	if errors.Is(err, repository.ErrNotInClan) {
		return PlayerClan{}, httpx.NewError(http.StatusNotFound, "not_in_clan", "You are not in a clan")
	}
	if err != nil {
		return PlayerClan{}, fmt.Errorf("my clan: %w", err)
	}
	c := row.Clan
	return PlayerClan{
		Clan: Clan{ID: c.ID, Name: c.Name, MemberCount: c.MemberCount, MaxMembers: c.MaxMembers},
		Role: row.Role,
	}, nil
}

// Member is one member of a clan.
type Member struct {
	PlayerID    string // social's own player ID
	DisplayName string
	Role        string // "owner" or "member"
	JoinedAt    time.Time
}

// ListMembers returns the members of clan clanID of the player's game, owner first.
// Any player of the game may look, so players can see who is in a clan before joining.
// A bad ID, an unknown clan and another game's clan all give 404 clan_not_found.
func (s *Service) ListMembers(ctx context.Context, gameID, clanID string) ([]Member, error) {
	if !uuidPattern.MatchString(clanID) {
		return nil, errClanNotFound()
	}
	rows, err := s.store.ListMembers(ctx, gameID, strings.ToLower(clanID))
	if errors.Is(err, repository.ErrClanNotFound) {
		return nil, errClanNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	members := make([]Member, 0, len(rows))
	for _, m := range rows {
		members = append(members, Member{PlayerID: m.PlayerID, DisplayName: m.DisplayName, Role: m.Role, JoinedAt: m.JoinedAt})
	}
	return members, nil
}

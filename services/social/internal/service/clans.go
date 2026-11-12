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

	"gitlab.com/clan-platform/clan-backend/pkg/events"
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
	// maxClanDescriptionLen limits the clan description, counted in characters (not bytes).
	// A clan without a description is a valid clan: the creation form asks for one, the
	// contract does not, so a game that does not ask keeps working.
	maxClanDescriptionLen = 300
	// maxClanBadgeLen limits the badge id. A badge is only an id such as "wolf": the picture
	// belongs to the game, which lists its badges in the widget config in Studio and ships
	// the same assets with the game, so Social never stores or serves an image.
	maxClanBadgeLen = 64
	// maxClanTagLen limits the clan tag, the few characters shown above a member's name.
	// The clan form allows 4; this is the contract's own limit, a little wider so the form
	// can be widened without a migration.
	maxClanTagLen = 8
)

// Clan is a clan as shown in clan lists.
type Clan struct {
	ID          string
	Name        string
	Description string
	Badge       string
	Tag         string
	MemberCount int
	MaxMembers  int
}

// NewClan is what a player sends when they create a clan. Only Name is required.
type NewClan struct {
	Name        string
	Description string
	Badge       string
	Tag         string
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

	opts.MaxMembers = s.maxMembers(ctx, gameID)
	rows, err := s.store.ListClans(ctx, gameID, q, opts)
	if err != nil {
		return nil, fmt.Errorf("list clans: %w", err)
	}
	clans := make([]Clan, 0, len(rows))
	for _, c := range rows {
		clans = append(clans, toClan(c, opts.MaxMembers))
	}
	return clans, nil
}

// CreateClan creates a clan in gameID. The calling player (from the token) becomes its
// owner and first member. The player record is created first if this is the player's
// first request.
//
// Only in.Name is required. in.Badge is the id of a badge from the game's widget config,
// never a picture: the developer adds the image in Studio and ships the same asset with
// the game, so Social stores the id and nothing else.
//
// Errors the client can act on:
//   - 400 invalid_clan_name: the name is not 3 to 20 characters, or has invisible characters
//   - 400 invalid_clan_description: the description is longer than 300 characters or has
//     invisible characters
//   - 400 invalid_clan_badge: the badge id is too long or has characters a badge id cannot have
//   - 400 invalid_clan_tag: the tag is longer than 8 characters or is not only letters and digits
//   - 409 clan_name_taken: the game already has a clan with this name (ignoring upper/lower case)
//   - 409 already_in_clan: the player is already in a clan of this game
func (s *Service) CreateClan(ctx context.Context, gameID, externalID, displayName string, in NewClan) (Clan, error) {
	clean, err := cleanClanName(in.Name)
	if err != nil {
		return Clan{}, err
	}
	description, err := cleanClanDescription(in.Description)
	if err != nil {
		return Clan{}, err
	}
	badge, err := cleanClanBadge(in.Badge)
	if err != nil {
		return Clan{}, err
	}
	tag, err := cleanClanTag(in.Tag)
	if err != nil {
		return Clan{}, err
	}

	owner, err := s.EnsurePlayer(ctx, gameID, externalID, displayName)
	if err != nil {
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}

	row, err := s.store.CreateClan(ctx, gameID, owner.ID, repository.NewClan{
		Name:        clean,
		Description: description,
		Badge:       badge,
		Tag:         tag,
	})
	switch {
	case errors.Is(err, repository.ErrClanNameTaken):
		return Clan{}, httpx.NewError(http.StatusConflict, "clan_name_taken", "A clan with this name already exists")
	case errors.Is(err, repository.ErrAlreadyInClan):
		return Clan{}, httpx.NewError(http.StatusConflict, "already_in_clan", "You are already in a clan; leave it first")
	case err != nil:
		return Clan{}, fmt.Errorf("create clan: %w", err)
	}
	_ = s.events.Publish(ctx, events.New(EventClanCreated, gameID, map[string]any{
		"clan":   ref(row.ID, row.Name),
		"player": ref(owner.ID, owner.DisplayName),
	}))
	return toClan(row, s.maxMembers(ctx, gameID)), nil
}

// Clan events for the other services (docs/event-messages.md). Each is published after the
// change is committed; a clan and a player are always {id, name}.
const (
	EventClanCreated      = "clan.created"       // {clan, player}: player is the owner
	EventClanMemberJoined = "clan.member_joined" // {clan, player}
	EventClanMemberLeft   = "clan.member_left"   // {clan, player, reason, kicked_by, new_owner}
	EventClanDisbanded    = "clan.disbanded"     // {clan}: the last member left
	EventClanRenamed      = "clan.renamed"       // {clan, previous_name}: clan has the new name
)

// ref is how events name a clan or a player.
func ref(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name}
}

// toClan is a clan row as the API shows it, with the game's clan size as maxMembers. A clan
// over a lowered limit shows memberCount above maxMembers: it keeps its members, takes no one.
func toClan(c repository.Clan, maxMembers int) Clan {
	return Clan{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Badge:       c.Badge,
		Tag:         c.Tag,
		MemberCount: c.MemberCount,
		MaxMembers:  maxMembers,
	}
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

// cleanClanDescription trims the description and checks it; an empty one is allowed.
// A browser textarea sends "\r\n" for a new line, so line ends are made plain "\n" first,
// and then only line breaks are allowed among the invisible characters.
func cleanClanDescription(description string) (string, error) {
	clean := strings.ReplaceAll(description, "\r\n", "\n")
	clean = strings.TrimSpace(strings.ReplaceAll(clean, "\r", "\n"))
	if utf8.RuneCountInString(clean) > maxClanDescriptionLen {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_description",
			fmt.Sprintf("Clan description can be at most %d characters", maxClanDescriptionLen))
	}
	for _, r := range clean {
		if r != '\n' && !unicode.IsPrint(r) {
			return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_description",
				"Clan description can only have visible characters and line breaks")
		}
	}
	return clean, nil
}

// badgePattern is the shape of a badge id in a game's widget config: letters and digits,
// with the separators asset ids use.
var badgePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// cleanClanBadge trims the badge id and checks its shape; an empty one is allowed. The id
// is not looked up in the game's config: Studio decides which badges exist and the clan
// form offers only those, and a game whose config cannot be read at this moment should
// still be able to create clans.
func cleanClanBadge(badge string) (string, error) {
	clean := strings.TrimSpace(badge)
	if clean == "" {
		return "", nil
	}
	if utf8.RuneCountInString(clean) > maxClanBadgeLen || !badgePattern.MatchString(clean) {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_badge",
			fmt.Sprintf("Clan badge must be at most %d characters of letters, digits, '-', '_', '.' or ':'",
				maxClanBadgeLen))
	}
	return clean, nil
}

// tagPattern is the shape of a clan tag: letters and digits only, so a tag cannot be made
// of spaces or punctuation that would not read as a tag above a player's name.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// cleanClanTag trims the tag and checks it; an empty one is allowed, and is what a game
// that does not use tags (clan.tags is false in its config) always sends. Upper and lower
// case are kept as given: the clan form already writes tags in capitals.
func cleanClanTag(tag string) (string, error) {
	clean := strings.TrimSpace(tag)
	if clean == "" {
		return "", nil
	}
	if utf8.RuneCountInString(clean) > maxClanTagLen || !tagPattern.MatchString(clean) {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_clan_tag",
			fmt.Sprintf("Clan tag must be at most %d letters or digits", maxClanTagLen))
	}
	return clean, nil
}

// uuidPattern matches a UUID such as 3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// JoinClan adds the calling player to clan clanID of their game with the first role of the
// game's clan.roles (what a new member gets; "member" before the game publishes roles) and
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

	rules, ladder := s.clanRules(ctx, gameID)
	limit := rules.MaxMembers
	row, err := s.store.JoinClan(ctx, gameID, strings.ToLower(clanID), player.ID, limit, ladder.NewMemberRole())
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
	_ = s.events.Publish(ctx, events.New(EventClanMemberJoined, gameID, map[string]any{
		"clan":   ref(row.ID, row.Name),
		"player": ref(player.ID, player.DisplayName),
	}))
	return toClan(row, limit), nil
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
	res, err := s.store.LeaveClan(ctx, gameID, strings.ToLower(clanID), externalID)
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return errClanNotFound()
	case errors.Is(err, repository.ErrNotInClan):
		return httpx.NewError(http.StatusConflict, "not_in_clan", "You are not a member of this clan")
	case err != nil:
		return fmt.Errorf("leave clan: %w", err)
	}
	s.publishLeave(ctx, gameID, res)
	return nil
}

// KickMember removes player playerID from clan clanID of the caller's game. The game's clan
// roles decide who may: a member whose role has canKick, on someone of a lower role (the
// owner may kick anyone). Nobody kicks themselves (they leave instead). The kicked player
// can join a clan again at once; there is no ban.
//
// Errors the client can act on:
//   - 404 clan_not_found: no such clan in the caller's game
//   - 403 not_allowed: the caller's role does not allow it, or the caller is not in the clan
//   - 400 cannot_kick_self: the caller named themselves
//   - 404 member_not_found: the player is not a member of this clan
func (s *Service) KickMember(ctx context.Context, gameID, externalID, clanID, playerID string) error {
	if !uuidPattern.MatchString(clanID) {
		return errClanNotFound()
	}
	if !uuidPattern.MatchString(playerID) {
		return errMemberNotFound()
	}
	_, ladder := s.clanRules(ctx, gameID)
	res, err := s.store.KickMember(ctx, gameID, strings.ToLower(clanID), externalID, strings.ToLower(playerID),
		func(actor, target repository.MemberRole) bool {
			return ladder.CanKick(toRoleMember(actor), toRoleMember(target))
		})
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return errClanNotFound()
	case errors.Is(err, repository.ErrNotAllowed):
		return errNotAllowed()
	case errors.Is(err, repository.ErrKickSelf):
		return httpx.NewError(http.StatusBadRequest, "cannot_kick_self", "You cannot remove yourself; leave the clan instead")
	case errors.Is(err, repository.ErrMemberNotFound):
		return errMemberNotFound()
	case err != nil:
		return fmt.Errorf("kick member: %w", err)
	}
	s.publishLeave(ctx, gameID, res)
	return nil
}

// RenameClan gives clan clanID of the caller's game a new name and returns the clan. Only the
// clan's owner may do it. The name is cleaned and checked like on create. Sending the name
// the clan already has changes nothing and publishes nothing; a change of upper/lower case
// only is a rename.
//
// Errors the client can act on:
//   - 400 invalid_clan_name: the name is not 3 to 20 characters, or has invisible characters
//   - 404 clan_not_found: no such clan in the caller's game
//   - 403 not_clan_owner: the caller is not this clan's owner
//   - 409 clan_name_taken: another clan of the game has this name (ignoring upper/lower case)
func (s *Service) RenameClan(ctx context.Context, gameID, externalID, clanID, name string) (Clan, error) {
	clean, err := cleanClanName(name)
	if err != nil {
		return Clan{}, err
	}
	if !uuidPattern.MatchString(clanID) {
		return Clan{}, errClanNotFound()
	}
	res, err := s.store.RenameClan(ctx, gameID, strings.ToLower(clanID), externalID, clean)
	switch {
	case errors.Is(err, repository.ErrClanNotFound):
		return Clan{}, errClanNotFound()
	case errors.Is(err, repository.ErrNotClanOwner):
		return Clan{}, httpx.NewError(http.StatusForbidden, "not_clan_owner", "Only the clan owner can rename the clan")
	case errors.Is(err, repository.ErrClanNameTaken):
		return Clan{}, httpx.NewError(http.StatusConflict, "clan_name_taken", "A clan with this name already exists")
	case err != nil:
		return Clan{}, fmt.Errorf("rename clan: %w", err)
	}
	c := res.Clan
	if res.Changed {
		_ = s.events.Publish(ctx, events.New(EventClanRenamed, gameID, map[string]any{
			"clan":          ref(c.ID, c.Name),
			"previous_name": res.PreviousName,
		}))
	}
	return toClan(c, s.maxMembers(ctx, gameID)), nil
}

func errMemberNotFound() error {
	return httpx.NewError(http.StatusNotFound, "member_not_found", "This player is not a member of the clan")
}

// publishLeave tells what a leave or a kick did: clan.disbanded when the last member left (the
// clan is gone, so there is no member_left), otherwise clan.member_left with reason "left" or
// "kicked".
func (s *Service) publishLeave(ctx context.Context, gameID string, res repository.LeaveResult) {
	clan := ref(res.ClanID, res.ClanName)
	if res.Disbanded {
		_ = s.events.Publish(ctx, events.New(EventClanDisbanded, gameID, map[string]any{"clan": clan}))
		return
	}
	var newOwner any // JSON null when the owner did not change
	if res.NewOwner != nil {
		newOwner = ref(res.NewOwner.ID, res.NewOwner.DisplayName)
	}
	reason, kickedBy := "left", any(nil) // kicked_by is JSON null for a player who left
	if res.KickedBy != nil {
		reason, kickedBy = "kicked", ref(res.KickedBy.ID, res.KickedBy.DisplayName)
	}
	_ = s.events.Publish(ctx, events.New(EventClanMemberLeft, gameID, map[string]any{
		"clan":      clan,
		"player":    ref(res.Player.ID, res.Player.DisplayName),
		"reason":    reason,
		"kicked_by": kickedBy,
		"new_owner": newOwner,
	}))
}

func errClanNotFound() error {
	return httpx.NewError(http.StatusNotFound, "clan_not_found", "Clan not found")
}

// PlayerClan is the clan a player is in, and their role in it ("owner" or "member").
type PlayerClan struct {
	Clan Clan
	Role string // a role id of the game's clan.roles; the owner has the last one
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
	rules, ladder := s.clanRules(ctx, gameID)
	return PlayerClan{
		Clan: toClan(c, rules.MaxMembers),
		Role: ladder.Of(storedRole(row.Role)).ID,
	}, nil
}

// Member is one member of a clan.
type Member struct {
	PlayerID    string // social's own player ID
	DisplayName string
	Role        string // a role id of the game's clan.roles; the owner has the last one
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
	_, ladder := s.clanRules(ctx, gameID)
	members := make([]Member, 0, len(rows))
	for _, m := range rows {
		role := ladder.Of(storedRole(m.Role)).ID
		members = append(members, Member{PlayerID: m.PlayerID, DisplayName: m.DisplayName, Role: role, JoinedAt: m.JoinedAt})
	}
	return members, nil
}

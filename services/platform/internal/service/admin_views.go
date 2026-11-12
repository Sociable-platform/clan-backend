package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Paging and search limits of the admin lists.
const (
	defaultPageSize = 25
	maxPageSize     = 100
	maxSearchLen    = 100
	maxCursorLen    = 200
)

// Account statuses (developers.status and games.status).
const (
	StatusActive    = repository.StatusActive
	StatusSuspended = repository.StatusSuspended
)

// ListDevelopersInput selects a page of developers for the admin panel.
type ListDevelopersInput struct {
	Query  string // part of the email or name
	Status string // active, suspended, or empty for both
	Limit  int    // 0 means the default
	Cursor string // NextCursor of the previous page, or empty
}

// DeveloperSummary is a row of the admin developer list.
type DeveloperSummary struct {
	ID        string
	Email     string
	Name      string
	Status    string
	GameCount int
	CreatedAt time.Time
}

// DeveloperPage is one page of the admin developer list.
type DeveloperPage struct {
	Developers []DeveloperSummary
	// NextCursor goes into the next request; empty on the last page.
	NextCursor string
}

// AdminGame is a game with its status, as the admin detail lists it.
type AdminGame struct {
	ID              string
	Name            string
	Description     string
	Status          string
	StatusReason    string
	StatusChangedAt *time.Time
	CreatedAt       time.Time
}

// AdminDeveloper is a developer with their games, as the admin detail shows it.
type AdminDeveloper struct {
	ID              string
	Email           string
	Name            string
	Status          string
	StatusReason    string
	StatusChangedAt *time.Time
	CreatedAt       time.Time
	Games           []AdminGame
}

// AuditLogInput selects a page of the audit log.
type AuditLogInput struct {
	TargetID string // a developer or game ID, or empty for every entry
	Limit    int    // 0 means the default
	Cursor   string // NextCursor of the previous page, or empty
}

// AuditEntry is one change of access in the audit log.
type AuditEntry struct {
	ID         string
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Reason     string
	Note       string
	CreatedAt  time.Time
}

// AuditPage is one page of the audit log, newest first.
type AuditPage struct {
	Entries    []AuditEntry
	NextCursor string
}

func errDeveloperNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "developer_not_found", "Developer not found")
}

// ListDevelopers returns a page of developers, newest first, for the admin panel.
func (s *Service) ListDevelopers(ctx context.Context, in ListDevelopersInput) (DeveloperPage, error) {
	limit, err := pageSize(in.Limit)
	if err != nil {
		return DeveloperPage{}, err
	}
	query := strings.TrimSpace(in.Query)
	if utf8.RuneCountInString(query) > maxSearchLen {
		return DeveloperPage{}, httpx.NewError(http.StatusBadRequest, "invalid_query",
			fmt.Sprintf("Search text must be at most %d characters", maxSearchLen))
	}
	if in.Status != "" && in.Status != StatusActive && in.Status != StatusSuspended {
		return DeveloperPage{}, httpx.NewError(http.StatusBadRequest, "invalid_status", "Status must be active or suspended")
	}
	after, err := decodeCursor(in.Cursor)
	if err != nil {
		return DeveloperPage{}, err
	}

	// One row more than the page shows tells whether there is a next page.
	rows, err := s.store.ListDevelopers(ctx, repository.DeveloperFilter{Query: query, Status: in.Status, After: after, Limit: limit + 1})
	if err != nil {
		return DeveloperPage{}, fmt.Errorf("list developers: %w", err)
	}
	page := DeveloperPage{Developers: []DeveloperSummary{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.NextCursor = encodeCursor(repository.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	for _, d := range rows {
		page.Developers = append(page.Developers, DeveloperSummary{
			ID: d.ID, Email: d.Email, Name: d.Name, Status: d.Status, GameCount: d.GameCount, CreatedAt: d.CreatedAt,
		})
	}
	return page, nil
}

// AdminDeveloper returns one developer with every game they own. A malformed ID and an unknown
// developer both give 404 developer_not_found.
func (s *Service) AdminDeveloper(ctx context.Context, developerID string) (AdminDeveloper, error) {
	if !uuidPattern.MatchString(developerID) {
		return AdminDeveloper{}, errDeveloperNotFound()
	}
	d, err := s.store.AdminDeveloperByID(ctx, developerID)
	if errors.Is(err, repository.ErrNotFound) {
		return AdminDeveloper{}, errDeveloperNotFound()
	}
	if err != nil {
		return AdminDeveloper{}, fmt.Errorf("find developer %s: %w", developerID, err)
	}
	games, err := s.store.AdminGamesByDeveloper(ctx, developerID)
	if err != nil {
		return AdminDeveloper{}, fmt.Errorf("list games of developer %s: %w", developerID, err)
	}
	return toAdminDeveloper(d, games), nil
}

func toAdminDeveloper(d repository.AdminDeveloper, games []repository.AdminGame) AdminDeveloper {
	out := AdminDeveloper{
		ID: d.ID, Email: d.Email, Name: d.Name, Status: d.Status, StatusReason: d.StatusReason,
		StatusChangedAt: d.StatusChangedAt, CreatedAt: d.CreatedAt, Games: make([]AdminGame, 0, len(games)),
	}
	for _, g := range games {
		out.Games = append(out.Games, toAdminGame(g))
	}
	return out
}

func toAdminGame(g repository.AdminGame) AdminGame {
	return AdminGame{
		ID: g.ID, Name: g.Name, Description: g.Description, Status: g.Status, StatusReason: g.StatusReason,
		StatusChangedAt: g.StatusChangedAt, CreatedAt: g.CreatedAt,
	}
}

// AuditLog returns a page of the audit log, newest first, optionally only the entries about one
// developer or game.
func (s *Service) AuditLog(ctx context.Context, in AuditLogInput) (AuditPage, error) {
	limit, err := pageSize(in.Limit)
	if err != nil {
		return AuditPage{}, err
	}
	if in.TargetID != "" && !uuidPattern.MatchString(in.TargetID) {
		return AuditPage{}, httpx.NewError(http.StatusBadRequest, "invalid_target_id", "targetId must be a UUID")
	}
	after, err := decodeCursor(in.Cursor)
	if err != nil {
		return AuditPage{}, err
	}

	rows, err := s.store.AuditEntries(ctx, repository.AuditFilter{TargetID: in.TargetID, After: after, Limit: limit + 1})
	if err != nil {
		return AuditPage{}, fmt.Errorf("read audit log: %w", err)
	}
	page := AuditPage{Entries: []AuditEntry{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.NextCursor = encodeCursor(repository.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	for _, e := range rows {
		page.Entries = append(page.Entries, AuditEntry(e))
	}
	return page, nil
}

// pageSize applies the default and checks the bounds of a requested page size.
func pageSize(requested int) (int, error) {
	switch {
	case requested == 0:
		return defaultPageSize, nil
	case requested < 0 || requested > maxPageSize:
		return 0, httpx.NewError(http.StatusBadRequest, "invalid_limit",
			fmt.Sprintf("Limit must be 1 to %d", maxPageSize))
	}
	return requested, nil
}

// cursorBody is what an opaque cursor holds. Clients must not rely on it.
type cursorBody struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

func encodeCursor(c repository.Cursor) string {
	b, _ := json.Marshal(cursorBody{T: c.CreatedAt.UTC(), ID: c.ID}) // two plain fields cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor returns nil for an empty cursor (the first page) and 400 invalid_cursor for
// anything that this service did not produce.
func decodeCursor(s string) (*repository.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	bad := httpx.NewError(http.StatusBadRequest, "invalid_cursor", "Cursor is not valid")
	if len(s) > maxCursorLen {
		return nil, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, bad
	}
	var c cursorBody
	if json.Unmarshal(raw, &c) != nil || c.T.IsZero() || !uuidPattern.MatchString(c.ID) {
		return nil, bad
	}
	return &repository.Cursor{CreatedAt: c.T, ID: c.ID}, nil
}

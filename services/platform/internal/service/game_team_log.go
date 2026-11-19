package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// logTeam records a change to a game's team. It is called inside the transaction of the change, so the change and its
// row are both kept or both lost: a team change without a history entry cannot happen, nor an entry for a change
// that was rolled back.
func (s *Service) logTeam(ctx context.Context, gameID string, in repository.TeamLogInput) error {
	if err := s.store.AddTeamLog(ctx, gameID, in); err != nil {
		return fmt.Errorf("record %s on game %s: %w", in.Action, gameID, err)
	}
	return nil
}

// TeamLogPerson is a developer in the history: who did something, or who it was done to.
type TeamLogPerson struct {
	DeveloperID string // empty for an invitation to an address that is not an account
	Name        string
	Email       string
}

// TeamLogEntry is one change to a game's team, as the admin panel reads it.
type TeamLogEntry struct {
	ID        string
	Action    string
	Actor     TeamLogPerson
	Subject   TeamLogPerson
	Role      string // the new role of a role change, else empty
	CreatedAt time.Time
}

// TeamLogPage is one page of a game's history, newest first.
type TeamLogPage struct {
	Entries    []TeamLogEntry
	NextCursor string // empty on the last page
}

// AdminTeamLog returns a page of the history of a game's team, for the admin panel: every invitation and answer,
// removal, leaving, role change and hand-over, each with who did it. A malformed ID and an unknown game both give 404
// game_not_found.
func (s *Service) AdminTeamLog(ctx context.Context, gameID string, limit int, cursor string) (TeamLogPage, error) {
	if !uuidPattern.MatchString(gameID) {
		return TeamLogPage{}, errGameNotFound()
	}
	size, err := pageSize(limit)
	if err != nil {
		return TeamLogPage{}, err
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return TeamLogPage{}, err
	}
	if _, err := s.store.AdminGameByID(ctx, gameID); errors.Is(err, repository.ErrNotFound) {
		return TeamLogPage{}, errGameNotFound()
	} else if err != nil {
		return TeamLogPage{}, fmt.Errorf("find game %s: %w", gameID, err)
	}
	rows, err := s.store.TeamLogOfGame(ctx, gameID, after, size+1)
	if err != nil {
		return TeamLogPage{}, fmt.Errorf("read team log of game %s: %w", gameID, err)
	}
	page := TeamLogPage{Entries: []TeamLogEntry{}}
	if len(rows) > size {
		rows = rows[:size]
		last := rows[size-1]
		page.NextCursor = encodeCursor(repository.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	for _, e := range rows {
		page.Entries = append(page.Entries, TeamLogEntry{
			ID: e.ID, Action: e.Action, Role: e.Role, CreatedAt: e.CreatedAt,
			Actor:   TeamLogPerson{DeveloperID: e.ActorID, Name: e.ActorName, Email: e.ActorEmail},
			Subject: TeamLogPerson{DeveloperID: e.SubjectID, Name: e.SubjectName, Email: e.SubjectEmail},
		})
	}
	return page, nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Reasons for switching access off. The developer sees a message for the code, never the note.
const (
	ReasonNonPayment = "non_payment"
	ReasonAbuse      = "abuse"
	ReasonOther      = "other"
)

// maxStatusNoteLen limits the internal note of a status change.
const maxStatusNoteLen = 1000

// SetStatusInput is a request to suspend or resume a developer or a game.
type SetStatusInput struct {
	Status string // active (resume) or suspended
	// Reason is required when suspending: non_payment, abuse or other. When resuming it may be empty.
	Reason string
	// Note is internal text for the audit log. It is never returned by any developer route.
	Note string
}

// SetDeveloperStatus suspends or resumes a developer. Their games keep their own status: a game is
// served only when it and its developer are both active, so one switch turns off every game of the
// developer without changing any game row. actor says who did it: an admin's ID today, "system:billing" for a billing webhook later. The change and its audit
// row are written in one transaction. Asking for the status the developer already has changes
// nothing, writes no audit row and is not an error. It returns the developer as it is afterwards.
func (s *Service) SetDeveloperStatus(ctx context.Context, actor, developerID string, in SetStatusInput) (AdminDeveloper, error) {
	if !uuidPattern.MatchString(developerID) {
		return AdminDeveloper{}, errDeveloperNotFound()
	}
	change, err := newStatusChange("developer", actor, in)
	if err != nil {
		return AdminDeveloper{}, err
	}
	d, _, err := s.store.SetDeveloperStatus(ctx, developerID, change)
	if errors.Is(err, repository.ErrNotFound) {
		return AdminDeveloper{}, errDeveloperNotFound()
	}
	if err != nil {
		return AdminDeveloper{}, fmt.Errorf("set status of developer %s: %w", developerID, err)
	}
	games, err := s.store.AdminGamesByDeveloper(ctx, developerID)
	if err != nil {
		return AdminDeveloper{}, fmt.Errorf("list games of developer %s: %w", developerID, err)
	}
	return toAdminDeveloper(d, games), nil
}

// SetGameStatus suspends or resumes one game, with the same rules as SetDeveloperStatus.
func (s *Service) SetGameStatus(ctx context.Context, actor, gameID string, in SetStatusInput) (AdminGame, error) {
	if !uuidPattern.MatchString(gameID) {
		return AdminGame{}, errGameNotFound()
	}
	change, err := newStatusChange("game", actor, in)
	if err != nil {
		return AdminGame{}, err
	}
	g, _, err := s.store.SetGameStatus(ctx, gameID, change)
	if errors.Is(err, repository.ErrNotFound) {
		return AdminGame{}, errGameNotFound()
	}
	if err != nil {
		return AdminGame{}, fmt.Errorf("set status of game %s: %w", gameID, err)
	}
	return toAdminGame(g), nil
}

// newStatusChange validates in and builds the change for target ("developer" or "game").
func newStatusChange(target, actor string, in SetStatusInput) (repository.StatusChange, error) {
	if actor == "" {
		return repository.StatusChange{}, errors.New("status change without an actor") // a bug in the caller, not a bad request
	}
	reason := strings.TrimSpace(in.Reason)
	note := strings.TrimSpace(in.Note)

	var verb string
	switch in.Status {
	case StatusSuspended:
		verb = "suspended"
		if reason == "" {
			return repository.StatusChange{}, httpx.NewError(http.StatusBadRequest, "reason_required",
				"A reason is required when suspending: non_payment, abuse or other")
		}
	case StatusActive:
		verb = "resumed"
	default:
		return repository.StatusChange{}, httpx.NewError(http.StatusBadRequest, "invalid_status", "Status must be active or suspended")
	}
	if reason != "" && reason != ReasonNonPayment && reason != ReasonAbuse && reason != ReasonOther {
		return repository.StatusChange{}, httpx.NewError(http.StatusBadRequest, "invalid_reason", "Reason must be non_payment, abuse or other")
	}
	if utf8.RuneCountInString(note) > maxStatusNoteLen {
		return repository.StatusChange{}, httpx.NewError(http.StatusBadRequest, "invalid_note",
			fmt.Sprintf("Note must be at most %d characters", maxStatusNoteLen))
	}
	return repository.StatusChange{
		Actor: actor, Action: target + "." + verb, Status: in.Status, Reason: reason, Note: note,
	}, nil
}

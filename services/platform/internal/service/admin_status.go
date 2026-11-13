package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/clan-platform/clan-backend/pkg/events"
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

// Events published after a game stops or starts being served. data is { "status" }: "suspended" or
// "active", the same word the internal game config uses.
const (
	EventGameSuspended = "game.suspended"
	EventGameResumed   = "game.resumed"
)

// publishStatusTimeout bounds all the events of one status change together. A developer with many
// games is announced one game at a time, and a broker that does not answer must not keep the
// admin waiting for each of them.
const publishStatusTimeout = 10 * time.Second

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
// nothing, writes no audit row, publishes no event and is not an error. After a change is saved,
// each game that this switch turns off or on is announced (game.suspended or game.resumed); a game
// that was suspended on its own is not, because it is served or not served the same as before.
// It returns the developer as it is afterwards.
func (s *Service) SetDeveloperStatus(ctx context.Context, actor, developerID string, in SetStatusInput) (AdminDeveloper, error) {
	if !uuidPattern.MatchString(developerID) {
		return AdminDeveloper{}, errDeveloperNotFound()
	}
	change, err := newStatusChange("developer", actor, in)
	if err != nil {
		return AdminDeveloper{}, err
	}
	d, changed, err := s.store.SetDeveloperStatus(ctx, developerID, change)
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
	if changed {
		s.publishDeveloperGames(ctx, d.Status, games)
	}
	return toAdminDeveloper(d, games), nil
}

// SetGameStatus suspends or resumes one game, with the same rules as SetDeveloperStatus. The game's
// own change is announced unless its developer is suspended: then the game is not served either
// way, so nothing changed for the other services.
func (s *Service) SetGameStatus(ctx context.Context, actor, gameID string, in SetStatusInput) (AdminGame, error) {
	if !uuidPattern.MatchString(gameID) {
		return AdminGame{}, errGameNotFound()
	}
	change, err := newStatusChange("game", actor, in)
	if err != nil {
		return AdminGame{}, err
	}
	g, changed, err := s.store.SetGameStatus(ctx, gameID, change)
	if errors.Is(err, repository.ErrNotFound) {
		return AdminGame{}, errGameNotFound()
	}
	if err != nil {
		return AdminGame{}, fmt.Errorf("set status of game %s: %w", gameID, err)
	}
	if changed {
		s.publishGameChange(ctx, g)
	}
	return toAdminGame(g), nil
}

// publishGameChange announces the change of a game's own status, if it changes whether the game is
// served. The change is saved already, so a failure here is never returned to the admin.
func (s *Service) publishGameChange(ctx context.Context, g repository.AdminGame) {
	ctx, cancel := publishContext(ctx) // the check below also must not die with the admin's request
	defer cancel()
	a, err := s.store.GameAccessOf(ctx, g.ID)
	switch {
	case err == nil && a.DeveloperStatus != StatusActive:
		return // the developer's switch is off: the game was not served before and is not now
	case err != nil && g.Status != StatusSuspended:
		// Cannot tell whether a resumed game is served again. Staying silent is the safe side: an
		// announcement that a game is back, when it is not, would let it through. A suspension is
		// always safe to announce, so it still goes out.
		return
	}
	s.publishGameStatus(ctx, g.ID, g.Status)
}

// publishDeveloperGames announces, for each game of a developer whose own switch is on, that the
// developer's new status turns it off or on. Games suspended on their own are skipped: they stay
// off after the developer is resumed, so a game.resumed for them would be wrong.
func (s *Service) publishDeveloperGames(ctx context.Context, developerStatus string, games []repository.AdminGame) {
	ctx, cancel := publishContext(ctx)
	defer cancel()
	for _, g := range games {
		if g.Status == StatusActive {
			s.publishGameStatus(ctx, g.ID, developerStatus)
		}
	}
}

// publishContext is the context for the events of a change that is already saved: it ignores the
// admin hanging up, which must not lose the announcement, and ends after publishStatusTimeout.
func publishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), publishStatusTimeout)
}

// publishGameStatus sends game.suspended or game.resumed for gameID. The publisher is wrapped in
// events.Safe, so a broker problem never fails the admin's request.
func (s *Service) publishGameStatus(ctx context.Context, gameID, status string) {
	eventType := EventGameResumed
	if status == StatusSuspended {
		eventType = EventGameSuspended
	}
	_ = s.events.Publish(ctx, events.New(eventType, gameID, map[string]any{"status": status}))
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

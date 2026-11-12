package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// whySuspended says which switch is off for a game that is not served.
type whySuspended int

const (
	notSuspended       whySuspended = iota // the game is served
	gameIsSuspended                        // the game's own switch is off
	developerSuspended                     // the developer's switch is off (every game of theirs is)
)

// suspension reports why gameID is not served. A game is effectively active only when the game and
// its developer are both active; if both are suspended the developer wins, because that is the
// switch that turns off the whole account. A game that does not exist counts as not suspended: each
// caller already has its own answer for an unknown game (a key always has a game, the public widget
// route says "not published").
func (s *Service) suspension(ctx context.Context, gameID string) (whySuspended, error) {
	a, err := s.store.GameAccessOf(ctx, gameID)
	if errors.Is(err, repository.ErrNotFound) {
		return notSuspended, nil
	}
	if err != nil {
		return notSuspended, fmt.Errorf("check access of game %s: %w", gameID, err)
	}
	switch {
	case a.DeveloperStatus != repository.StatusActive:
		return developerSuspended, nil
	case a.GameStatus != repository.StatusActive:
		return gameIsSuspended, nil
	}
	return notSuspended, nil
}

// EnsureGameServed returns nil when gameID may use the product, and otherwise 403
// account_suspended (the developer is suspended) or 403 game_suspended (only this game is). The
// game's own server sees these answers, so the message may name the account; players never do.
// It is for callers that proved they speak for the game, such as the player-token endpoint.
func (s *Service) EnsureGameServed(ctx context.Context, gameID string) error {
	why, err := s.suspension(ctx, gameID)
	if err != nil {
		return err
	}
	switch why {
	case developerSuspended:
		return httpx.NewError(http.StatusForbidden, "account_suspended", "The developer account is suspended")
	case gameIsSuspended:
		return errGameSuspended()
	}
	return nil
}

func errGameSuspended() *httpx.Error {
	return httpx.NewError(http.StatusForbidden, "game_suspended", "This game is suspended")
}

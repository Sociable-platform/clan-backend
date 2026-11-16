package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// defaultRefreshTokenTTL is used until WithRefreshTokenTTL is called. Every refresh gives a
	// new token with the full lifetime, so a developer who keeps using the dashboard stays logged
	// in, and one who stays away this long has to log in again.
	defaultRefreshTokenTTL = 7 * 24 * time.Hour
	// refreshReuseGrace is how long after a refresh the replaced token is still accepted. It
	// covers the same refresh arriving twice: the answer was lost on the way and the browser
	// asks again, or two tabs start at the same moment. Later than this, a replaced token can
	// only come from a copy, and the session is ended.
	refreshReuseGrace = 10 * time.Second
)

// WithRefreshTokenTTL sets how long a refresh token stays valid (default 7 days). It returns s,
// so it can follow New.
func (s *Service) WithRefreshTokenTTL(ttl time.Duration) *Service {
	s.refreshTokenTTL = ttl
	return s
}

func errInvalidRefreshToken() *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, "invalid_refresh_token", "Your session has ended. Log in again.")
}

// startRefreshSession creates the refresh token of a developer who just logged in and returns it
// with its lifetime in seconds. Only the token's hash is stored.
func (s *Service) startRefreshSession(ctx context.Context, developerID string) (string, int, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return "", 0, err
	}
	now := s.now()
	if err := s.store.CreateRefreshSession(ctx, developerID, hashOpaqueToken(token), now.Add(s.refreshTokenTTL), now); err != nil {
		return "", 0, fmt.Errorf("store refresh token: %w", err)
	}
	return token, int(s.refreshTokenTTL.Seconds()), nil
}

// RefreshSession exchanges a refresh token for a new developer token and a new refresh token of
// the same session (rotation); the one that was sent stops working. Anything else than the
// session's current token gives invalid_refresh_token: an unknown or expired token, a session
// that was logged out, and a token that was already replaced. In that last case somebody is
// using a copy, so the whole session is ended (see refreshReuseGrace for the one exception).
//
// A suspended developer can refresh, like they can log in. The session of an account that is
// gone is refused.
func (s *Service) RefreshSession(ctx context.Context, refreshToken string) (Session, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || len(refreshToken) > maxOpaqueTokenLen {
		return Session{}, errInvalidRefreshToken()
	}
	next, err := newOpaqueToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	developerID, err := s.store.RotateRefreshToken(ctx, hashOpaqueToken(refreshToken), hashOpaqueToken(next),
		now.Add(s.refreshTokenTTL), now, refreshReuseGrace)
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRefreshTokenReused) {
		return Session{}, errInvalidRefreshToken()
	}
	if err != nil {
		return Session{}, fmt.Errorf("rotate refresh token: %w", err)
	}

	d, err := s.store.DeveloperByID(ctx, developerID)
	if errors.Is(err, repository.ErrNotFound) {
		return Session{}, errInvalidRefreshToken()
	}
	if err != nil {
		return Session{}, fmt.Errorf("find developer %s: %w", developerID, err)
	}
	token, err := jwt.Sign(s.key, jwt.Claims{Subject: d.ID, Name: d.Name, Type: jwt.TypeDeveloper}, s.tokenTTL, now)
	if err != nil {
		return Session{}, fmt.Errorf("sign developer token: %w", err)
	}
	return Session{
		Token: token, ExpiresIn: int(s.tokenTTL.Seconds()),
		RefreshToken: next, RefreshExpiresIn: int(s.refreshTokenTTL.Seconds()),
	}, nil
}

// Logout ends the session of that refresh token: it and the tokens it replaced are deleted, so no
// new developer token can be had with any of them. The developer token already issued stays valid
// until it expires, which is why it is short. An empty or unknown token is not an error: the
// caller is logged out either way.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || len(refreshToken) > maxOpaqueTokenLen {
		return nil
	}
	if err := s.store.DeleteRefreshSession(ctx, hashOpaqueToken(refreshToken)); err != nil {
		return fmt.Errorf("delete refresh session: %w", err)
	}
	return nil
}

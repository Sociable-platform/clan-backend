package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Admin is an admin account without its password hash.
type Admin struct {
	ID        string
	Email     string
	Name      string
	CreatedAt time.Time
}

func errAdminUnauthorized() *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, "unauthorized", "A valid admin token is required")
}

// AdminLogin checks the email and password of an admin and returns an admin token.
// An unknown email, a wrong password and a disabled admin all give the same error, so the
// answer does not tell an outsider which admin accounts exist.
func (s *Service) AdminLogin(ctx context.Context, in LoginInput) (Session, error) {
	a, err := s.store.AdminByEmail(ctx, normalizeEmail(in.Email))
	if errors.Is(err, repository.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(in.Password)) // spend the same time; result is irrelevant
		return Session{}, errInvalidCredentials()
	}
	if err != nil {
		return Session{}, fmt.Errorf("find admin: %w", err)
	}
	// Compare before looking at disabled_at, so a disabled admin costs the same time as a wrong password.
	if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(in.Password)) != nil || a.DisabledAt != nil {
		return Session{}, errInvalidCredentials()
	}

	token, err := jwt.Sign(s.key, jwt.Claims{Subject: a.ID, Name: a.Name, Type: jwt.TypeAdmin}, s.adminTokenTTL, s.now())
	if err != nil {
		return Session{}, fmt.Errorf("sign admin token: %w", err)
	}
	return Session{Token: token, ExpiresIn: int(s.adminTokenTTL.Seconds())}, nil
}

// AuthenticateAdmin checks an admin token (signature, expiry and type "admin") and that the
// admin behind it still exists and is not disabled, then returns the admin's ID. Developer and
// player tokens are rejected. The database is asked on every call so that disabling an admin
// takes effect at once instead of when their token expires; admin traffic is small enough for that.
func (s *Service) AuthenticateAdmin(ctx context.Context, token string) (string, error) {
	c, err := jwt.Verify(s.pub, token, jwt.TypeAdmin, s.now())
	if err != nil {
		return "", errAdminUnauthorized()
	}
	a, err := s.store.AdminByID(ctx, c.Subject)
	if errors.Is(err, repository.ErrNotFound) {
		return "", errAdminUnauthorized()
	}
	if err != nil {
		return "", fmt.Errorf("find admin %s: %w", c.Subject, err)
	}
	if a.DisabledAt != nil {
		return "", errAdminUnauthorized()
	}
	return a.ID, nil
}

// AdminMe returns the admin with the given ID (taken from a verified token).
func (s *Service) AdminMe(ctx context.Context, id string) (Admin, error) {
	a, err := s.store.AdminByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return Admin{}, errAdminUnauthorized() // the account behind a valid token is gone
	}
	if err != nil {
		return Admin{}, fmt.Errorf("find admin %s: %w", id, err)
	}
	if a.DisabledAt != nil {
		return Admin{}, errAdminUnauthorized()
	}
	return Admin{ID: a.ID, Email: a.Email, Name: a.Name, CreatedAt: a.CreatedAt}, nil
}

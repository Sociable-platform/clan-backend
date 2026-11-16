package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// Limits for developer input.
const (
	maxEmailLen    = 254
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt ignores (and the library rejects) anything longer, in bytes
	maxNameLen     = 100
)

// Developer is a developer account without its password hash.
type Developer struct {
	ID        string
	Email     string
	Name      string
	CreatedAt time.Time
	// Status is "active" or "suspended". StatusReason (non_payment, abuse, other) says why while
	// suspended and is empty otherwise. The admin's internal note is never part of this.
	Status       string
	StatusReason string
}

// RegisterInput is what a new developer sends.
type RegisterInput struct {
	Email    string
	Password string
	Name     string
}

// LoginInput is what a developer sends to log in.
type LoginInput struct {
	Email    string
	Password string
}

// Session is a freshly issued developer (or admin) token. A developer session also carries a
// refresh token, which gets a new token when this one has expired (RefreshSession); an admin
// session has none, and RefreshToken is empty.
type Session struct {
	Token     string
	ExpiresIn int // seconds
	// RefreshToken is for the dashboard's HttpOnly cookie; it is never part of a JSON answer.
	RefreshToken     string
	RefreshExpiresIn int // seconds
}

func errUnauthorized() *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, "unauthorized", "A valid developer token is required")
}

func errInvalidCredentials() *httpx.Error {
	return httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "Email or password is wrong")
}

// Register validates the input, hashes the password and creates the developer.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Developer, error) {
	email := normalizeEmail(in.Email)
	name := strings.TrimSpace(in.Name)
	if err := validateEmail(email); err != nil {
		return Developer{}, err
	}
	if err := validatePassword(in.Password); err != nil {
		return Developer{}, err
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxNameLen {
		return Developer{}, httpx.NewError(http.StatusBadRequest, "invalid_name",
			fmt.Sprintf("Name must be 1 to %d characters", maxNameLen))
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return Developer{}, fmt.Errorf("hash password: %w", err)
	}
	d, err := s.store.CreateDeveloper(ctx, email, string(hash), name)
	if errors.Is(err, repository.ErrEmailTaken) {
		return Developer{}, httpx.NewError(http.StatusConflict, "email_taken", "An account with this email already exists")
	}
	if err != nil {
		return Developer{}, fmt.Errorf("create developer: %w", err)
	}
	return toDeveloper(d), nil
}

// Login checks the email and password and returns a developer token, and the refresh token of the
// session this login starts.
// An unknown email and a wrong password give the same error.
func (s *Service) Login(ctx context.Context, in LoginInput) (Session, error) {
	d, err := s.store.DeveloperByEmail(ctx, normalizeEmail(in.Email))
	if errors.Is(err, repository.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(in.Password)) // spend the same time; result is irrelevant
		return Session{}, errInvalidCredentials()
	}
	if err != nil {
		return Session{}, fmt.Errorf("find developer: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(d.PasswordHash), []byte(in.Password)) != nil {
		return Session{}, errInvalidCredentials()
	}

	token, err := jwt.Sign(s.key, jwt.Claims{Subject: d.ID, Name: d.Name, Type: jwt.TypeDeveloper}, s.tokenTTL, s.now())
	if err != nil {
		return Session{}, fmt.Errorf("sign developer token: %w", err)
	}
	refreshToken, refreshExpiresIn, err := s.startRefreshSession(ctx, d.ID)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Token: token, ExpiresIn: int(s.tokenTTL.Seconds()),
		RefreshToken: refreshToken, RefreshExpiresIn: refreshExpiresIn,
	}, nil
}

// AuthenticateDeveloper checks a developer token (signature, expiry and type "developer")
// and returns the developer ID inside it. Player tokens are rejected.
//
// It also reads the developer: a token of an account that is gone is refused, and so is a token
// issued before the password was last reset, so a reset logs out whoever knew the old password.
// A token's issue time has whole seconds, so one issued in the very second of the reset still works.
func (s *Service) AuthenticateDeveloper(ctx context.Context, token string) (string, error) {
	c, err := jwt.Verify(s.pub, token, jwt.TypeDeveloper, s.now())
	if err != nil {
		return "", errUnauthorized()
	}
	d, err := s.store.DeveloperByID(ctx, c.Subject)
	if errors.Is(err, repository.ErrNotFound) {
		return "", errUnauthorized()
	}
	if err != nil {
		return "", fmt.Errorf("find developer %s: %w", c.Subject, err)
	}
	if d.PasswordChangedAt != nil && c.IssuedAt < d.PasswordChangedAt.Unix() {
		return "", errUnauthorized()
	}
	return d.ID, nil
}

// Me returns the developer with the given ID (taken from a verified token).
func (s *Service) Me(ctx context.Context, id string) (Developer, error) {
	d, err := s.store.DeveloperByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return Developer{}, errUnauthorized() // the account behind a valid token is gone
	}
	if err != nil {
		return Developer{}, fmt.Errorf("find developer %s: %w", id, err)
	}
	return toDeveloper(d), nil
}

func toDeveloper(d repository.Developer) Developer {
	return Developer{
		ID: d.ID, Email: d.Email, Name: d.Name, CreatedAt: d.CreatedAt,
		Status: d.Status, StatusReason: d.StatusReason,
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateEmail accepts a plain address like dev@example.com: no display name,
// and a domain with at least one dot.
func validateEmail(email string) error {
	bad := httpx.NewError(http.StatusBadRequest, "invalid_email", "Email address is not valid")
	if email == "" || len(email) > maxEmailLen {
		return bad
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return bad
	}
	at := strings.LastIndex(email, "@")
	if !strings.Contains(email[at+1:], ".") {
		return bad
	}
	return nil
}

func validatePassword(p string) error {
	if len(p) < minPasswordLen || len(p) > maxPasswordLen {
		return httpx.NewError(http.StatusBadRequest, "invalid_password",
			fmt.Sprintf("Password must be %d to %d characters", minPasswordLen, maxPasswordLen))
	}
	return nil
}

package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// resetTokenTTL is how long a password reset link works.
	resetTokenTTL = 30 * time.Minute
	// opaqueTokenBytes is the size of a random token (reset link, refresh token): 256 bits, not guessable.
	opaqueTokenBytes = 32
	// maxOpaqueTokenLen bounds what is hashed and looked up; a real token is 43 characters.
	maxOpaqueTokenLen = 128
	// resetPath is the dashboard page that the link opens. The token is in the fragment (after #),
	// which browsers never send to a server, so it stays out of access logs and Referer headers.
	resetPath = "/reset-password#token="
)

// ResetSender delivers a password reset link to a developer. SendPasswordReset must not block:
// the request that asked for the link is answered at once, whether or not an email goes out.
type ResetSender interface {
	SendPasswordReset(to, name, link string)
}

// noResetSender is used until WithPasswordReset is called: links are created but go nowhere.
type noResetSender struct{}

func (noResetSender) SendPasswordReset(string, string, string) {}

// ResetPasswordInput is what the reset page sends: the token from the link and the new password.
type ResetPasswordInput struct {
	Token    string
	Password string
}

// WithPasswordReset sets who delivers the reset links and the address of the dashboard they point
// to (for example https://studio.example.com). It returns s, so it can follow New.
func (s *Service) WithPasswordReset(sender ResetSender, dashboardURL string) *Service {
	s.resetSender = sender
	s.dashboardURL = strings.TrimRight(dashboardURL, "/")
	return s
}

func errInvalidResetToken() *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, "invalid_reset_token",
		"This reset link is not valid or has expired. Ask for a new one.")
}

// RequestPasswordReset creates a reset link for the developer with that email and hands it to the
// ResetSender. For an email nobody registered it does nothing and still returns nil: the caller
// answers the same either way, so the endpoint cannot be used to find out who has an account.
//
// A new link replaces the developer's earlier ones. A suspended developer can reset the password
// too: they are allowed to log in.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	d, err := s.store.DeveloperByEmail(ctx, email)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find developer: %w", err)
	}

	token, err := newOpaqueToken()
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.store.CreatePasswordResetToken(ctx, d.ID, hashOpaqueToken(token), now.Add(resetTokenTTL), now); err != nil {
		return fmt.Errorf("store password reset token: %w", err)
	}
	s.resetSender.SendPasswordReset(d.Email, d.Name, s.dashboardURL+resetPath+token)
	return nil
}

// ResetPassword sets a new password with the token from a reset link. The token works once and
// for resetTokenTTL; afterwards, and for anything else, the error is invalid_reset_token.
// The new password follows the rules of registration, and a password that breaks them does not
// use the token up. After a reset the developer's earlier logins stop working
// (see AuthenticateDeveloper).
func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput) error {
	if err := validatePassword(in.Password); err != nil {
		return err
	}
	token := strings.TrimSpace(in.Token)
	if token == "" || len(token) > maxOpaqueTokenLen {
		return errInvalidResetToken()
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	_, err = s.store.ResetPassword(ctx, hashOpaqueToken(token), string(hash), s.now())
	if errors.Is(err, repository.ErrNotFound) {
		return errInvalidResetToken()
	}
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// newOpaqueToken returns a random token of 256 bits that is safe to put in a URL or a cookie.
// Password reset links and refresh tokens are made with it.
func newOpaqueToken() (string, error) {
	b := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashOpaqueToken is what the database stores instead of the token. A plain SHA-256 is enough:
// the token is 256 random bits, so there is nothing to guess, unlike with a password.
func hashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

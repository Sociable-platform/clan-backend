package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// verificationCodeDigits is the length of the code emailed to a new developer.
	verificationCodeDigits = 6
	// verificationCodeTTL is how long a code works.
	verificationCodeTTL = 15 * time.Minute
	// maxVerificationAttempts is how many wrong codes cancel a registration. A code has a million
	// values, so with this limit guessing it works once in 200,000 registrations.
	maxVerificationAttempts = 5
	// verificationResendInterval is the shortest time between two emails for one registration.
	verificationResendInterval = time.Minute
)

// VerificationSender delivers the emails of a registration with email verification. Neither
// method may block: the request is answered at once, whether or not an email goes out.
type VerificationSender interface {
	// SendVerificationCode emails the code that confirms the address.
	SendVerificationCode(to, name, code string)
	// SendAlreadyRegistered tells the owner of an existing account that someone tried to
	// register with its address. The request that caused it was answered like any other.
	SendAlreadyRegistered(to, name string)
}

// VerifyRegistrationInput is what the "enter the code" page sends.
type VerifyRegistrationInput struct {
	Email string
	Code  string
}

// WithEmailVerification switches email verification on: registering then goes through
// RequestRegistration and VerifyRegistration, and sender delivers the code. Without it, Register
// creates the account at once. It returns s, so it can follow New.
func (s *Service) WithEmailVerification(sender VerificationSender) *Service {
	s.verification = sender
	return s
}

// EmailVerificationOn reports whether a new developer must confirm their email address with a
// code before the account is created.
func (s *Service) EmailVerificationOn() bool { return s.verification != nil }

func errInvalidVerificationCode() *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, "invalid_verification_code", "The code is wrong or has expired.")
}

// RequestRegistration is the first half of registering with email verification. It checks the
// input like Register, stores the registration as pending and emails a code to the address. No
// developer exists until VerifyRegistration gets that code.
//
// For an address that already has an account it stores nothing, tells that account's owner by
// email, and returns nil as well: the caller answers the same either way, so registering cannot
// be used to find out who has an account. Registering again with an address that is still
// pending replaces the earlier registration and its code.
func (s *Service) RequestRegistration(ctx context.Context, in RegisterInput) error {
	if s.verification == nil {
		return errors.New("email verification is not switched on")
	}
	email, name, err := validateRegistration(in)
	if err != nil {
		return err
	}
	// Hashed before the lookup, so a known and an unknown address take about the same time.
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	existing, err := s.store.DeveloperByEmail(ctx, email)
	if err == nil {
		s.verification.SendAlreadyRegistered(existing.Email, existing.Name)
		return nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("find developer: %w", err)
	}

	code, err := newVerificationCode()
	if err != nil {
		return err
	}
	now := s.now()
	p := repository.PendingRegistration{
		Email: email, Name: name, PasswordHash: string(hash), CodeHash: hashVerificationCode(email, code),
		ExpiresAt: now.Add(verificationCodeTTL), LastSentAt: now,
	}
	if err := s.store.SavePendingRegistration(ctx, p, now); err != nil {
		return fmt.Errorf("store pending registration: %w", err)
	}
	s.verification.SendVerificationCode(email, name, code)
	return nil
}

// VerifyRegistration is the second half: with the right code it creates the developer from the
// pending registration. A wrong code, an expired one and an address nobody registered all give
// invalid_verification_code. The wrong code that reaches maxVerificationAttempts cancels the
// registration (verification_attempts_exceeded): the developer registers again for a new code.
func (s *Service) VerifyRegistration(ctx context.Context, in VerifyRegistrationInput) (Developer, error) {
	email := normalizeEmail(in.Email)
	code := strings.TrimSpace(in.Code)
	if validateEmail(email) != nil || !isVerificationCode(code) {
		return Developer{}, errInvalidVerificationCode()
	}
	d, err := s.store.ConfirmPendingRegistration(ctx, email, hashVerificationCode(email, code), s.now(), maxVerificationAttempts)
	switch {
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrWrongCode):
		return Developer{}, errInvalidVerificationCode()
	case errors.Is(err, repository.ErrTooManyAttempts):
		return Developer{}, httpx.NewError(http.StatusBadRequest, "verification_attempts_exceeded",
			"Too many wrong codes. Register again to get a new one.")
	case errors.Is(err, repository.ErrEmailTaken):
		return Developer{}, httpx.NewError(http.StatusConflict, "email_taken", "An account with this email already exists")
	case err != nil:
		return Developer{}, fmt.Errorf("confirm pending registration: %w", err)
	}
	return toDeveloper(d), nil
}

// ResendRegistrationCode emails a new code for a pending registration; the earlier code stops
// working and the count of wrong attempts starts again. For an address without a pending
// registration it does nothing and returns nil, like RequestRegistration. Asking again within
// verificationResendInterval is refused with rate_limited.
func (s *Service) ResendRegistrationCode(ctx context.Context, email string) error {
	if s.verification == nil {
		return errors.New("email verification is not switched on")
	}
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	now := s.now()
	p, err := s.store.PendingRegistrationByEmail(ctx, email, now)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find pending registration: %w", err)
	}
	if now.Sub(p.LastSentAt) < verificationResendInterval {
		return httpx.NewError(http.StatusTooManyRequests, "rate_limited", "A code was just sent. Wait a minute before asking for a new one.")
	}

	code, err := newVerificationCode()
	if err != nil {
		return err
	}
	err = s.store.ReplacePendingRegistrationCode(ctx, email, hashVerificationCode(email, code), now.Add(verificationCodeTTL), now)
	if errors.Is(err, repository.ErrNotFound) {
		return nil // it expired or was confirmed in between
	}
	if err != nil {
		return fmt.Errorf("replace verification code: %w", err)
	}
	s.verification.SendVerificationCode(email, p.Name, code)
	return nil
}

// newVerificationCode returns a random code of verificationCodeDigits digits, leading zeros
// included.
func newVerificationCode() (string, error) {
	limit := big.NewInt(1)
	for i := 0; i < verificationCodeDigits; i++ {
		limit.Mul(limit, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("generate verification code: %w", err)
	}
	return fmt.Sprintf("%0*d", verificationCodeDigits, n.Int64()), nil
}

// isVerificationCode reports whether s has the shape of a code: exactly the digits.
func isVerificationCode(s string) bool {
	if len(s) != verificationCodeDigits {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// hashVerificationCode is what the database stores instead of the code. The email is part of it,
// so the same code gives a different hash for every registration. The hash does not protect a
// 6-digit code from someone who can read the table (a million tries find it); what protects the
// code is the limit on wrong attempts and its short life.
func hashVerificationCode(email, code string) string {
	sum := sha256.Sum256([]byte(email + ":" + code))
	return hex.EncodeToString(sum[:])
}

package repository

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrWrongCode means the verification code did not match; the attempt was counted.
	ErrWrongCode = errors.New("wrong verification code")
	// ErrTooManyAttempts means the code was wrong once too often: the pending registration has
	// been deleted, and the developer has to register again.
	ErrTooManyAttempts = errors.New("too many wrong verification codes")
)

// PendingRegistration is a row of the pending_registrations table: a registration that waits for
// its email address to be confirmed with a code. There is no developer for it yet.
type PendingRegistration struct {
	Email        string
	Name         string
	PasswordHash string
	// CodeHash is what the service stores instead of the code.
	CodeHash string
	// Attempts counts the wrong codes so far.
	Attempts  int
	ExpiresAt time.Time
	// LastSentAt is when the code was last emailed.
	LastSentAt time.Time
}

// SavePendingRegistration stores p, replacing an earlier pending registration of the same email
// address (and with it the earlier code and its count of wrong attempts). Rows of anyone that
// expired before now are removed on the way, which keeps the table small.
func (r *Repository) SavePendingRegistration(ctx context.Context, p PendingRegistration, now time.Time) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	return r.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.db(ctx).Exec(ctx,
			`delete from pending_registrations where expires_at <= $1`, now); err != nil {
			return fmt.Errorf("delete expired pending registrations: %w", err)
		}
		if _, err := r.db(ctx).Exec(ctx,
			`insert into pending_registrations (email, name, password_hash, code_hash, attempts, expires_at, last_sent_at, created_at)
			 values ($1, $2, $3, $4, 0, $5, $6, $6)
			 on conflict (email) do update set
			   name = excluded.name, password_hash = excluded.password_hash, code_hash = excluded.code_hash,
			   attempts = 0, expires_at = excluded.expires_at, last_sent_at = excluded.last_sent_at,
			   created_at = excluded.created_at`,
			p.Email, p.Name, p.PasswordHash, p.CodeHash, p.ExpiresAt, p.LastSentAt); err != nil {
			return fmt.Errorf("upsert pending registration: %w", err)
		}
		return nil
	})
}

// PendingRegistrationByEmail returns the pending registration of that (already lowercased) email
// address, or ErrNotFound when there is none or it expired before now.
func (r *Repository) PendingRegistrationByEmail(ctx context.Context, email string, now time.Time) (PendingRegistration, error) {
	if r.pool == nil {
		return PendingRegistration{}, ErrNoDatabase
	}
	p := PendingRegistration{Email: email}
	err := r.db(ctx).QueryRow(ctx,
		`select name, password_hash, code_hash, attempts, expires_at, last_sent_at
		 from pending_registrations where email = $1 and expires_at > $2`,
		email, now).Scan(&p.Name, &p.PasswordHash, &p.CodeHash, &p.Attempts, &p.ExpiresAt, &p.LastSentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingRegistration{}, ErrNotFound
	}
	if err != nil {
		return PendingRegistration{}, fmt.Errorf("select pending registration: %w", err)
	}
	return p, nil
}

// ReplacePendingRegistrationCode gives the pending registration of that email address a new code:
// the wrong attempts start at zero again, the code runs until expiresAt, and now is recorded as
// the time it was sent. ErrNotFound when there is no pending registration that is still valid.
func (r *Repository) ReplacePendingRegistrationCode(ctx context.Context, email, codeHash string, expiresAt, now time.Time) error {
	if r.pool == nil {
		return ErrNoDatabase
	}
	tag, err := r.db(ctx).Exec(ctx,
		`update pending_registrations
		 set code_hash = $2, attempts = 0, expires_at = $3, last_sent_at = $4
		 where email = $1 and expires_at > $4`,
		email, codeHash, expiresAt, now)
	if err != nil {
		return fmt.Errorf("replace pending registration code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ConfirmPendingRegistration checks a verification code and, when it is right, turns the pending
// registration of that email address into a developer, in one transaction.
//
//   - No pending registration, or it expired before now: ErrNotFound.
//   - codeHash matches: the developer is created from the row, the row is deleted, and the
//     developer is returned. ErrEmailTaken when an account with that address appeared meanwhile.
//   - codeHash does not match: the attempt is counted and the result is ErrWrongCode; the attempt
//     that reaches maxAttempts deletes the row and gives ErrTooManyAttempts instead.
//
// The row is locked while this runs, so two requests cannot both use the same attempt.
func (r *Repository) ConfirmPendingRegistration(ctx context.Context, email, codeHash string, now time.Time, maxAttempts int) (Developer, error) {
	if r.pool == nil {
		return Developer{}, ErrNoDatabase
	}
	var (
		d       Developer
		refused error // a counted wrong attempt: returned after the commit, so the count is kept
	)
	err := r.InTx(ctx, func(ctx context.Context) error {
		var (
			name, passwordHash, storedHash string
			attempts                       int
		)
		err := r.db(ctx).QueryRow(ctx,
			`select name, password_hash, code_hash, attempts
			 from pending_registrations where email = $1 and expires_at > $2 for update`,
			email, now).Scan(&name, &passwordHash, &storedHash, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("select pending registration: %w", err)
		}

		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
			if attempts+1 >= maxAttempts {
				if _, err := r.db(ctx).Exec(ctx, `delete from pending_registrations where email = $1`, email); err != nil {
					return fmt.Errorf("delete pending registration: %w", err)
				}
				refused = ErrTooManyAttempts
				return nil
			}
			if _, err := r.db(ctx).Exec(ctx,
				`update pending_registrations set attempts = attempts + 1 where email = $1`, email); err != nil {
				return fmt.Errorf("count wrong verification code: %w", err)
			}
			refused = ErrWrongCode
			return nil
		}

		d = Developer{Email: email, PasswordHash: passwordHash, Name: name}
		err = r.db(ctx).QueryRow(ctx,
			`insert into developers (id, email, password_hash, name)
			 values (gen_random_uuid(), $1, $2, $3)
			 returning id::text, created_at, status`,
			email, passwordHash, name,
		).Scan(&d.ID, &d.CreatedAt, &d.Status)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return ErrEmailTaken
			}
			return fmt.Errorf("insert developer: %w", err)
		}
		if _, err := r.db(ctx).Exec(ctx, `delete from pending_registrations where email = $1`, email); err != nil {
			return fmt.Errorf("delete pending registration: %w", err)
		}
		return nil
	})
	if err != nil {
		return Developer{}, err
	}
	if refused != nil {
		return Developer{}, refused
	}
	return d, nil
}

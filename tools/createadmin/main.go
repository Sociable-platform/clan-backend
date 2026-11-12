// Command createadmin creates an admin account for the admin panel.
//
// There is no register endpoint for admins on purpose: whoever can reach the platform database
// creates the first one with this tool, like tools/devtoken makes test tokens.
//
//	export PLATFORM_DATABASE_URL=postgres://platform_user:...@localhost:5432/clan
//	go run ./tools/createadmin -email ops@example.com -name "Ops One"
//
// The password is read from the CREATEADMIN_PASSWORD environment variable, or from the first line
// of standard input when that is not set, so it never lands in the shell history or the process list.
// It is stored as a bcrypt hash. The tool writes only to the platform schema's admins table.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

// Limits match what the platform service enforces for developer accounts.
const (
	maxEmailLen    = 254
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt rejects anything longer, in bytes
	maxNameLen     = 100

	uniqueViolation = "23505" // Postgres error code for a broken unique constraint
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "createadmin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("createadmin", flag.ContinueOnError)
	email := fs.String("email", "", "the admin's email address (the login)")
	name := fs.String("name", "", "the admin's display name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	password := getenv("CREATEADMIN_PASSWORD")
	if password == "" {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	}

	a, err := newAdmin(*email, *name, password, bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	url := getenv("PLATFORM_DATABASE_URL")
	if url == "" {
		return errors.New("PLATFORM_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err) // pgx does not put the password in this error
	}
	defer conn.Close(context.Background())

	id, err := insertAdmin(ctx, conn, a)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created admin %s (%s)\n", a.email, id)
	return nil
}

// admin is a validated account, ready to insert.
type admin struct {
	email        string // lowercase, like the service stores it
	name         string
	passwordHash string
}

// newAdmin validates the input and hashes the password.
func newAdmin(email, name, password string, cost int) (admin, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if err := validateEmail(email); err != nil {
		return admin{}, err
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxNameLen {
		return admin{}, fmt.Errorf("-name must be 1 to %d characters", maxNameLen)
	}
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return admin{}, fmt.Errorf("password must be %d to %d characters (set CREATEADMIN_PASSWORD or pipe it to stdin)", minPasswordLen, maxPasswordLen)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return admin{}, fmt.Errorf("hash password: %w", err)
	}
	return admin{email: email, name: name, passwordHash: string(hash)}, nil
}

// validateEmail accepts a plain address like ops@example.com: no display name, and a domain with a dot.
func validateEmail(email string) error {
	bad := errors.New("-email must be an address like ops@example.com")
	if email == "" || len(email) > maxEmailLen {
		return bad
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return bad
	}
	if !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return bad
	}
	return nil
}

// insertAdmin adds the admin and returns its generated ID.
func insertAdmin(ctx context.Context, conn *pgx.Conn, a admin) (string, error) {
	var id string
	err := conn.QueryRow(ctx,
		`insert into admins (id, email, password_hash, name)
		 values (gen_random_uuid(), $1, $2, $3)
		 returning id::text`,
		a.email, a.passwordHash, a.name,
	).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return "", fmt.Errorf("an admin with the email %s already exists", a.email)
	}
	if err != nil {
		return "", fmt.Errorf("insert admin: %w", err)
	}
	return id, nil
}

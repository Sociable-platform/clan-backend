package main

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNewAdmin(t *testing.T) {
	a, err := newAdmin("  Ops@Example.COM ", " Ops One ", "correct horse", bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if a.email != "ops@example.com" || a.name != "Ops One" {
		t.Fatalf("not normalized: %+v", a)
	}
	if a.passwordHash == "correct horse" || bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte("correct horse")) != nil {
		t.Fatal("password is not stored as a bcrypt hash of the input")
	}
}

func TestNewAdmin_Invalid(t *testing.T) {
	tests := []struct {
		name, email, adminName, password string
	}{
		{"no email", "", "Ops", "correct horse"},
		{"not an address", "ops", "Ops", "correct horse"},
		{"display name in address", "Ops <ops@example.com>", "Ops", "correct horse"},
		{"domain without a dot", "ops@localhost", "Ops", "correct horse"},
		{"no name", "ops@example.com", "  ", "correct horse"},
		{"name too long", "ops@example.com", strings.Repeat("a", 101), "correct horse"},
		{"password too short", "ops@example.com", "Ops", "short"},
		{"no password", "ops@example.com", "Ops", ""},
		{"password over the bcrypt limit", "ops@example.com", "Ops", strings.Repeat("a", 73)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newAdmin(tt.email, tt.adminName, tt.password, bcrypt.MinCost); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// run must fail on bad input before it touches the database, and read the password from stdin
// when the environment variable is not set.
func TestRun_FailsBeforeConnecting(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	t.Run("missing email", func(t *testing.T) {
		err := run(context.Background(), []string{"-name", "Ops"}, strings.NewReader("correct horse\n"), &strings.Builder{}, env(nil))
		if err == nil || !strings.Contains(err.Error(), "-email") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("password from stdin is checked", func(t *testing.T) {
		err := run(context.Background(), []string{"-email", "ops@example.com", "-name", "Ops"}, strings.NewReader("short\n"), &strings.Builder{}, env(nil))
		if err == nil || !strings.Contains(err.Error(), "password") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no database url", func(t *testing.T) {
		err := run(context.Background(), []string{"-email", "ops@example.com", "-name", "Ops"},
			strings.NewReader(""), &strings.Builder{}, env(map[string]string{"CREATEADMIN_PASSWORD": "correct horse"}))
		if err == nil || !strings.Contains(err.Error(), "PLATFORM_DATABASE_URL") {
			t.Fatalf("got %v", err)
		}
	})
}

// Package dbmigrate applies a service's SQL migrations when the service starts,
// using golang-migrate. Applied versions are recorded in a schema_migrations
// table inside the service's own schema.
package dbmigrate

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	gomigrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5://" driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Up applies every migration in files that has not run yet and returns the current version.
// files holds NNNNNN_name.up.sql / .down.sql files at its root.
func Up(files fs.FS, databaseURL string) (uint, error) {
	src, err := iofs.New(files, ".")
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: read migration files: %w", err)
	}
	m, err := gomigrate.NewWithSourceInstance("iofs", src, pgx5URL(databaseURL))
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: connect: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
		return 0, fmt.Errorf("dbmigrate: apply: %w", err)
	}
	version, dirty, err := m.Version()
	if errors.Is(err, gomigrate.ErrNilVersion) {
		return 0, nil // no migrations yet
	}
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: read version: %w", err)
	}
	if dirty {
		return version, fmt.Errorf("dbmigrate: version %d is dirty: a migration failed halfway", version)
	}
	return version, nil
}

// pgx5URL turns postgres://... into pgx5://..., the scheme golang-migrate's pgx v5 driver uses.
func pgx5URL(u string) string {
	for _, p := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(u, p) {
			return "pgx5://" + strings.TrimPrefix(u, p)
		}
	}
	return u
}

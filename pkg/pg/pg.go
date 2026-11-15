// Package pg opens the shared PostgreSQL connection pool.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
)

// Connect opens a pool for url (for example postgres://user:pass@localhost:5432/clan)
// and checks it with a ping. Each service connects with its own database user,
// whose search_path should point at that service's schema.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pg: invalid database URL: %w", err)
	}
	cfg.MaxConns = 10
	// Queries run for a traced request show up as spans under it (trace.go).
	cfg.ConnConfig.Tracer = queryTracer{tracer: otel.Tracer(tracerName)}
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: open pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: database not reachable: %w", err)
	}
	return pool, nil
}

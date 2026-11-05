// Command social runs the social service: players, clans and clan members.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/config"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

const serviceName = "social"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, serviceName+":", err)
		os.Exit(1)
	}
}

func run() error {
	port, err := config.Int("SOCIAL_PORT", 8082)
	if err != nil {
		return err
	}
	log := logx.New(serviceName, config.String("LOG_LEVEL", "info"))

	// Stop cleanly on Ctrl+C or when Docker sends SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if url := config.String("SOCIAL_DATABASE_URL", ""); url != "" {
		pool, err = pg.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		log.Info("connected to database")
	} else {
		log.Warn("SOCIAL_DATABASE_URL is not set: starting without a database, /ready will report not ready")
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc := service.New(repo)

	mux := http.NewServeMux()
	handler.New(serviceName, svc).Register(mux)

	h := httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log))
	return httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
}

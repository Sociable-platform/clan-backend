// Command platform runs the platform service: developers, registered games, API keys and player tokens.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/config"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const serviceName = "platform"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, serviceName+":", err)
		os.Exit(1)
	}
}

func run() error {
	port, err := config.Int("PLATFORM_PORT", 8081)
	if err != nil {
		return err
	}
	tokenTTL, err := config.Duration("PLATFORM_DEVELOPER_TOKEN_TTL", 24*time.Hour)
	if err != nil {
		return err
	}
	log := logx.New(serviceName, config.String("LOG_LEVEL", "info"))

	// Platform is the only service that holds the private key; it signs developer
	// and player tokens with it.
	keyFile := config.String("PLATFORM_JWT_PRIVATE_KEY_FILE", "")
	if keyFile == "" {
		return errors.New("PLATFORM_JWT_PRIVATE_KEY_FILE is not set (see README: JWT signing key)")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read JWT private key: %w", err)
	}
	signingKey, err := jwt.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return err
	}

	// Stop cleanly on Ctrl+C or when Docker sends SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if url := config.String("PLATFORM_DATABASE_URL", ""); url != "" {
		pool, err = pg.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		log.Info("connected to database")
	} else {
		log.Warn("PLATFORM_DATABASE_URL is not set: starting without a database, /ready will report not ready")
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc, err := service.New(repo, signingKey, tokenTTL)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	handler.New(serviceName, svc, log).Register(mux)

	// Browser pages allowed to call this service (the developer dashboard).
	origins := config.List("PLATFORM_CORS_ORIGINS", []string{"http://localhost:5173"})
	cors, err := httpx.CORS(origins, "/api/v1/public/")
	if err != nil {
		return err
	}
	log.Info("cors", "allowed_origins", origins)

	h := httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors)
	return httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
}

// Command realtime runs the realtime service: WebSockets, chat, presence and live updates.
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
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

const serviceName = "realtime"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, serviceName+":", err)
		os.Exit(1)
	}
}

func run() error {
	port, err := config.Int("REALTIME_PORT", 8083)
	if err != nil {
		return err
	}
	log := logx.New(serviceName, config.String("LOG_LEVEL", "info"))

	// Realtime holds only platform's PUBLIC key: it can check player tokens, never create them.
	// The key is loaded now so a missing or broken file stops the service at startup; the token
	// check itself is wired in together with the first endpoint that needs it.
	keyFile := config.String("REALTIME_JWT_PUBLIC_KEY_FILE", "")
	if keyFile == "" {
		return fmt.Errorf("REALTIME_JWT_PUBLIC_KEY_FILE is not set (see README: JWT signing key)")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read JWT public key: %w", err)
	}
	if _, err := jwt.ParsePublicKeyPEM(keyPEM); err != nil {
		return err
	}
	log.Info("loaded JWT public key", "file", keyFile)

	// Stop cleanly on Ctrl+C or when Docker sends SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if url := config.String("REALTIME_DATABASE_URL", ""); url != "" {
		pool, err = pg.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		log.Info("connected to database")
	} else {
		log.Warn("REALTIME_DATABASE_URL is not set: starting without a database, /ready will report not ready")
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc := service.New(repo)

	mux := http.NewServeMux()
	handler.New(serviceName, svc, log).Register(mux)

	// Browser pages allowed to call this service (the demo game, where the SDK runs).
	origins := config.List("REALTIME_CORS_ORIGINS", []string{"http://localhost:5174"})
	cors, err := httpx.CORS(origins)
	if err != nil {
		return err
	}
	log.Info("cors", "allowed_origins", origins)

	h := httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors)
	return httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
}

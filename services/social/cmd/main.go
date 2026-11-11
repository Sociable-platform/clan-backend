// Command social runs the social service: players, clans and clan members.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/clan-platform/clan-backend/pkg/config"
	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

const serviceName = "social"

// minInternalTokenLen keeps the shared secret of the /internal endpoints from being guessable.
const minInternalTokenLen = 32

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

	// Social holds only platform's PUBLIC key: it can check player tokens, never create them.
	keyFile := config.String("SOCIAL_JWT_PUBLIC_KEY_FILE", "")
	if keyFile == "" {
		return fmt.Errorf("SOCIAL_JWT_PUBLIC_KEY_FILE is not set (see README: JWT signing key)")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read JWT public key: %w", err)
	}
	verifyKey, err := jwt.ParsePublicKeyPEM(keyPEM)
	if err != nil {
		return err
	}
	log.Info("loaded JWT public key", "file", keyFile)

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

	// Events for the other services (see README "RabbitMQ events"). Without a broker URL they are
	// dropped. The publisher connects on its first event, so a broker that is down never stops
	// social from starting, and events.Safe makes sure it never fails a request either.
	var publisher events.Publisher = events.Noop{}
	if url := config.String("SOCIAL_RABBITMQ_URL", ""); url != "" {
		rabbit := events.NewRabbitMQ(url, log)
		defer rabbit.Close()
		publisher = rabbit
		log.Info("publishing events to rabbitmq", "exchange", events.Exchange) // never log the URL: it holds the password
	} else {
		log.Warn("SOCIAL_RABBITMQ_URL is not set: events for other services are dropped")
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc := service.New(repo).WithPublisher(events.Safe(publisher, log))

	// The /internal routes (for realtime's backfill) need a shared secret. Without one they answer 404.
	internalToken := config.String("SOCIAL_INTERNAL_TOKEN", "")
	switch {
	case internalToken == "":
		log.Warn("SOCIAL_INTERNAL_TOKEN is not set: the /internal endpoints are switched off")
	case len(internalToken) < minInternalTokenLen:
		return fmt.Errorf("SOCIAL_INTERNAL_TOKEN must be at least %d characters (try: openssl rand -hex 32)", minInternalTokenLen)
	default:
		log.Info("internal endpoints enabled") // never log the token itself
	}

	mux := http.NewServeMux()
	// Every /api/v1 route requires a player token signed by platform (checked with verifyKey).
	handler.New(serviceName, svc, auth.RequirePlayer(verifyKey, time.Now), log).
		WithInternal(auth.RequireInternal(internalToken)).
		Register(mux)

	// Browser pages allowed to call this service (the demo game, where the SDK runs).
	origins := config.List("SOCIAL_CORS_ORIGINS", []string{"http://localhost:5174"})
	cors, err := httpx.CORS(origins)
	if err != nil {
		return err
	}
	log.Info("cors", "allowed_origins", origins)

	h := httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors)
	return httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
}

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
	"gitlab.com/clan-platform/clan-backend/pkg/events"
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/otelx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const serviceName = "platform"

// minInternalTokenLen keeps the shared secret of the /internal endpoints from being guessable.
const minInternalTokenLen = 32

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
	adminTokenTTL, err := config.Duration("PLATFORM_ADMIN_TOKEN_TTL", time.Hour)
	if err != nil {
		return err
	}
	log := logx.New(serviceName, config.String("LOG_LEVEL", "info"))

	// Platform is the only service that holds the private key; it signs developer,
	// player and admin tokens with it.
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

	// Tracing and metrics (pkg/otelx), on only when OTEL_EXPORTER_OTLP_ENDPOINT is set. Deferred this
	// early, the flush runs last: after the server and everything below has stopped.
	defer otelx.Setup(ctx, serviceName, log)()

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

	// Events for the other services (config.published for Realtime's live re-skin, game.suspended and
	// game.resumed), sent by the outbox relay below. The broker is reached lazily, so a broker that is
	// down does not stop platform from starting, and requests never wait for it.
	var publisher events.Publisher = events.Noop{}
	if url := config.String("PLATFORM_RABBITMQ_URL", ""); url != "" {
		rabbit := events.NewRabbitMQ(url, log)
		defer rabbit.Close()
		publisher = rabbit
		log.Info("publishing events to rabbitmq", "exchange", events.Exchange) // never log the URL: it holds the password
	} else {
		log.Warn("PLATFORM_RABBITMQ_URL is not set: events for other services are dropped")
	}

	// The outbox relay publishes the events written to the outbox table, in the order they were
	// written, and retries until the broker confirms them (README "Event outbox"). It gets the
	// publisher, so it sees a failed send and keeps the row. The service writes every event to the
	// outbox in the transaction of its change; the relay is the publisher's only user.
	relayDone, err := startRelay(ctx, pool, publisher, "PLATFORM", log)
	if err != nil {
		return err
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc, err := service.New(repo, signingKey, tokenTTL)
	if err != nil {
		return err
	}
	svc.WithAdminTokenTTL(adminTokenTTL)

	// The /internal routes (for realtime) need a shared secret. Without one they answer 404.
	internalToken := config.String("PLATFORM_INTERNAL_TOKEN", "")
	switch {
	case internalToken == "":
		log.Warn("PLATFORM_INTERNAL_TOKEN is not set: the /internal endpoints are switched off")
	case len(internalToken) < minInternalTokenLen:
		return fmt.Errorf("PLATFORM_INTERNAL_TOKEN must be at least %d characters (try: openssl rand -hex 32)", minInternalTokenLen)
	default:
		log.Info("internal endpoints enabled") // never log the token itself
	}

	mux := http.NewServeMux()
	handler.New(serviceName, svc, log).WithInternal(httpx.RequireInternal(internalToken)).Register(mux)

	// Browser pages allowed to call this service (the developer dashboard).
	origins := config.List("PLATFORM_CORS_ORIGINS", []string{"http://localhost:5173"})
	cors, err := httpx.CORS(origins, "/api/v1/public/")
	if err != nil {
		return err
	}
	log.Info("cors", "allowed_origins", origins)

	h := httpx.Chain(mux, httpx.Trace(), httpx.Recover(log), httpx.RequestLog(log), cors)
	err = httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
	// The relay stops with ctx too, after recording the batch it was sending; wait for it before
	// the pool and the broker connection are closed.
	select {
	case <-relayDone:
	case <-time.After(relayStopWait):
		log.Warn("outbox relay did not stop in time")
	}
	return err
}

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
	"gitlab.com/clan-platform/clan-backend/pkg/otelx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/gameconfig"
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

	// Tracing and metrics (pkg/otelx), on only when OTEL_EXPORTER_OTLP_ENDPOINT is set. Deferred this
	// early, the flush runs last: after the server and everything below has stopped.
	defer otelx.Setup(ctx, serviceName, log)()

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

	// Events for the other services (see README "RabbitMQ events"), sent by the outbox relay below.
	// Without a broker URL they are dropped. The publisher connects on its first event, so a broker
	// that is down never stops social from starting, and requests never wait for it.
	var publisher events.Publisher = events.Noop{}
	if url := config.String("SOCIAL_RABBITMQ_URL", ""); url != "" {
		rabbit := events.NewRabbitMQ(url, log)
		defer rabbit.Close()
		publisher = rabbit
		log.Info("publishing events to rabbitmq", "exchange", events.Exchange) // never log the URL: it holds the password
	} else {
		log.Warn("SOCIAL_RABBITMQ_URL is not set: events for other services are dropped")
	}

	// The service writes every event to the outbox table in the transaction of its change; the
	// outbox relay publishes them, in the order they were written, and retries until the broker
	// confirms them (README "Event outbox"). It is the only user of the publisher, and must see a
	// failed send, so it keeps the row.
	relayDone, err := startRelay(ctx, pool, publisher, "SOCIAL", log)
	if err != nil {
		return err
	}

	// The clan rules each game set in Studio (clan.maxMembers), read from platform's published
	// config and kept for SOCIAL_CONFIG_CACHE_TTL. Without a platform URL every game gets the
	// default (10 members). Platform being down never fails a request: the last known rules,
	// or the default, are used.
	var rules gameconfig.Source = gameconfig.Fixed{}
	if url := config.String("SOCIAL_PLATFORM_URL", ""); url != "" {
		ttl, err := config.Duration("SOCIAL_CONFIG_CACHE_TTL", 30*time.Second)
		if err != nil {
			return err
		}
		client, err := gameconfig.NewClient(url, 3*time.Second, ttl, log)
		if err != nil {
			return err
		}
		rules = client
		log.Info("reading clan rules from platform", "url", url, "cache_ttl", ttl)
	} else {
		log.Warn("SOCIAL_PLATFORM_URL is not set: every game gets the default clan size", "max_members", gameconfig.DefaultMaxMembers)
	}

	// Wiring: repository (database) -> service (rules) -> handler (HTTP).
	repo := repository.New(pool)
	svc := service.New(repo).WithRules(rules)

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
	// After the token check, a game that an admin suspended is refused with 403 game_suspended.
	// The answer comes from the same platform call and cache as the clan rules, so it applies
	// (and lifts) within SOCIAL_CONFIG_CACHE_TTL. Without SOCIAL_PLATFORM_URL nothing is suspended.
	isSuspended := func(ctx context.Context, gameID string) bool { return rules.Rules(ctx, gameID).Suspended }
	handler.New(serviceName, svc, auth.RequirePlayer(verifyKey, time.Now), log).
		WithGameGate(auth.RequireServedGame(isSuspended)).
		WithInternal(httpx.RequireInternal(internalToken)).
		Register(mux)

	// Browser pages allowed to call this service (the demo game, where the SDK runs).
	origins := config.List("SOCIAL_CORS_ORIGINS", []string{"http://localhost:5174"})
	cors, err := httpx.CORS(origins)
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

// Command realtime runs the realtime service: WebSockets, chat, presence and live updates.
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
	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/pkg/logx"
	"gitlab.com/clan-platform/clan-backend/pkg/pg"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/backfill"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/consumer"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/gameconfig"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/handler"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/presence"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
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
	keyFile := config.String("REALTIME_JWT_PUBLIC_KEY_FILE", "")
	if keyFile == "" {
		return fmt.Errorf("REALTIME_JWT_PUBLIC_KEY_FILE is not set (see README: JWT signing key)")
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
	// The hub of open connections is shared: the WebSocket handler fills it, the event consumer sends to it.
	connections := hub.New()
	repo := repository.New(pool)
	// Platform owns each game's published config. Realtime keeps the newest version of each game in memory,
	// loads it from Platform's public endpoint the first time it is needed, and fetches it again on config.published.
	platformURL := config.String("REALTIME_PLATFORM_URL", "http://localhost:8081")
	configSource, err := gameconfig.NewHTTPSource(platformURL, 3*time.Second)
	if err != nil {
		return err
	}
	configCache := gameconfig.NewCache(configSource, gameconfig.Options{})
	log.Info("platform", "url", platformURL)

	// Presence adds the 10 s grace period to "has an open WebSocket", and tells friends about going offline.
	tracker := presence.New(connections, repo, connections, presence.Options{Log: log})
	defer tracker.Close()
	// The message rules (slow mode, length, filter, ...) come from each game's published config, kept by the cache above.
	svc := service.New(repo, service.WithPresence(tracker), service.WithRules(configCache))

	// Browser pages allowed to call this service and to open a WebSocket (the demo game, where the SDK runs).
	origins := config.List("REALTIME_CORS_ORIGINS", []string{"http://localhost:5174"})
	cors, err := httpx.CORS(origins)
	if err != nil {
		return err
	}
	log.Info("cors", "allowed_origins", origins)

	// Social tells us each player's own ID; the player's token is sent to it, and only to this address.
	socialURL := config.String("REALTIME_SOCIAL_URL", "http://localhost:8082")
	socialClient, err := social.NewClient(socialURL, 3*time.Second)
	if err != nil {
		return err
	}
	log.Info("social", "url", socialURL)

	hdl := handler.New(serviceName, svc, handler.WSOptions{
		Verifier: auth.NewVerifier(verifyKey, time.Now),
		Players:  socialClient,
		Config:   configCache,
		Presence: tracker,
		Hub:      connections,
		Origins:  origins,
		Shutdown: ctx, // on SIGTERM every open socket is closed with 1001
	}, log)
	mux := http.NewServeMux()
	hdl.Register(mux)

	// Backfill: friendships Social has but Realtime never heard about (made before Realtime ran, or whose event
	// was lost) are stored once, before the consumer starts, so events then arrive on top of a complete state.
	// It is off by default; switch it on for the first start after a gap, or after restoring a database.
	backfillOn, err := config.Bool("REALTIME_BACKFILL", false)
	if err != nil {
		return err
	}
	if backfillOn {
		token := config.String("REALTIME_INTERNAL_TOKEN", "")
		if token == "" {
			return fmt.Errorf("REALTIME_BACKFILL is true but REALTIME_INTERNAL_TOKEN is not set (it must equal SOCIAL_INTERNAL_TOKEN)")
		}
		socialClient.WithInternalToken(token)
		if pool == nil {
			log.Warn("REALTIME_BACKFILL is true but there is no database: skipping the backfill")
		} else {
			log.Info("backfill started")
			res, err := backfill.Run(ctx, socialClient, svc, backfill.Options{Log: log})
			if err != nil {
				// Chat keeps working with what is stored and with new events; a restart with the flag on tries again.
				log.Error("backfill failed, starting anyway", "pages", res.Pages, "synced", res.Synced, "skipped", res.Skipped, "error", err)
			} else {
				log.Info("backfill done", "pages", res.Pages, "friendships", res.Synced, "skipped", res.Skipped)
			}
		}
	}

	// Events from Social (friends, later clans) arrive through RabbitMQ. Without a broker URL, or while the
	// broker is down, the service still runs; it just learns nothing new until the broker is back.
	consumerDone := make(chan struct{})
	brokerURL := config.String("REALTIME_RABBITMQ_URL", "")
	switch {
	case brokerURL == "":
		log.Warn("REALTIME_RABBITMQ_URL is not set: not reading events, friendships will not reach realtime")
		close(consumerDone)
	case pool == nil:
		// Events are dropped after one retry, so reading them with nowhere to store them would lose them.
		log.Warn("not reading events: there is no database to store them in")
		close(consumerDone)
	default:
		cons, err := consumer.New(consumer.Config{URL: brokerURL}, consumer.NewDispatcher(svc, connections, log).WithConfig(configCache, connections), log)
		if err != nil {
			return fmt.Errorf("REALTIME_RABBITMQ_URL: %w", err)
		}
		go func() {
			defer close(consumerDone)
			cons.Run(ctx) // returns when ctx ends (SIGTERM)
		}()
	}

	h := httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors)
	err = httpx.Run(ctx, fmt.Sprintf(":%d", port), h, log)
	// Run does not wait for upgraded connections; give them time to send their close frame.
	hdl.Wait(5 * time.Second)
	// The consumer stops with ctx too; it finishes the event it is working on first.
	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		log.Warn("event consumer did not stop in time")
	}
	return err
}

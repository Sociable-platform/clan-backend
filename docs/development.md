# Development

How the code is laid out, the rules it follows, and how to check a change before it is merged. Back to the [README](../README.md).

## Layout

```
clan-backend/
├── go.mod                     # one Go module for the whole repo
├── docker-compose.yml         # postgres + rabbitmq + platform + social + realtime + traefik
├── deploy/postgres/init/      # creates the schemas and database users on first start
├── deploy/traefik/            # gateway: static config and the /realtime route
├── deploy/rabbitmq/           # definitions.json: exchanges, queues, bindings and dead-letter policies, applied by rabbitmq-setup
├── deploy/migrate/            # Dockerfile of the migration images (migrate/migrate + a service's migrations)
├── deploy/helm/clan/          # the Helm chart of the backend on the server (its own README)
├── deploy/k3s/                # the server's Traefik settings
├── deploy/deploy.sh           # what CI runs to deploy main: copies the chart to the server and runs helm there
├── openapi/                   # API contracts: platform.yaml, social.yaml, realtime.yaml, realtime-ws.md
├── docs/                      # overview.md: start here; running-locally.md: every part on your computer; operations.md: deploys, rollbacks, logs, the production database, admins; api-reference.md: where each API is described; events.md: how services publish and consume events; event-messages.md: every event message and the clan chat frames; realtime-flow.md: the flows as UML diagrams
├── tools/                     # devtoken: test tokens; createadmin: creates an admin account; e2e: end-to-end check of the realtime flow; dlq: list and replay dead events
├── .gitlab-ci.yml             # CI: build, vet and test with the race detector, then on main build and push the images and deploy
├── pkg/                       # shared code, used by all three services
│   ├── config/                # read settings from environment variables
│   ├── logx/                  # JSON logger (slog)
│   ├── otelx/                 # OpenTelemetry tracing and metrics setup, off unless an endpoint is set
│   ├── pg/                    # PostgreSQL connection pool (pgx)
│   ├── httpx/                 # error format, JSON helpers, middleware, CORS, server with graceful shutdown
│   ├── events/                # the event envelope, publishing to RabbitMQ and the outbox relay
│   ├── mailer/                # sending email over SMTP (password reset, registration codes)
│   ├── ratelimit/             # in-memory request limits (sign-in, register, password reset)
│   └── jwt/                   # sign (platform) and verify (social, realtime) tokens, Ed25519
└── services/
    ├── platform/
    │   ├── cmd/main.go        # starts the service and wires the layers
    │   ├── internal/
    │   │   ├── handler/       # HTTP: routes, reading requests, writing JSON
    │   │   ├── service/       # business rules, no HTTP and no SQL
    │   │   └── repository/    # SQL, platform schema only
    │   ├── migrations/        # SQL for tables in the platform schema
    │   └── Dockerfile
    ├── social/                # same structure
    └── realtime/              # same structure
```

A request flows `handler -> service -> repository`. Each layer only calls the one below it.

## Rules

1. **Routing uses the standard `net/http` ServeMux** (`mux.HandleFunc("GET /api/v1/clans/{clanID}", ...)`). No Chi.
2. **Shared code goes in `pkg/` only.** A service never imports another service's code. Go enforces this: packages under `services/platform/internal/` cannot be imported from `services/social`.
3. **Each service only touches its own schema.** If platform needs social data, it calls social's internal HTTP endpoints.
4. **Errors always use one shape:** `{ "error": { "code": "clan_full", "message": "..." } }` (see `pkg/httpx`).

## First-time setup

Needs Go 1.26 or newer.

```bash
# download dependencies and create go.sum (run once, and after adding a dependency)
go mod tidy
```

The repo is one Go module, but the three services are still separate programs with separate `main` packages, ports and database schemas.
If a service ever moves to its own repository, it gets its own `go.mod` then.

## Check

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...     # the same, with the race detector (needs gcc; CI runs this one)
```

CI (`.gitlab-ci.yml`) runs `go build`, `go vet` and `go test -race` on `main`, before the images are built and deployed. On a merge request the job waits for the play button, and it does not run on `develop` (to save runner minutes), so run these on your machine before merging.
It needs no database or broker: the unit tests use fakes, and the tests that need a real database or RabbitMQ skip themselves.

### End-to-end test (realtime)

`tools/e2e` plays a developer and two players against the **running** stack, with real HTTP and real WebSockets:
the developer registers, creates a game and an API key and publishes config version 1; two players get tokens and
connect, and each gets `hello`; player A asks B to be friends and B accepts (both get `conversation.added`); A sends a
direct message (A gets `message.ack`, both get `message.new`); the developer publishes config version 2 (both get
`config.updated`). Then clan chat: A creates a clan and gets its room; B joins (B gets the room, A gets `clan_joined`,
both get B's `member_joined` line); A writes in the room (both get `message.new`); B leaves (`conversation.removed`, A
gets the `member_left` line) and does not get the next message; B joins again and gets the room with the whole history,
the missed message and the member lines included, none of them unread; A kicks B (`conversation.removed` and
`clan_kicked`, A gets the `member_kicked` line); A, the last member, leaves (`conversation.removed` and `clan_disbanded`). Every frame must arrive
within 2 seconds of the action that causes it. The published configs turn `clan.enabled` and `chat.clanChat` on.

```bash
docker compose up -d --build
go run ./tools/e2e          # prints one PASS line per step; exit code 1 on the first failure
```

| Flag | Environment variable | Default |
|---|---|---|
| `-platform` | `E2E_PLATFORM_URL` | `http://localhost:8081` |
| `-social` | `E2E_SOCIAL_URL` | `http://localhost:8082` |
| `-realtime` | `E2E_REALTIME_URL` | `http://localhost:8083` |
| `-deadline` | | `2s` |

Each run leaves one developer, one game and two players in the development database (the emails and names are unique per
run, so runs do not collide). It is for local use, not CI: CI has no Docker stack. `tools/e2e/config.json` is a complete,
valid widget config (a copy of `services/platform/internal/widgetconfig/testdata/examples/valid/farm-minimal.json`); change it
if the config contract changes.

### Integration tests (real database)

Some repository tests talk to a real Postgres and are **skipped** unless a test database URL is set.
With the Docker stack running (`docker compose up -d`, so the migrations are applied):

```bash
# PowerShell
$env:SOCIAL_TEST_DATABASE_URL="postgres://social_user:<SOCIAL_DB_PASSWORD>@localhost:5432/clan?sslmode=disable"
go test ./services/social/internal/repository/ -v
```

The platform repository tests use `PLATFORM_TEST_DATABASE_URL` (`postgres://platform_user:<PLATFORM_DB_PASSWORD>@localhost:5432/clan?sslmode=disable`) and `./services/platform/internal/repository/`.
The realtime repository tests work the same way with `REALTIME_TEST_DATABASE_URL`
(`postgres://realtime_user:<REALTIME_DB_PASSWORD>@localhost:5432/clan?sslmode=disable`) and `./services/realtime/internal/repository/` (the backfill test in `./services/realtime/internal/backfill/` and the duplicate-event test in `./services/realtime/internal/consumer/` use the same variable).

Each run uses new random game IDs and deletes its rows afterwards, so it does not disturb other data.

The outbox tests in `./pkg/events/` also use `SOCIAL_TEST_DATABASE_URL`. Each one creates a table of its own with a
random name (`outbox_test_...`) from the social outbox migration and drops it afterwards, so a running social service never
sees their rows.

The event publisher has one test that needs a real RabbitMQ (`docker compose up -d rabbitmq`), skipped unless
`TEST_RABBITMQ_URL` is set. It uses its own temporary queue and the routing key `test.published`, so nothing lands in
`realtime.events`:

```bash
# PowerShell
$env:TEST_RABBITMQ_URL="amqp://clan:<RABBITMQ_PASSWORD>@localhost:5672/"
go test -run TestPublishToRealBroker -v ./pkg/events/
```

The Realtime consumer has two tests on the same variable (`./services/realtime/internal/consumer/`): a rejected event
reaches a dead-letter queue and the DLQ check warns with its count, and a missing dead-letter queue does not stop the
consumer. They make their own temporary queues (`test.dlq.<uuid>`), so `realtime.events` and its DLQ are not touched.
`./tools/dlq/` has one more on the same variable: it makes dead events in such temporary queues, lists them and replays
one and then all of them.

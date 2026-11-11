# clan-backend

Backend for the clan platform: three Go services in one repository (realtime is a skeleton so far).

| Service | Owns | Port | Database schema |
|---|---|---|---|
| `services/platform` | Developers, registered games, API keys, player tokens | 8081 | `platform` |
| `services/social` | Players, clans, clan members | 8082 | `social` |
| `services/realtime` | WebSocket connections, chat, presence (so far: connections, the hub, `hello`, friend events from RabbitMQ that create and remove DM conversations, sending messages, and presence with a 10 s offline grace) | 8083 | `realtime` |

## Layout

```
clan-backend/
├── go.mod                     # one Go module for the whole repo
├── docker-compose.yml         # postgres + rabbitmq + platform + social + realtime + traefik
├── deploy/postgres/init/      # creates the schemas and database users on first start
├── deploy/traefik/            # gateway: static config and the /realtime route
├── deploy/rabbitmq/           # definitions.json: exchange, queues and bindings, applied by rabbitmq-setup
├── openapi/                   # API contracts: platform.yaml, social.yaml, realtime.yaml, realtime-ws.md
├── docs/                      # events.md: how services publish and consume events; event-messages.md: every event message and the clan chat frames; realtime-flow.md: the flows as UML diagrams
├── tools/                     # devtoken: test tokens; e2e: end-to-end check of the realtime flow
├── .gitlab-ci.yml             # CI: build, vet and test with the race detector
├── pkg/                       # shared code, used by both services
│   ├── config/                # read settings from environment variables
│   ├── logx/                  # JSON logger (slog)
│   ├── pg/                    # PostgreSQL connection pool (pgx)
│   ├── httpx/                 # error format, JSON helpers, middleware, server with graceful shutdown
│   └── jwt/                   # sign (platform) and verify (social) tokens, Ed25519
└── services/
    ├── platform/
    │   ├── cmd/main.go        # starts the service and wires the layers
    │   ├── internal/
    │   │   ├── handler/       # HTTP: routes, reading requests, writing JSON
    │   │   ├── service/       # business rules, no HTTP and no SQL
    │   │   └── repository/    # SQL, platform schema only
    │   │   ├── migrations/        # SQL for tables in the platform schema
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

The repo is one Go module, but the two services are still separate programs with separate `main` packages, ports and database schemas.
If a service ever moves to its own repository, it gets its own `go.mod` then.

## JWT signing key

Platform signs developer and player tokens with an Ed25519 private key.
Social and realtime check player tokens with the matching public key, so they can verify tokens but never create them.
Generate the pair once (in Git Bash on Windows); neither file is committed (`*.pem` is in `.gitignore`),
and neither service starts without its key.

```bash
mkdir -p secrets
openssl genpkey -algorithm ed25519 -out secrets/platform_jwt_private.pem
openssl pkey -in secrets/platform_jwt_private.pem -pubout -out secrets/platform_jwt_public.pem
chmod 644 secrets/platform_jwt_private.pem secrets/platform_jwt_public.pem   # local dev only: containers run as a non-root user and must read them
```

Create both files **before** `docker compose up`: if a mounted file is missing, Docker creates an empty folder with its name instead.

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_JWT_PRIVATE_KEY_FILE` | none (required) | Path to the PEM file. Docker Compose sets it to the mounted `secrets/platform_jwt_private.pem`. |
| `PLATFORM_DEVELOPER_TOKEN_TTL` | `24h` | How long a developer token is valid (Go duration such as `30m`). |
| `PLATFORM_INTERNAL_TOKEN` | none | Shared secret of platform's `/internal` endpoints, sent by other services as `X-Internal-Token`. At least 32 characters (`openssl rand -hex 32`); a shorter one stops platform at startup. Empty switches the endpoints off: they answer 404 like an unknown route. Never logged. |
| `SOCIAL_JWT_PUBLIC_KEY_FILE` | none (required) | Platform's public key, used by social to check player tokens. Docker Compose sets it to the mounted `secrets/platform_jwt_public.pem`. |
| `SOCIAL_INTERNAL_TOKEN` | none | Shared secret of social's `/internal` endpoints, sent by other services as `X-Internal-Token`. At least 32 characters (`openssl rand -hex 32`); a shorter one stops social at startup. Empty switches the endpoints off: they answer 404 like an unknown route. Never logged. |
| `SOCIAL_PLATFORM_URL` | none (docker compose: `http://platform:8081`) | Where social reads each game's clan rules: `clan.maxMembers` from the game's published config (`GET /api/v1/public/games/{gameID}/widget-config`). Empty: every game gets 10 members. |
| `SOCIAL_CONFIG_CACHE_TTL` | `30s` | How long social keeps a game's clan rules. A new limit published in Studio applies within this time. If platform cannot be reached, the last known rules (or 10) are used and a warning is logged; requests never fail because of it. |
| `REALTIME_JWT_PUBLIC_KEY_FILE` | none (required) | The same public key, for realtime. Only the public key is mounted into the realtime container. |
| `REALTIME_SOCIAL_URL` | `http://localhost:8082` | Where realtime reaches the social service to learn each connecting player's own ID. Docker Compose sets it to `http://social:8082`. |
| `REALTIME_BACKFILL` | `false` | `true` makes realtime read every friendship from social's `/internal/v1/friendships` once at startup, before it reads events, and store the DM conversation of each pair (see "Realtime backfill"). Needs `REALTIME_INTERNAL_TOKEN`; without it realtime stops at startup. Anything but a boolean is an error. |
| `REALTIME_INTERNAL_TOKEN` | none | The secret realtime sends to social as `X-Internal-Token`; it must equal social's `SOCIAL_INTERNAL_TOKEN`. Docker Compose passes `SOCIAL_INTERNAL_TOKEN` here. Never logged. |
| `REALTIME_PLATFORM_URL` | `http://localhost:8081` | Where realtime reaches the platform service to read a game's published config (`GET /api/v1/public/games/{gameID}/widget-config`). Docker Compose sets it to `http://platform:8081`. If platform is down, realtime still runs: `hello` says `configVersion` 0 and a `config.published` event is retried once. |
| `REALTIME_RABBITMQ_URL` | none | RabbitMQ address (`amqp://user:password@host:5672/`) realtime reads events from, queue `realtime.events`. Empty means: do not read events (the service still runs). Use letters and digits only in the password. Docker Compose builds it from `RABBITMQ_USER` and `RABBITMQ_PASSWORD`. The password is never logged. |

### Get a player token from platform

A game's own server trades its secret API key for a player token (valid 1 hour). Never do this from a browser.

```bash
curl -s -X POST http://localhost:8081/api/v1/players/auth \
  -H "X-API-Key: clk_..." -H "Content-Type: application/json" \
  -d '{"externalPlayerId":"u_42","displayName":"Rashad"}'
# {"token":"eyJ...","expiresIn":3600}
```

A missing, wrong or revoked key gets `401 invalid_api_key`. The token's game is always the key's game.

### Test tokens without platform

To test social alone, make a token with the same private key:

```bash
go run ./tools/devtoken -player u_42 -name Ali                # player token, valid 1 hour
go run ./tools/devtoken -player u_43 -game <other-game-uuid>  # a player of another game
go run ./tools/devtoken -developer                            # developer token: social must reject it
go run ./tools/devtoken -ttl -1m                              # already expired: social must reject it
```

Send it as `Authorization: Bearer <token>`. `go run ./tools/devtoken -h` lists all options.

### Widget config validation

`PUT /api/v1/games/{gameID}/widget/draft` and `POST .../widget/publish` check the config against the
config v1 contract (JSON Schema 2020-12, owned by the frontend team: `docs/config-v1` in
`clan-frontend`). The schema is copied to `services/platform/internal/widgetconfig/` together with the
shared fixtures in `testdata/examples/`; when the contract changes, copy the new files over and run
`go test ./services/platform/...`.

A config over 64 KB, text that is not JSON, or a config that breaks the contract gets
`400 invalid_widget_config`. `error.details` lists every problem as `{ path, rule, message }`
(`path` is a JSON Pointer such as `/clan/maxMembers`, `rule` a JSON Schema keyword such as `maximum`).
Nothing is saved or published in that case. Publishing checks the saved draft again, so a draft saved
under older rules cannot go public.

## Run with Docker (recommended)

```bash
cp .env.example .env        # Windows PowerShell: Copy-Item .env.example .env
# edit .env: set the five passwords (letters and digits only)
# create the JWT signing key first (see "JWT signing key" above)
docker compose up --build
```

This starts these containers:

| Container | URL |
|---|---|
| postgres | `localhost:5432` (change with `POSTGRES_HOST_PORT`) |
| rabbitmq | `localhost:5672` (AMQP), management UI http://localhost:15672 (log in with `RABBITMQ_USER` / `RABBITMQ_PASSWORD`) |
| rabbitmq-setup | none: declares the RabbitMQ exchange, queue and bindings, then exits (see "RabbitMQ events") |
| platform-migrate | none: applies pending platform SQL migrations, then exits (see "Database migrations") |
| social-migrate, realtime-migrate | none: the same for the social and realtime schemas |
| platform | http://localhost:8081/health, http://localhost:8081/ready |
| social | http://localhost:8082/health, http://localhost:8082/ready |
| realtime | http://localhost:8083/health, http://localhost:8083/ready |
| traefik | gateway on `localhost:80` (change with `TRAEFIK_HOST_PORT`): http://localhost/realtime/v1/healthz (see "Gateway (Traefik)") |

Each service starts only after its own migrate job finished successfully.

On the first start, `deploy/postgres/init/01-schemas.sh` creates:

| Database user | Owns schema | Can use other schemas? |
|---|---|---|
| `platform_user` | `platform` | No |
| `social_user` | `social` | No |
| `realtime_user` | `realtime` | No |

The init script runs only when the database volume is empty. After changing it, reset the database with
`docker compose down -v` (this deletes all local data) and start again.

**Database created before realtime existed:** it has no `realtime_user`, and the init script will not run again.
Either reset with `down -v`, or add the user and schema once (use your `REALTIME_DB_PASSWORD`, letters and digits only):

```bash
docker compose exec -T postgres psql -U postgres -d clan -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE realtime_user LOGIN PASSWORD 'your-realtime-password';
GRANT CONNECT ON DATABASE clan TO realtime_user;
CREATE SCHEMA realtime AUTHORIZATION realtime_user;
ALTER ROLE realtime_user SET search_path = realtime;
SQL
```

## Gateway (Traefik)

Traefik is the public entry point; today it only routes realtime. Browsers and the SDK use the public paths, and Traefik
rewrites them to the service's own paths (`deploy/traefik/dynamic/realtime.yml`):

| Public URL (through Traefik, port 80) | Reaches realtime as |
|---|---|
| `/realtime/v1/healthz` | `/health` |
| `/realtime/v1/readyz` | `/ready` |
| `/realtime/v1/<rest>` (conversations, messages, `ws`) | `/api/v1/<rest>` (for example `ws://localhost/realtime/v1/ws?token=...` reaches `/api/v1/ws`) |

- Check it: `curl http://localhost/realtime/v1/healthz` returns 200 `{"service":"realtime","status":"ok"}`.
- The entrypoint timeouts in `deploy/traefik/traefik.yml` are above 60 s so an idle WebSocket is not cut by the gateway.
- There is no access log on purpose: the WebSocket URL carries the player token in its query string.
- Port 80 busy on your computer? Set `TRAEFIK_HOST_PORT` in `.env` and use that port in the URLs.

## RabbitMQ events

The full guide (flow diagram, every event's fields, publisher and consumer behavior, how to test, open gaps) is in [`docs/events.md`](docs/events.md).

Services talk to each other through one topic exchange, `socialkit.events`. `docker compose up` starts RabbitMQ and then
runs the one-shot `rabbitmq-setup` job, which imports `deploy/rabbitmq/definitions.json`:

| Queue (durable) | Bound to routing keys |
|---|---|
| `realtime.events` | `config.published`, `friend.*`, `player.*`, `clan.*` |

The queue exists before the Realtime service does, so events published today wait in it instead of being lost.
Open http://localhost:15672, go to **Queues**, and watch `realtime.events` count them.

- The job is safe to repeat: importing definitions only adds what is missing and never deletes messages.
  To re-apply it after editing `definitions.json`: `docker compose run --rm rabbitmq-setup`.
- A new queue or binding goes into `definitions.json` in the same commit as the code that needs it.
- Without `docker compose` (for example `go run` against your own broker), import the same file in the management UI
  (**Overview > Import definitions**).
- Check by hand: in the management UI open **Exchanges > socialkit.events > Publish message**, use routing key
  `friend.accepted`, and the `realtime.events` message count goes up by one. A key no queue is bound to
  (for example `other.thing`) is dropped.

### Event format

Every message on `socialkit.events` is JSON with the same envelope (`events.Event` in `pkg/events`), and its routing
key is its `type`. Realtime decodes the same struct, so a renamed field breaks both sides at compile time.

| Field | Meaning |
|---|---|
| `event_id` | Random UUID, also the message's `message_id`. Lets a consumer notice the same event delivered twice. |
| `type` | The event name and the routing key, for example `friend.accepted` |
| `occurred_at` | When it happened, RFC 3339 in UTC |
| `game_id` | The game the event belongs to |
| `data` | Fields that depend on `type` (table below); left out when empty |

```json
{ "event_id": "3f1c0b7e-9a2d-4c51-8e0f-6b7a1d2c3e4f", "type": "friend.accepted",
  "occurred_at": "2026-11-10T08:00:00Z", "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15", "data": { } }
```

Messages are persistent (`delivery_mode` 2) with `content_type` `application/json`.

Every event with its `data` (the full messages, as they travel on the queue, are in [`docs/event-messages.md`](docs/event-messages.md)):

| Event (routing key) | Published by | `data` |
|---|---|---|
| `friend.request_sent` | social, `POST /api/v1/friends/requests` (201) | `{ "request_id", "sender": { "id", "name" }, "receiver": { "id", "name" } }`; `request_id` is the request's own ID, so a notification can accept it |
| `friend.request_cancelled` | social, `DELETE /api/v1/friends/requests/{requestID}` (204) | `{ "request_id", "sender": { "id", "name" }, "receiver": { "id", "name" } }` |
| `friend.request_declined` | social, `POST /api/v1/friends/requests/{requestID}/decline` (204) | the same as `friend.request_cancelled` |
| `friend.accepted` | social, accept, or a request sent back (200) | `{ "request_id", "players": [ { "id", "name" }, { "id", "name" } ] }`, the smaller ID first; `request_id` is the request that was accepted (for a request sent back: the older one) |
| `friend.removed` | social, `DELETE /api/v1/friends/{playerID}` (204) | `{ "players": [ { "id", "name" }, { "id", "name" } ], "removed_by" }`, the smaller ID first; `removed_by` is the ID of the player who removed the other |
| `player.renamed` | social, the first request whose player token carries a new display name (any endpoint) | `{ "player": { "id", "name" } }` with the new name; once per rename, not on the first request |
| `clan.created` | social, `POST /api/v1/clans` (201) | `{ "clan": { "id", "name" }, "player": { "id", "name" } }`; `player` is the owner |
| `clan.member_joined` | social, `POST /api/v1/clans/{clanID}/join` (200) | `{ "clan", "player" }` |
| `clan.member_left` | social, `POST /api/v1/clans/{clanID}/leave` (204) while others remain | `{ "clan", "player", "reason": "left", "kicked_by": null, "new_owner" }`; `new_owner` is the member who took over when the owner left, otherwise `null` |
| `clan.member_left` (kick) | social, `DELETE /api/v1/clans/{clanID}/members/{playerID}` (204) | the same fields with `"reason": "kicked"`, `kicked_by` = the member who kicked `{ "id", "name" }`, `new_owner` always `null` |
| `clan.member_role_changed` | social, `POST /api/v1/clans/{clanID}/members/{playerID}/promote` or `/demote` (200) | `{ "clan", "player", "role", "previous_role", "changed_by" }`; `role` and `previous_role` are role ids of the game's `clan.roles` |
| `clan.disbanded` | social, the last member leaves (204) | `{ "clan" }`; only this event, no `clan.member_left` |
| `clan.renamed` | social, `PATCH /api/v1/clans/{clanID}` (200) when the name changed | `{ "clan": { "id", "name" }, "previous_name" }`; `clan.name` is the new name. Sending the same name publishes nothing |
| `player.blocked`, `player.unblocked` | social (block task) | `{ "blocker_id", "blocked_id" }` |
| `config.published` | platform, `POST /api/v1/games/{gameID}/widget/publish` and `.../widget/versions/{version}/rollback` (200); realtime then sends `config.updated` to the game's connections | `{ "version" }`, the new version number (a rollback saves a new version, so it is that number) |

`id` is always the social service's player ID (a UUID, as in `conversation_members.player_id` in Realtime), and
`name` the display name at the time of the event.

**Realtime reads six of them** (the others that reach `realtime.events` are acknowledged and ignored until their tasks exist):

| Event | Realtime does |
|---|---|
| `friend.request_sent` | sends a `friend_request` notification to the `receiver` (nothing is stored). `request_id` goes into the notification as `requestId`, so the SDK can accept that very request. It is optional here only so that an older event without it is still shown (`requestId: null`). |
| `friend.request_cancelled` | sends a `friend_request_cancelled` notification to the `receiver` (all open tabs) so the SDK removes the `friend_request` with the same `requestId`. `request_id` is required. The sender is not told. |
| `friend.request_declined` | sends a `friend_request_declined` notification to the `receiver` only (their other tabs drop the `friend_request`). `request_id` is required. The sender is **not** told. |
| `friend.accepted` | creates the DM conversation of the two `players` and both memberships (or reuses it), sends `conversation.added` and a `friend_accepted` notification to both. Exactly two players are required. The notification carries `requestId` (`null` when the event has no `request_id`). |
| `friend.removed` | deletes both memberships (the messages stay) and sends `conversation.removed` to both, with no notification. Exactly two `players` are required and `removed_by` must be one of them. |
| `player.renamed` | stores the new name with the player's memberships in that game. No frame is sent; messages keep the name their sender had when writing them. |

A field Realtime needs that is missing or malformed makes it drop that event (logged as `dropping unusable event`).
So **Social and Realtime must keep these field names in step**; `TestDispatchReadsEventsAsSocialPublishesThem` in
`services/realtime/internal/consumer` checks the shape Social publishes today, and
`TestDispatchReadsEveryMessageInTheEventMessagesFile` feeds every message of `docs/event-messages.md` to the dispatcher.

**How realtime reads the queue**

- One consumer, one event at a time, in order. Prefetch is 20, acknowledgement is manual and comes after the database write.
- An event that fails (for example the database is down) is put back **once** after one second; if it fails again it is logged
  and dropped. An unusable event (not JSON, missing field, id that is not a UUID) is logged and dropped at once.
  There is no dead-letter queue yet, so a dropped event is lost.
- Every database write is an upsert or a delete, so a redelivered event changes nothing. The WebSocket frames are sent again;
  the SDK ignores a conversation it already has.
- If the broker is down or restarts, realtime keeps running and reconnects (1 s, 2 s, 4 s ... up to 30 s).
  Without a database it does not read events at all, because it would have nowhere to store them.

### Publishing from a service

- Social publishes when `SOCIAL_RABBITMQ_URL` is set (docker compose sets it). Without it social starts anyway, logs
  `SOCIAL_RABBITMQ_URL is not set`, and drops its events.
- The publisher (`events.NewRabbitMQ`) connects on the first event, not at startup, and reconnects by itself. After a
  failed connection it waits 5 s before trying again, so requests never queue up behind a dead broker.
- Every publish waits for the broker to confirm it (up to 5 s).
- Services get the publisher wrapped in `events.Safe`: a failed publish is logged as `publish event failed` with its
  `event_id` and `type`, and the user's request still succeeds.
- Publish only after the database change has committed, so an event never announces something that was rolled back.
- Service tests use `events.Recorder` to check which events were published.

**Known gap: an event can be lost.** If RabbitMQ is down or does not confirm in time, the database change stays and
the event is gone (only the log line remains). Realtime then misses a friendship until it is backfilled (see "Realtime backfill" in the Realtime section).
*Future work:* a transactional outbox: write the event to an `outbox` table in the same transaction as the change,
and let a background worker publish it and mark it sent, retrying until the broker accepts it.

## Run with go run

Start Postgres and apply the migrations in Docker, then run a service from your editor or terminal:

```bash
docker compose up -d postgres
docker compose run --rm platform-migrate     # safe to repeat
docker compose run --rm social-migrate   # creates or updates the social tables
docker compose run --rm realtime-migrate # creates or updates the realtime tables
go run ./services/platform/cmd      # uses PLATFORM_DATABASE_URL
go run ./services/social/cmd        # uses SOCIAL_DATABASE_URL
go run ./services/realtime/cmd      # uses REALTIME_DATABASE_URL
```

Set the `*_DATABASE_URL` variables from `.env.example` in your terminal or GoLand run configuration.
Without a database URL the services still start; `/health` answers 200 and `/ready` answers 503.

## Database migrations

Table definitions live in `services/<name>/migrations/` as `NNNNNN_name.up.sql` / `NNNNNN_name.down.sql`.
`docker compose up` applies the pending ones automatically: the `platform-migrate` job (and `social-migrate`, `realtime-migrate`)
([golang-migrate](https://github.com/golang-migrate/migrate)) runs first, as the service's own database user,
so tables land in that service's schema. It remembers what already ran in a `schema_migrations` table in that
schema, so running it again does nothing.

```bash
docker compose exec -T postgres psql -U platform_user -d clan -v ON_ERROR_STOP=1 \
  < services/platform/migrations/000001_create_developers.up.sql
```

- **Add a migration:** create the next numbered pair, e.g. `000003_add_x.up.sql` and `.down.sql`, then run
  `docker compose up` (or `docker compose run --rm platform-migrate`).
- **Never edit a migration that has already run** (on `develop` or on a teammate's machine). Add a new numbered file instead.
- **A migration failed:** the job logs the SQL error and the service does not start. Golang-migrate marks the database
  "dirty". Fix the problem by hand in the database, then clear the mark with
  `docker compose run --rm platform-migrate -path=/migrations -database="$URL" force <last good version>`
  (`$URL` is the `-database` value from `docker-compose.yml`; the same works for `social-migrate` and `realtime-migrate`). On a local database it is usually quicker to run
  `docker compose down -v` and start again.
- **Roll back one step:** the same command with `down 1` instead of `force ...`.
- **Database created before this existed** (tables applied by hand): `docker compose down -v` once, then start again.

## API contracts (OpenAPI)

| Service | File |
|---|---|
| platform | `openapi/platform.yaml` |
| social | `openapi/social.yaml` |
| realtime | `openapi/realtime.yaml` (REST) and `openapi/realtime-ws.md` (WebSocket frames); the REST endpoints are not built yet, so there is no route test for it yet |

These files are the contract the dashboard, SDK and demo game build against.
When you add or change a route, update `Routes()` in the service's `internal/handler/handler.go` **and** its OpenAPI file in the same commit.
`go test ./...` fails if a route is missing from the file, or the file lists a route that does not exist.
To browse a file, paste it into https://editor.swagger.io or open it with the OpenAPI plugin in GoLand.

## CORS (which websites may call the API)

| Service | Variable | Default | Who calls it from a browser |
|---|---|---|---|
| platform | `PLATFORM_CORS_ORIGINS` | `http://localhost:5173` | Dashboard (clan-frontend) |
| social | `SOCIAL_CORS_ORIGINS` | `http://localhost:5174` | Demo game with the SDK |
| realtime | `REALTIME_CORS_ORIGINS` | `http://localhost:5174` | Demo game with the SDK |

- Comma-separated list, each written exactly as `scheme://host:port` (no trailing slash). `*` is rejected at startup.
- Requests from other websites get 403 `origin_not_allowed`.
- Requests without an `Origin` header (servers, curl) are not affected, so the game's own server can still call `/players/auth`.

## Check

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...     # the same, with the race detector (needs gcc; CI runs this one)
```

CI (`.gitlab-ci.yml`) runs `go build`, `go vet` and `go test -race` on every merge request and on `develop` and `main`.
It needs no database or broker: the unit tests use fakes, and the tests that need a real database or RabbitMQ skip themselves.

### End-to-end test (realtime)

`tools/e2e` plays a developer and two players against the **running** stack, with real HTTP and real WebSockets:
the developer registers, creates a game and an API key and publishes config version 1; two players get tokens and
connect, and each gets `hello`; player A asks B to be friends and B accepts (both get `conversation.added`); A sends a
direct message (A gets `message.ack`, both get `message.new`); the developer publishes config version 2 (both get
`config.updated`). Every frame must arrive within 2 seconds of the action that causes it.

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

The realtime repository tests work the same way with `REALTIME_TEST_DATABASE_URL`
(`postgres://realtime_user:<REALTIME_DB_PASSWORD>@localhost:5432/clan?sslmode=disable`) and `./services/realtime/internal/repository/` (the backfill test in `./services/realtime/internal/backfill/` uses the same variable).

Each run uses new random game IDs and deletes its rows afterwards, so it does not disturb other data.

The event publisher has one test that needs a real RabbitMQ (`docker compose up -d rabbitmq`), skipped unless
`TEST_RABBITMQ_URL` is set. It uses its own temporary queue and the routing key `test.published`, so nothing lands in
`realtime.events`:

```bash
# PowerShell
$env:TEST_RABBITMQ_URL="amqp://clan:<RABBITMQ_PASSWORD>@localhost:5672/"
go test -run TestPublishToRealBroker -v ./pkg/events/
```

## Endpoints so far

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| POST | `/api/v1/developers/register` | 201 with the new developer; 400 invalid email, password or name; 409 `email_taken` |
| POST | `/api/v1/developers/login` | 200 `{ token, expiresIn }`; 401 `invalid_credentials` |
| GET | `/api/v1/developers/me` | 200 with the developer; 401 unless `Authorization: Bearer <developer token>` |
| GET | `/api/v1/games` | 200 `{ games: [...] }` with only the logged-in developer's games, newest first (each with its `allowedOrigins`); 401 without a developer token |
| POST | `/api/v1/games` | 201 with the registered game (registers an existing game, never creates one); 400 `invalid_name` / `invalid_description`; 401 |
| PUT | `/api/v1/games/{gameID}/allowed-origins` | Body `{ allowedOrigins: [...] }` replaces the list of websites that may open a WebSocket for the game (200 with the game). Each origin is `scheme://host[:port]` as the browser sends it, `http` or `https`, no path or trailing slash, no wildcard; hosts are lower-cased, repeats dropped, at most 20. `[]` clears it. 400 `invalid_origins` (nothing is saved); 404 `game_not_found` unless you own the game |
| POST | `/api/v1/games/{gameID}/api-keys` | 201 with the new key; **the full `key` is in this response only** (the database stores just a hash). 404 `game_not_found` unless you own the game |
| GET | `/api/v1/games/{gameID}/api-keys` | 200 `{ apiKeys: [...] }`: prefix, created and revoked time, never the key. 404 unless you own the game |
| DELETE | `/api/v1/api-keys/{keyID}` | 204; the key stops working at once and stays listed as revoked. 404 `api_key_not_found` unless it is a key of your game |
| GET | `/api/v1/games/{gameID}/widget/draft` | 200 `{ config, updatedAt }`: the widget config being edited, or the default config with `updatedAt: null` when nothing was saved yet. Players never see drafts. 404 `game_not_found` unless you own the game |
| PUT | `/api/v1/games/{gameID}/widget/draft` | 200 with the saved draft. Body `{ "config": { ... } }`: a JSON object of at most 64 KB that replaces the whole draft. 400 `invalid_widget_config` / `invalid_json`; 404 unless you own the game |
| POST | `/api/v1/games/{gameID}/widget/publish` | 201 with the new version `{ version, publishedAt, publishedBy, config }`. No body: it publishes the saved draft (the draft stays as it is). 409 `widget_draft_missing` if nothing was saved; 400 `invalid_widget_config`; 404 unless you own the game |
| GET | `/api/v1/games/{gameID}/widget/versions` | 200 `{ versions: [...] }`, newest first, without configs; an empty list if never published. 404 unless you own the game |
| GET | `/api/v1/games/{gameID}/widget/versions/{version}` | 200 with that version and its `config`. 404 `widget_version_not_found` (also for a number that is not a positive whole number) |
| POST | `/api/v1/games/{gameID}/widget/versions/{version}/rollback` | 201 with a **new** version that has the old version's config; nothing is deleted and the draft is untouched. 404 `widget_version_not_found` |
| GET | `/api/v1/public/games/{gameID}/widget-config` | **No login, open to every website.** 200 `{ version, config }`: the latest **published** version (never a draft), with a strong `ETag` (`"v3"`) and `Cache-Control: public, max-age=60`. Send `If-None-Match` to get 304. 404 `widget_not_published` (also for an unknown game ID). Not rate-limited in the service: put a CDN or proxy limit in front of `/api/v1/public/` |
| GET | `/internal/v1/games/{gameID}/config` | **For other services, not browsers.** Needs `X-Internal-Token: <PLATFORM_INTERNAL_TOKEN>` (401 `invalid_internal_token`; 404 `not_found` while the secret is not set). 200 `{ gameId, version, config, allowedOrigins }`: the newest **published** config and the game's allowed origins, never cached. A game never published has `version: 0` and `config: null` but still its origins. 404 `game_not_found` for an unknown game |
| any | anything else | 404 `not_found` in the standard error format |
| any | from a website not in the allowed origins | 403 `origin_not_allowed` |

### Social service (port 8082)

Every `/api/v1/` route needs `Authorization: Bearer <player token>`. The game and the player
always come from the token, never from the URL or the body.

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/players/me` | 200 with the calling player; the record is created on the first call |
| GET | `/api/v1/players?q=&limit=` | 200 `{ players: [...] }`: players of the caller's game whose name contains `q` (ignoring upper/lower case; `%` and `_` are normal characters), sorted by name, never the caller. Each has `id`, `displayName`, `relation` (`none` / `friend` / `request_sent` / `request_received`) and, for the two pending ones, `requestId`. `q` is required: 400 `invalid_query` (missing, empty or over 50 characters); `limit` 1 to 100, default 100 (400 `invalid_limit`) |
| GET | `/api/v1/players/me/clan` | 200 `{ clan, role }` with the caller's clan and role (`owner` / `member`); 404 `not_in_clan` |
| GET | `/api/v1/clans?q=&limit=&sort=&hasSpace=` | 200 `{ clans: [...] }` of the caller's game, by default sorted by name, at most 100. `q` keeps clans whose name contains it (ignoring upper/lower case, over 50 characters gives 400 `invalid_query`) and searches **all** clans, full ones too. `limit` 1 to 100 (400 `invalid_limit`); `sort` `name` or `members`, biggest first (400 `invalid_sort`); `hasSpace=true` leaves out full clans (400 `invalid_has_space`). Featured clans = `?hasSpace=true&sort=members&limit=3` |
| POST | `/api/v1/clans` | body `{ "name": "Wolves" }`. 201 with the new clan, the caller becomes its owner. 400 `invalid_json` / `invalid_clan_name` (3 to 20 characters); 409 `clan_name_taken` / `already_in_clan` |
| PATCH | `/api/v1/clans/{clanID}` | body `{ "name": "Grey Wolves" }`. 200 with the clan. Only the **owner**; the name is checked like on create. The same name changes nothing (200, no event); a change of upper/lower case only is a rename. 400 `invalid_json` / `invalid_clan_name`; 403 `not_clan_owner`; 404 `clan_not_found`; 409 `clan_name_taken`. Publishes `clan.renamed` |
| POST | `/api/v1/clans/{clanID}/join` | no body. 200 with the clan and its new `memberCount`, the caller joins as a member. 404 `clan_not_found` (also for another game's clan); 409 `already_in_clan` / `clan_full`: the clan has as many members as the game allows (`clan.maxMembers` from the published config, 10 before the first publish). Lowering the limit removes nobody; a clan over it just takes no one new |
| POST | `/api/v1/clans/{clanID}/leave` | no body. 204: the caller left the clan and can join another one. A normal member is just removed. If the **owner** leaves and others remain, the member who joined first becomes the owner; if the owner is the only member, the clan is deleted. 404 `clan_not_found` (also for another game's clan); 409 `not_in_clan` (the caller is not a member of this clan) |
| GET | `/api/v1/clans/{clanID}/members` | 200 `{ members: [...] }` with `playerId`, `displayName`, `role`, `joinedAt`; owner first, then by join time. `role` is a role id of the game's `clan.roles` (see "Clan roles" below). 404 `clan_not_found` (also for another game's clan) |
| DELETE | `/api/v1/clans/{clanID}/members/{playerID}` | no body. 204: the caller removed that member (`playerId` from the member list): their role needs `canKick` and the member must have a lower role (the owner may remove anyone). The player can join a clan again at once. 403 `not_allowed`; 400 `cannot_kick_self` (leave instead); 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_left` with `reason` `kicked` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/promote` | no body. 200 `{ playerId, displayName, role }`: the member is one role up. The caller's role needs `canPromote`, the member must be lower, and the new role stays below the caller's own (nobody becomes owner). 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/demote` | no body. 200 `{ playerId, displayName, role }`: one role down, same rules. 409 `already_lowest_role`; 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| GET | `/api/v1/friends` | 200 `{ friends: [...] }` with `id`, `displayName`, `since`, sorted by name |
| GET | `/api/v1/friends/requests` | 200 `{ incoming: [...], outgoing: [...] }`, newest first; each has `id`, `player` (the other player: `id`, `displayName`) and `createdAt` |
| POST | `/api/v1/friends/requests` | body `{ "playerId": "<social player ID>" }`. 201 with the new request. If that player had already asked the caller, it is accepted instead: 200 with the new friend. 400 `invalid_json` / `invalid_player_id` / `cannot_friend_yourself`; 404 `player_not_found` (also for another game's player); 409 `already_friends` / `friend_request_exists`. Publishes `friend.request_sent` or `friend.accepted` |
| POST | `/api/v1/friends/requests/{requestID}/decline` | no body. 204: the request sent to the caller is deleted. 403 `not_request_receiver` (the caller sent it; cancel instead); 404 `friend_request_not_found`. Publishes `friend.request_declined` |
| DELETE | `/api/v1/friends/requests/{requestID}` | 204: the caller's own request is cancelled. 403 `not_request_sender` (the caller received it; decline instead); 404 `friend_request_not_found`. Publishes `friend.request_cancelled` |
| DELETE | `/api/v1/friends/{playerID}` | 204: the friendship ends for both players; either may send a new request afterwards. 404 `not_friends` (also for another game's player). Publishes `friend.removed` |
| POST | `/api/v1/friends/requests/{requestID}/accept` | no body. 200 with the new friend. 403 `not_request_receiver` (the caller sent it); 404 `friend_request_not_found`. Publishes `friend.accepted` |

**Clan roles.** Each game sets its clan roles in Studio (`clan.roles` in its published config, lowest power first,
2 to 12). Social reads them with the clan size (`SOCIAL_PLATFORM_URL`, cached `SOCIAL_CONFIG_CACHE_TTL`) and decides every
kick, promote and demote by them, inside the clan's row lock (package `internal/roles`):

- A new member gets the first role. The owner (`clans.owner_id`) always has the last role; their row stores `owner`.
- A member acts only on someone with a lower role, never on an equal, a higher one or themselves. Kick needs `canKick`;
  promote and demote need `canPromote`. A promote stays below the caller's own role, so nobody becomes owner.
- A role id the developer removed or renamed reads as the first role: nobody is removed. Before a game publishes
  roles there are two, `member` and `owner`, and only the owner may kick (as before roles).
- `clan_members.role` stores the role id; the database only checks its form (migration `000005`).

**Internal endpoints** are for other services, not for the SDK. They need `X-Internal-Token: <SOCIAL_INTERNAL_TOKEN>`
(401 `invalid_internal_token` otherwise; a player token is not accepted). They are the only routes that
see more than one game.

| Method | Path | Answer |
|---|---|---|
| GET | `/internal/v1/friendships?after=&limit=` | 200 `{ friendships: [...], nextCursor }`: every friendship of every game, page by page, in a fixed order. Each has `gameId`, `playerA` and `playerB` (`id`, `displayName`; `playerA` has the smaller ID) and `since`. `limit` 1 to 500, default 200 (400 `invalid_limit`). Pass `nextCursor` as `after` for the next page; it is missing on the last page; `after` must come from a previous page (400 `invalid_cursor`). The realtime backfill (TEAM-151) reads it |

### Realtime service (port 8083)

**Realtime backfill.** Realtime learns about friendships from events, so it knows nothing about friendships made
before it ran, or whose event was lost. With `REALTIME_BACKFILL=true` (and `REALTIME_INTERNAL_TOKEN` equal to social's
`SOCIAL_INTERNAL_TOKEN`) it reads every friendship from social's `GET /internal/v1/friendships`, page by page, and stores
the DM conversation of each pair. It runs once, after the database is connected and **before** the event consumer and
the HTTP server start, so events then arrive on top of a complete state. Details:
- It only adds. A conversation whose friendship Social no longer lists (a lost `friend.removed`) is left as it is.
- It is safe to leave on and to run again: stored conversations, members and messages are reused, names are updated.
- No frames are sent; nobody is told anything.
- A page that social cannot answer is tried again (5 tries, 1 s doubling to 8 s). When it keeps failing realtime logs
  `backfill failed, starting anyway` and runs without it; restart with the flag on to try again. A wrong token (401 or 404
  from social) is reported the same way.
- A friendship with an invalid ID or no name is skipped and counted (`skipped` in the `backfill done` log).
- Blocks and clans are **not** backfilled yet: social has no blocks, and realtime has no clan chat.
- State rebuild at startup is not needed: realtime keeps friends, memberships and blocks in its own database and reads
  them from there, so a restart already keeps chat working for everything it had stored.

The WebSocket endpoint, the chat, the conversation list and the message history are built. The contract is in
`openapi/realtime.yaml` (REST) and `openapi/realtime-ws.md` (WebSocket). The REST endpoints need
`Authorization: Bearer <player token>`; the token is checked with platform's public key and realtime then asks the social
service (`REALTIME_SOCIAL_URL`, with the player's own token) for the player's social ID, so they answer 503 `social_unavailable`
while social is down.

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| GET | `/api/v1/ws?token=<player token>` | WebSocket upgrade (101). Checked **before** the upgrade: 401 `missing_token` / `invalid_token` / `token_expired` / `wrong_token_type` for the token, 403 `origin_not_allowed` when the page's `Origin` is not in `REALTIME_CORS_ORIGINS` (requests without `Origin`, like Postman's, are allowed), 503 `social_unavailable` when the social service cannot be reached (realtime asks social for the player's own ID with `GET /api/v1/players/me` and the player's token, so **social must be running**), 503 `shutting_down`. After the upgrade the server first sends a `hello` frame with the player's social ID; a `ping` frame is answered with `pong`; a frame over 8 KB closes the connection (1009); a bad frame gets an `error` frame with `bad_frame` and the connection stays; the connection is closed with 4001 when the token expires and with 1001 when the service stops; a client that does not answer the server's pings (every 25 s) for 60 s is dropped; a client that does not read its frames (64 waiting) is closed with 4002 without holding anyone else up. Public URL through Traefik: `/realtime/v1/ws` |
| GET | `/api/v1/conversations` | 200 `{ "conversations": [...] }`: every conversation of the caller in the token's game, most recently active first, each with `members` (`online` = has an open WebSocket, or closed the last one less than 10 s ago), `lastMessage` (or `null`) and `unread` (messages of other players after the caller's read position, at most 99). 401 `missing_token` / `invalid_token` / `token_expired` / `wrong_token_type`; 403 `origin_not_allowed`; 503 `social_unavailable` |
| GET | `/api/v1/conversations/{conversationId}/messages?before=<messageId>&limit=50` | 200 `{ "messages": [...], "hasMore": true }`: **newest first**; `before` returns only older messages, `limit` is 1 to 100 (default 50). 400 `invalid_limit` / `invalid_before`; 403 `not_member` (not in it, or an unknown id or another game's: one answer for all); plus the errors above |
| any | anything else | 404 `not_found` in the standard error format |

**Try the WebSocket (Postman or any client).** Social and realtime must both be running (`docker compose up`). Make a player token
(it must be signed with the private key whose public key the services have), then connect through the gateway and send a ping:

```bash
go run ./tools/devtoken -player u_42 -name Ali        # prints a token
# Postman: New > WebSocket, URL  ws://localhost/realtime/v1/ws?token=<token>   (or ws://localhost:8083/api/v1/ws?token=<token>)
# On connect you get: {"event_id":"...","type":"hello","occurred_at":"...","game_id":"<game>","data":{"playerId":"<social uuid>","configVersion":<published config version or 0>,"onlineFriends":[]}}
# Then send:           {"type":"ping","id":"p-1","data":{}}
# You get:             {"event_id":"...","type":"pong","occurred_at":"...","game_id":"<game>","data":{"replyTo":"p-1"}}
# Every frame the server sends has the envelope of a queue event: event_id, type, occurred_at, game_id, data.
# Frames you send stay {"type","id","data"}.
```

Two players: make a second token with `go run ./tools/devtoken -player u_43 -name Rashad`, open a second Postman WebSocket tab
with it. Each tab gets its own `hello` with a different `playerId`; disconnecting one does not affect the other.

A missing or wrong token makes Connect fail with HTTP 401 and a JSON error body. The token is in the URL, so the service
and Traefik never log the query string. In `hello`, `configVersion` is the version of the game's published config (`0` when there is none, or platform did not answer in a second) and `onlineFriends` lists the friends (people the player has a DM with) who are online now (a friend who closed their last tab less than 10 s ago still counts). When the game publishes a config, realtime fetches it from platform and sends `config.updated { version }` to every open connection of that game.

**Chat between two friends (first full slice).** Two players only share a conversation after a `friend.accepted` event (Social does not
publish it yet; publish it by hand, see "RabbitMQ events" and its "Check by hand" bullet, with the `playerId`s from the two `hello` frames and
the `gid` of the tokens as `game_id`). Both tabs then get `conversation.added`; copy `data.conversation.id`. In either tab send:

```
{"type":"message.send","id":"c-1","data":{"conversationId":"<conversation id>","body":"  hello  "}}
```

The sender gets `message.ack` (`replyTo: "c-1"`, `messageId`, `createdAt`) and then, like the friend and every other open tab of either player,
`message.new` with the trimmed text. Check the rows with
`docker compose exec postgres psql -U postgres -d clan -c "select id, sender_name, body, created_at from realtime.messages order by id"`.
Errors come back as `error` frames with `replyTo` and nothing is stored or sent: `not_member` (not in the conversation, or an unknown id),
`rate_limited`, `feature_disabled`, `blocked`, `empty`, `too_long`, `slow_mode` (with `retryAfterMs`), `bad_frame` (missing or wrong-type field),
`internal` (for example the database is down).
**Message rules.** `message.send` is checked in this order, and the first failure answers (full table in `openapi/realtime-ws.md`): rate limit of the connection (5 in 5 s; three refused in a row close it with `1008`), not empty, member, the game's chat switches (`chat.enabled`, `directMessages`, `clanChat`), not blocked (DMs), `chat.maxMessageLength` in characters, slow mode (`chat.slowModeSeconds` per player across all tabs), then the profanity filter (`chat.profanityFilter`, whole words, replaced with `***`; the list is `services/realtime/internal/profanity/words.txt`). The game's rules come from its published config (kept in memory, updated on `config.published`); a game without a published config has the permissive defaults. Blocks come from the `player.blocked` and `player.unblocked` events into Realtime's own `blocks` table (Social does not publish them yet).
Presence: a player is online while one of their WebSockets is open. When the last one closes, realtime waits 10 s; if the player did not connect again, the friends who are connected get `presence.update { playerId, status: "offline" }`, so a page refresh never shows as offline. Connecting sends friends nothing (they read `hello.onlineFriends`), and a new friendship between two online players gives each a `presence.update` `online` for the other. The 10 s timers are in memory of the one process: if realtime restarts during them, nobody is told that player went offline.
Slow mode and the rate limit live in the memory of the one Realtime process (a restart forgets them).

**Postman collection and flow diagrams.** `postman/realtime-chat.postman_collection.json` runs the whole flow against a local stack: it makes its own developer, game, three players and a friendship, then checks the conversation, the history, paging, unread and the error cases (103 checks). The two things that need a WebSocket (sending messages and `conversation.read`) are two manual steps that show the exact frames to send in the Visualize tab; the collection description says how to run it. Folder 9 (TEAM-150) tries every `message.send` rule: it sets the game's rules through Platform (draft and Publish), puts `player.blocked` / `player.unblocked` on RabbitMQ (set the collection variables `rabbitUser` and `rabbitPassword`), and its STEP requests show the frames that give `rate_limited` (and the `1008` close), `feature_disabled`, `blocked`, `too_long`, `slow_mode`, `empty` and `not_member`. How everything flows, as UML diagrams: [`docs/realtime-flow.md`](docs/realtime-flow.md).

**Conversation list, history and "read" (Postman).** After the chat above, for each player (use that player's token):

```
GET http://localhost:8083/api/v1/conversations                                           Authorization: Bearer <token>
GET http://localhost:8083/api/v1/conversations/<conversation id>/messages?limit=20       Authorization: Bearer <token>
GET http://localhost:8083/api/v1/conversations/<conversation id>/messages?before=<id of the oldest message>&limit=20
```

The list shows the other player's `lastMessage` and `unread` (the number of messages the *other* player sent that you have not read).
The messages come newest first; `hasMore` is `true` while older ones exist, then ask again with `before` set to the `id` of the
last message you got. To clear the unread count send, in that player's WebSocket tab:

```
{"type":"conversation.read","id":"r-1","data":{"conversationId":"<conversation id>","lastMessageId":"<id of the newest message>"}}
```

There is no reply; `GET /api/v1/conversations` now shows `unread: 0`. A message sent later makes it `1` again. Reading is remembered per player
and only moves forward. A player who is not in the conversation gets 403 `not_member` (REST) or an `error` frame with `not_member` (WebSocket).

Only the service-wide `REALTIME_CORS_ORIGINS` list is checked for the origin so far. Platform now stores a per-game
`allowedOrigins` list (set with `PUT /api/v1/games/{gameID}/allowed-origins`) and serves it, with the active config, on
`GET /internal/v1/games/{gameID}/config`; realtime does not read it yet (TEAM-142, TEAM-148).

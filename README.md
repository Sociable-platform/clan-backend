# clan-backend

Backend for the clan platform: three Go services in one repository (realtime is a skeleton so far).

| Service | Owns | Port | Database schema |
|---|---|---|---|
| `services/platform` | Developers, registered games, API keys, player tokens | 8081 | `platform` |
| `services/social` | Players, clans, clan members | 8082 | `social` |
| `services/realtime` | WebSocket connections, chat, presence (so far: connections, the hub, `hello`, friend events from RabbitMQ that create and remove DM conversations, clan events that create and remove clan chat rooms, sending messages, and presence with a 10 s offline grace) | 8083 | `realtime` |

## Layout

```
clan-backend/
├── go.mod                     # one Go module for the whole repo
├── docker-compose.yml         # postgres + rabbitmq + platform + social + realtime + traefik
├── deploy/postgres/init/      # creates the schemas and database users on first start
├── deploy/traefik/            # gateway: static config and the /realtime route
├── deploy/rabbitmq/           # definitions.json: exchanges, queues, bindings and dead-letter policies, applied by rabbitmq-setup
├── deploy/migrate/            # Dockerfile of the migration images (migrate/migrate + a service's migrations)
├── openapi/                   # API contracts: platform.yaml, social.yaml, realtime.yaml, realtime-ws.md
├── docs/                      # events.md: how services publish and consume events; event-messages.md: every event message and the clan chat frames; realtime-flow.md: the flows as UML diagrams
├── tools/                     # devtoken: test tokens; createadmin: creates an admin account; e2e: end-to-end check of the realtime flow; dlq: list and replay dead events
├── .gitlab-ci.yml             # CI: build, vet and test with the race detector, then build and push the images
├── pkg/                       # shared code, used by both services
│   ├── config/                # read settings from environment variables
│   ├── logx/                  # JSON logger (slog)
│   ├── otelx/                 # OpenTelemetry tracing and metrics setup, off unless an endpoint is set
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
| `PLATFORM_DEVELOPER_TOKEN_TTL` | `15m` | How long a developer token is valid (Go duration such as `30m`). Short on purpose: it cannot be taken back, and the dashboard renews it with the refresh cookie (see "Developer sessions"). |
| `PLATFORM_ADMIN_TOKEN_TTL` | `1h` | How long an admin token (admin panel) is valid. Keep it short. |
| `PLATFORM_INTERNAL_TOKEN` | none | Shared secret of platform's `/internal` endpoints, sent by other services as `X-Internal-Token`. At least 32 characters (`openssl rand -hex 32`); a shorter one stops platform at startup. Empty switches the endpoints off: they answer 404 like an unknown route. Never logged. |
| `SOCIAL_JWT_PUBLIC_KEY_FILE` | none (required) | Platform's public key, used by social to check player tokens. Docker Compose sets it to the mounted `secrets/platform_jwt_public.pem`. |
| `SOCIAL_INTERNAL_TOKEN` | none | Shared secret of social's `/internal` endpoints, sent by other services as `X-Internal-Token`. At least 32 characters (`openssl rand -hex 32`); a shorter one stops social at startup. Empty switches the endpoints off: they answer 404 like an unknown route. Never logged. |
| `SOCIAL_PLATFORM_URL` | none (docker compose: `http://platform:8081`) | Where social reads each game's clan rules: `clan.maxMembers` from the game's published config (`GET /api/v1/public/games/{gameID}/widget-config`), and whether the game is suspended (the same call answers 403 `game_suspended`). Empty: every game gets 10 members and none is ever refused as suspended. |
| `SOCIAL_CONFIG_CACHE_TTL` | `30s` | How long social keeps a game's clan rules **and its suspended or active status**. A new limit published in Studio, a suspension and a resume all apply within this time. If platform cannot be reached, the last known rules and status (or 10 members, not suspended) are used and a warning is logged; requests never fail because of it. |
| `SOCIAL_OUTBOX_INTERVAL` | `1s` | How long social's outbox relay waits before looking again when it had nothing to send; also its first wait after a failed send (it then doubles, up to 30 s). See "Event outbox". |
| `SOCIAL_OUTBOX_RETENTION` | `168h` | How long a sent event stays in social's `outbox` table before the relay deletes it (checked once an hour). |
| `PLATFORM_OUTBOX_INTERVAL` | `1s` | The same as `SOCIAL_OUTBOX_INTERVAL`, for platform's outbox relay. |
| `PLATFORM_OUTBOX_RETENTION` | `168h` | The same as `SOCIAL_OUTBOX_RETENTION`, for platform's `outbox` table. |
| `REALTIME_JWT_PUBLIC_KEY_FILE` | none (required) | The same public key, for realtime. Only the public key is mounted into the realtime container. |
| `REALTIME_SOCIAL_URL` | `http://localhost:8082` | Where realtime reaches the social service to learn each connecting player's own ID. Docker Compose sets it to `http://social:8082`. |
| `REALTIME_BACKFILL` | `false` | `true` makes realtime read every friendship from social's `/internal/v1/friendships` once at startup, before it reads events, and store the DM conversation of each pair (see "Realtime backfill"). Needs `REALTIME_INTERNAL_TOKEN`; without it realtime stops at startup. Anything but a boolean is an error. |
| `REALTIME_INTERNAL_TOKEN` | none | The secret realtime sends to social as `X-Internal-Token`; it must equal social's `SOCIAL_INTERNAL_TOKEN`. Docker Compose passes `SOCIAL_INTERNAL_TOKEN` here. Never logged. |
| `REALTIME_PLATFORM_URL` | `http://localhost:8081` | Where realtime reaches the platform service to read a game's published config (`GET /api/v1/public/games/{gameID}/widget-config`). Docker Compose sets it to `http://platform:8081`. If platform is down, realtime still runs: `hello` says `configVersion` 0 and a `config.published` event is retried once. |
| `REALTIME_PROCESSED_EVENTS_RETENTION` | `168h` | How long realtime keeps the ID of a handled event, so a copy that arrives within that time is skipped. Checked once an hour; must be positive. |
| `REALTIME_DLQ_CHECK_INTERVAL` | `5m` | How often realtime counts its dead-letter queue `realtime.events.dead` while connected to the broker (also once at connect). It logs `dead-letter queue holds rejected events` with the count while the queue is not empty, and `dead-letter queue not found` when `rabbitmq-setup` has not created it. Must be positive. |
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

A valid key of a game that is not **effectively active** gets `403` and no token: `account_suspended` when the developer is suspended
(all their games are), `game_suspended` when only that game is. A game is served only when the game and its developer are both `active`
(see the admin endpoints below). The check is on the game, so every key of it is refused, old or new, and all work again at once
when the admin resumes it. The key is checked first: a wrong key still gets 401, so a stranger learns nothing about a game.

### Test tokens without platform

To test social alone, make a token with the same private key:

```bash
go run ./tools/devtoken -player u_42 -name Ali                # player token, valid 1 hour
go run ./tools/devtoken -player u_43 -game <other-game-uuid>  # a player of another game
go run ./tools/devtoken -developer                            # developer token: social must reject it
go run ./tools/devtoken -ttl -1m                              # already expired: social must reject it
```

Send it as `Authorization: Bearer <token>`. `go run ./tools/devtoken -h` lists all options.

### Developer sessions (refresh tokens)

A developer login gives two things:

- a **developer token** (JWT) in the answer's body, valid for `PLATFORM_DEVELOPER_TOKEN_TTL`. The dashboard keeps it in
  memory and sends it as `Authorization: Bearer <token>`;
- a **refresh token** in the cookie `clan_refresh` (HttpOnly, Secure, SameSite=Lax, path `/api/v1/developers`), valid
  for `PLATFORM_DEVELOPER_REFRESH_TTL`. JavaScript cannot read it. It is a random value, not a JWT, and the database
  keeps only its SHA-256 hash (`developer_refresh_tokens`).

When the developer token has expired, or after a page reload, the dashboard calls `POST /api/v1/developers/token/refresh`.
The cookie is the credential. The answer is a new developer token and a new refresh token; the old refresh token stops
working (**rotation**) and the new one runs for the full lifetime again, so a developer who keeps using the dashboard
stays logged in.

A **session** is one login. Its refresh tokens share a `session_id`, which is what makes these possible:

- **Logout** (`POST /api/v1/developers/logout`) deletes that session's tokens. Other logins of the developer stay.
- **A replaced token used again** means two parties hold the same token, so that session is ended and both must log in.
  A token replaced less than 10 seconds ago is still accepted: that is the same refresh arriving twice (a lost answer,
  two tabs starting together), not a copy.
- **A password reset** deletes every session of the developer.

The developer token itself cannot be taken back; it simply expires. That is why it should be short once the dashboard
refreshes it.

The dashboard runs on another origin than platform, so platform's CORS answers allowed origins with
`Access-Control-Allow-Credentials: true`, and the dashboard sends these requests with `credentials: "include"`.
A page on any other origin is refused (403 `origin_not_allowed`), so it cannot refresh or log out for someone.

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_DEVELOPER_REFRESH_TTL` | `168h` | Lifetime of a refresh token: how long a developer stays logged in without using the dashboard. |
| `PLATFORM_COOKIE_SECURE` | `true` | `false` lets the refresh cookie travel over plain http. For `http://localhost` only; docker compose sets `false`. |

### Password reset (developers)

A developer who forgot the password asks for a link on the dashboard's login page:

1. `POST /api/v1/developers/password-reset` with the email. The answer is always 202, so nobody can use it to
   find out who is registered. If the email belongs to an account, platform emails a link:
   `<PLATFORM_DASHBOARD_URL>/reset-password#token=<token>`.
2. The dashboard's reset page sends the token and the new password to `POST /api/v1/developers/password-reset/confirm`.

Rules: the link works **once** and for **30 minutes**; asking again replaces the earlier link; the database keeps only a
SHA-256 hash of the token (`password_reset_tokens`); the new password follows the rules of registration; after a reset,
every developer token issued before it is refused, so sessions that used the old password are logged out
(`developers.password_changed_at`). Limits, counted in platform's memory: 3 requests per email and 20 per IP address
in 15 minutes, then 429 `rate_limited`.

The email goes through an SMTP server with STARTTLS (`pkg/mailer`). **Without `PLATFORM_SMTP_HOST` nothing is sent**:
platform writes the email, with the link, to its log (`email not sent: no mail server is configured`), which is how
you try the flow on your own machine. Never run a public server that way.

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_DASHBOARD_URL` | `http://localhost:5173` | Where the dashboard runs; the start of the reset link. |
| `PLATFORM_SMTP_HOST` | none | SMTP server, for example `smtp.gmail.com`. Empty: emails are logged, not sent. |
| `PLATFORM_SMTP_PORT` | `587` | The STARTTLS port. Port 465 (TLS from the first byte) is not supported. |
| `PLATFORM_SMTP_USERNAME` | none | Login of the SMTP server; for Gmail the full address. |
| `PLATFORM_SMTP_PASSWORD` | none | Its password; for Gmail an **app password** (the account needs 2-Step Verification). Never logged. |
| `PLATFORM_SMTP_FROM` | the username | The sender: `noreply@example.com` or `Name <noreply@example.com>`. |

### Admin accounts (admin panel)

Admins are distributor staff, kept in their own `admins` table and signed in with their own token type (`typ: admin`),
valid for `PLATFORM_ADMIN_TOKEN_TTL` (default 1 hour). Only `/api/v1/admin/` routes accept it, and those routes
refuse developer and player tokens. There is **no register endpoint**: whoever can reach the platform database creates
an admin with the CLI. The password comes from `CREATEADMIN_PASSWORD` or from stdin (never a flag, so it stays out of
the shell history) and is stored as a bcrypt hash:

```bash
export PLATFORM_DATABASE_URL=postgres://platform_user:...@localhost:5432/clan
printf '%s\n' 'a long password' | go run ./tools/createadmin -email ops@example.com -name "Ops One"
```

Then `POST /api/v1/admin/login` returns `{ token, expiresIn }`. A disabled admin (`admins.disabled_at` set) cannot log in
and its tokens stop working at once, not when they expire.

**Postman collection.** `postman/platform-admin-suspension.postman_collection.json` tests the whole admin and suspension feature against a running platform (124 requests, 242 checks):
admin login and token types, the developer list, detail and audit log, suspending and resuming a game and a developer, the 403s on `POST /players/auth` and the public widget config, and the read-only dashboard.
Create an admin as above, set the collection variables `baseUrl`, `adminEmail` and `adminPassword`, then run the whole collection in order in the Runner; it makes its own developers and games, so it can be re-run.

### Widget config validation

`PUT /api/v1/games/{gameID}/widget/draft` and `POST .../widget/publish` check the config against the
config v1 contract (JSON Schema 2020-12, owned by the frontend team: `docs/config-v1` in
`clan-frontend`). The schema is copied to `services/platform/internal/widgetconfig/` together with the
shared fixtures in `testdata/examples/`; when the contract changes, copy the new files over and run
`go test ./services/platform/...`.

`clan.tags` is gone (TEAM-276): clans have no tags. A config that still sends it gets `400 invalid_widget_config`
(`/clan`, `additionalProperties`, "has the unknown field \"tags\""). Migration `000011_remove_clan_tags` removed it from
every stored draft and published version first, so they can still be published and rolled back; that is the only time a
published version was changed, and nothing else in it (or its version number) moved. Studio must stop sending the field
before this is deployed, and the copies in `clan-frontend` `docs/config-v1` change in the same release.

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
| rabbitmq-setup | none: declares the RabbitMQ exchanges, queues, bindings and policies, then exits (see "RabbitMQ events") |
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
| `realtime.events` | `config.published`, `friend.*`, `player.*`, `clan.*`, `game.*` |
| `realtime.events.dead` | `realtime.events`, on the dead-letter exchange `socialkit.dead` (direct) |

**Dead-letter queue.** An event Realtime rejects (unusable, or failed twice) is not deleted: the policy
`realtime-events-dead-letter` on `realtime.events` makes RabbitMQ move it to `socialkit.dead`, which routes it to
`realtime.events.dead`. There it keeps its body, `message_id` and `type`, plus an `x-death` header with the original routing
key, the reason and how often. The policy `realtime-events-dead-ttl` drops dead events after 14 days. Look at them in the
management UI under **Queues > realtime.events.dead > Get messages**. Every rejection is logged once as
`event rejected to the dead-letter queue`, with `event_id`, `type`, `routing_key`, `reason` (`unusable` or `failed_twice`)
and `error`: search the log for a dead event's `message_id` to find why it was rejected. While the consumer is connected it
counts the DLQ at connect and every `REALTIME_DLQ_CHECK_INTERVAL` (5 min), and logs `dead-letter queue holds rejected
events` with the count while it is not empty.

**Replaying dead events.** Once the cause is fixed, send them back with `tools/dlq` (the broker address comes from `-url`, or
`REALTIME_RABBITMQ_URL`; the password is never printed):

```bash
go run ./tools/dlq list                       # what is in realtime.events.dead; nothing is removed
go run ./tools/dlq replay -id <event_id>,...   # send these back to realtime.events
go run ./tools/dlq replay                     # send all of them back
```

Each event goes back to the queue it was dead-lettered from and leaves the DLQ only after the broker confirmed the copy, so a
failure halfway loses nothing. Replaying is safe: Realtime skips an event it already handled, and an event that is still
unusable goes back to the DLQ. Details: `docs/events.md` section 9. The settings are policies, not queue arguments, so an
existing `realtime.events` gets them by running `rabbitmq-setup` again, with its messages kept.

The queue exists before the Realtime service does, so events published today wait in it instead of being lost.
Open http://localhost:15672, go to **Queues**, and watch `realtime.events` count them.

- The job is safe to repeat: importing definitions adds what is missing, updates the policies and never deletes messages.
  To re-apply it after editing `definitions.json`: `docker compose run --rm rabbitmq-setup`.
- A new queue or binding goes into `definitions.json` in the same commit as the code that needs it.
  Copy the file to `deploy/helm/clan/files/definitions.json` too (`TestTheHelmChartHasTheSameDefinitions` fails otherwise),
  and put queue settings in a policy, not in queue arguments, so existing environments can take them.
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
| `player.blocked`, `player.unblocked` | social, `POST` / `DELETE /api/v1/players/{playerID}/block` (204), only when a block was really added or removed | `{ "blocker_id", "blocked_id" }`. A block also publishes `friend.removed` (`removed_by` = the blocker) when the two were friends, and `friend.request_cancelled` or `friend.request_declined` when one had a pending request to the other |
| `config.published` | platform, `POST /api/v1/games/{gameID}/widget/publish` and `.../widget/versions/{version}/rollback` (200); realtime then sends `config.updated` to the game's connections | `{ "version" }`, the new version number (a rollback saves a new version, so it is that number) |
| `game.suspended`, `game.resumed` | platform, `PUT /api/v1/admin/games/{gameID}/status` and `PUT /api/v1/admin/developers/{developerID}/status` (200), after the change is saved and only when it changed whether the game is served. A developer-level change publishes one event for each game of theirs that is on by its own switch; a game suspended on its own is skipped, because it stays off when the developer is resumed. A game-level change while its developer is suspended publishes nothing. Realtime closes every connection of the game with `4403` `game_suspended` on `game.suspended` and accepts the game again after `game.resumed` (TEAM-208) | `{ "status" }`: `suspended` or `active`, the same word as `status` in `GET /internal/v1/games/{gameID}/config`. Never the reason or the admin's note |

`id` is always the social service's player ID (a UUID, as in `conversation_members.player_id` in Realtime), and
`name` the display name at the time of the event.

**What realtime does with the friend, player and clan events** (`player.blocked` and `player.unblocked` are kept as a copy of the blocks, see `docs/events.md`; `clan.renamed` and `clan.member_role_changed` are acknowledged and need nothing, because realtime stores no clan names or roles; any other type is acknowledged and ignored):

| Event | Realtime does |
|---|---|
| `friend.request_sent` | sends a `friend_request` notification to the `receiver` (nothing is stored). `request_id` goes into the notification as `requestId`, so the SDK can accept that very request. It is optional here only so that an older event without it is still shown (`requestId: null`). |
| `friend.request_cancelled` | sends a `friend_request_cancelled` notification to the `receiver` (all open tabs) so the SDK removes the `friend_request` with the same `requestId`. `request_id` is required. The sender is not told. |
| `friend.request_declined` | sends a `friend_request_declined` notification to the `receiver` only (their other tabs drop the `friend_request`). `request_id` is required. The sender is **not** told. |
| `friend.accepted` | creates the DM conversation of the two `players` and both memberships (or reuses it), sends `conversation.added` and a `friend_accepted` notification to both. Exactly two players are required. The notification carries `requestId` (`null` when the event has no `request_id`). |
| `friend.removed` | deletes both memberships (the messages stay) and sends `conversation.removed` to both, with no notification. Exactly two `players` are required and `removed_by` must be one of them. |
| `player.renamed` | stores the new name with the player's memberships in that game. No frame is sent; messages keep the name their sender had when writing them. |
| `clan.created` | creates the clan's chat room (a conversation of `kind` `clan` with the clan's `clanId`) with the owner (`player`) as its first member, and sends `conversation.added` to the owner. |
| `clan.member_joined` | adds the `player` to the clan's room (and creates the room if `clan.created` was never seen), sends `conversation.added` with the room to the player and a `clan_joined` notification to the other members. The room comes with its **whole history**, also the messages written before the player joined or while they were away; their unread count starts from the start of the history. |
| `clan.member_left` | removes the `player` from the room (the messages stay for the others, and for the player if they join again) and sends `conversation.removed` to that player; when `reason` is `kicked`, also a `clan_kicked` notification. The members who stay are not told. |
| `clan.disbanded` | deletes the room with its members and messages, and sends `conversation.removed` and a `clan_disbanded` notification to everybody who was still in it (Social publishes only this event when the last member leaves). |

A field Realtime needs that is missing or malformed makes it reject that event to the dead-letter queue (logged as `event rejected to the dead-letter queue` with `reason` `unusable`).
So **Social and Realtime must keep these field names in step**; `TestDispatchReadsEventsAsSocialPublishesThem` in
`services/realtime/internal/consumer` checks the shape Social publishes today, and
`TestDispatchReadsEveryMessageInTheEventMessagesFile` feeds every message of `docs/event-messages.md` to the dispatcher.

**How realtime reads the queue**

- One consumer, one event at a time, in order. Prefetch is 20, acknowledgement is manual and comes after the database write.
- An event that fails (for example the database is down) is put back **once** after one second; if it fails again it is logged
  and rejected. An unusable event (not JSON, missing field, id that is not a UUID) is logged and rejected at once.
  A rejected event is not lost: the broker moves it to the dead-letter queue `realtime.events.dead`, where it is kept 14 days.
- Delivery is at least once, so the same event can arrive twice. Realtime records the `event_id` of every friend, player
  and clan event in `processed_events` (migration `000005`), in the same transaction as the event's writes, and acknowledges
  a copy without storing or sending anything (debug log `skipping an event handled before`). A failed write rolls the record
  back too, so the retry handles the event. The records are deleted after `REALTIME_PROCESSED_EVENTS_RETENTION`. Details: `docs/events.md` section 9.
- If the broker is down or restarts, realtime keeps running and reconnects (1 s, 2 s, 4 s ... up to 30 s).
  Without a database it does not read events at all, because it would have nowhere to store them.

### Publishing from a service

- **Social and platform** write every event to their own `outbox` table in the transaction of the change (see "Event
  outbox" below), and each service's outbox relay publishes them. A request never talks to RabbitMQ, and an event is
  never lost because the broker was down: it waits in the outbox and goes out once the broker is back.
- In the service layer a change and its events go through `change`: the store call and `AddEvents` run inside `InTx`.
  Service tests read the events a fake store committed; repository tests check that an event rolls back with its change.
- **Delivery is at least once.** An event can arrive twice (the relay stopped after the broker confirmed it but before it
  marked it sent, or RabbitMQ redelivered it). Consumers must be idempotent; Realtime skips a copy by its `event_id`.
- Without `<SERVICE>_RABBITMQ_URL` a service starts anyway, logs `..._RABBITMQ_URL is not set`, and its relay drops the events.
- The publisher (`events.NewRabbitMQ`) connects on the first event, not at startup, and reconnects by itself. After a
  failed connection it waits 5 s before trying again. Every publish waits for the broker to confirm it (up to 5 s).

**The consumer side.** An event Realtime rejects (unusable, or failed twice) waits in the dead-letter queue
`realtime.events.dead` for 14 days instead of being deleted (see "RabbitMQ events"); `go run ./tools/dlq replay` sends it
back once the cause is fixed.

### Event outbox

`pkg/events` has a transactional outbox, and platform and social each have an `outbox` table (social migration `000008`,
platform `000010`; the two files are identical, `TestOutboxMigrationsAreTheSameInEveryService` checks it).

- **Writing.** `events.Enqueue(ctx, tx, e)` inserts the event into the `outbox` table inside the caller's open `pgx.Tx`,
  so the event commits or rolls back together with the change it describes. It takes a transaction, never the pool.
- **Sending.** Each service runs one relay goroutine (`events.Relay`, started in `cmd/relay.go`) when it has a database.
  It claims up to 100 unsent rows, oldest first (`FOR UPDATE SKIP LOCKED`, so two copies of a service never send the same
  row), publishes them one by one and marks a row sent only after the broker has confirmed it. It stops a batch at the
  first failure, so a later event never overtakes an earlier one, and stores that row's `attempts` and `last_error`.
- **Broker down.** Rows wait in the table; the relay tries again after `<SERVICE>_OUTBOX_INTERVAL`, doubling up to 30 s,
  and sends them once RabbitMQ is back. Requests only insert a row, so they never wait for the broker.
- **No broker URL.** The relay still runs with the no-op publisher: rows are marked sent and dropped, as events are today.
- **Cleanup.** Sent rows older than `<SERVICE>_OUTBOX_RETENTION` (7 days) are deleted, checked once an hour.
- **Shutdown.** The relay stops with the service and records the batch it was sending first (up to 5 s).
- **Order.** Rows go out in `id` order, which is the order they were inserted. Two transactions that run at the same time
  can commit in the other order, so only events written one after the other are guaranteed to stay in order; events
  about the same thing are, when the service writes them inside the transaction that holds the row lock on it (a friend pair, a clan).
- **At least once.** If the relay stops after the broker confirmed a row but before the row was marked sent, the row is
  sent again with the same `event_id`. Consumers must handle a repeated event.
- **A row that always fails** blocks the rows after it (that is what keeps the order). Its `last_error` says why; look for
  `outbox relay could not send` in the log.

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

## Container images

Kubernetes pulls the services from this project's GitLab container registry. CI builds them (job `images` in
`.gitlab-ci.yml`), for both `linux/amd64` and `linux/arm64`; the Oracle server is ARM.

| Image | Built from | What it is |
|---|---|---|
| `registry.gitlab.com/clan-platform/clan-backend/platform` | `services/platform/Dockerfile` | the platform service |
| `.../social`, `.../realtime` | `services/social/Dockerfile`, `services/realtime/Dockerfile` | the same for social and realtime |
| `.../platform-migrate` | `deploy/migrate/Dockerfile` with `SERVICE=platform` | `migrate/migrate` plus `services/platform/migrations`; Kubernetes runs it before the service, like `platform-migrate` in docker-compose |
| `.../social-migrate`, `.../realtime-migrate` | the same with `SERVICE=social` / `SERVICE=realtime` | the migrations of social and realtime |

**When CI builds them:** after the tests pass, on every push to `develop` and `main`. On a merge request the job only
starts when you press ▶ in the pipeline (to try a branch's images before merging).

**Tags:** every image gets the short commit SHA (`social:a1b2c3d`, a fixed version to deploy and roll back to) and
the branch name (`social:develop`, the newest build of that branch). Builds from `main` also get `latest`.

**How the ARM build works:** the build stage of each service Dockerfile runs on the build machine's own CPU
(`FROM --platform=$BUILDPLATFORM`) and Go cross-compiles with `GOOS=$TARGETOS GOARCH=$TARGETARCH`, so no ARM emulation
is needed. The runtime stage is distroless, which has an image for each CPU.

**Build one yourself** (from the repository root; the result stays on your computer):

```bash
# for your own computer, like docker compose does
docker build -f services/social/Dockerfile -t social:dev .
# for the ARM server (Docker Desktop has buildx)
docker buildx build --platform linux/arm64 -f services/social/Dockerfile -t social:arm64-test .
docker buildx build --platform linux/arm64 -f deploy/migrate/Dockerfile --build-arg SERVICE=social -t social-migrate:arm64-test .
docker image inspect social:arm64-test --format "{{.Architecture}}"   # arm64
```

An ARM image cannot run on an Intel or AMD computer; Docker Desktop marks it with an ARM64 warning.

docker-compose does not use these images: it builds the services itself and mounts the migrations folders.

## Telemetry (OpenTelemetry)

`pkg/otelx` builds the tracer and meter providers for all three services, so each `main.go` has one line:
`defer otelx.Setup(ctx, serviceName, log)()`.

- **Off by default.** Nothing happens unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set, so `go run`, `docker compose` and
  `go test` start no exporter and never look for a collector.
- **On:** set `OTEL_EXPORTER_OTLP_ENDPOINT` to the collector's OTLP gRPC address (for example `http://localhost:4317`).
  Traces and metrics are sent there. A collector that is down is logged as a warning and never fails a request or
  the start of a service.
- **What every span and metric carries:** `service.name` (`platform`, `social` or `realtime`), `service.version` (the
  commit the image was built from: the `VERSION` build argument of the Dockerfiles, which CI sets; `dev` otherwise)
  and `deployment.environment` (`DEPLOYMENT_ENVIRONMENT`, `local` when unset).
- **Shutdown:** on SIGTERM or Ctrl+C the service stops as before, then sends what is still waiting, for at most
  5 seconds.
- **On the server** the chart sets the variables when `telemetry.enabled` is true (see `deploy/helm/clan/values.yaml`).

### What is traced

With telemetry on, one request is one trace across the services:

- **HTTP in** (`httpx.Trace()`, first in `httpx.Chain`): a span per request, named after the route pattern
  (`POST /api/v1/clans`, `GET /api/v1/clans/{clanID}`), never the raw path. A request that carries a `traceparent`
  header continues that trace. `/health`, `/ready` and WebSocket upgrades are not traced.
- **HTTP out** (`httpx.Transport()` on the services' own HTTP clients): a span per call, and the trace goes on to the
  service that is called.
- **Postgres** (`pkg/pg`): a span per query run for a traced request, with the SQL text (values are parameters and
  are never recorded). Queries of background loops (the outbox relay, the cleanups) are not traced.
- **RabbitMQ** (`pkg/events`): `Enqueue` stores the request's trace context with the outbox row, the relay publishes
  the event in a `<type> publish` span and writes the context to the message headers, and realtime handles it in a
  `<type> process` span. So a `clan.created` from social links to what realtime does with it.
- **Logs** (`pkg/logx`): a line logged with a context inside a trace (`log.InfoContext(ctx, ...)`) gets `trace_id`
  and `span_id`. The request line and the "request failed" and panic lines do; a line logged with plain
  `log.Info(...)` does not, so pass the context when a new log line should link to its trace.

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
`config.updated`). Then clan chat: A creates a clan and gets its room; B joins (B gets the room, A gets `clan_joined`);
A writes in the room (both get `message.new`); B leaves (`conversation.removed`) and does not get the next message; B
joins again and gets the room with the whole history, the missed message included; A kicks B (`conversation.removed`
and `clan_kicked`); A, the last member, leaves (`conversation.removed` and `clan_disbanded`). Every frame must arrive
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

## Endpoints so far

**Suspended developers are read-only.** While an admin has the developer's account suspended, login and every `GET` still work (so the dashboard can show why),
but everything that changes data answers `403 account_suspended` before the body is read: register a game, create an API key, save allowed origins,
save a widget draft, publish and roll back. Revoking an API key stays allowed (it only takes access away, so a leaked key can still be killed).
Resuming lets writes through again at once. The check is `requireWritable` in `internal/handler`; `TestWritesRefuseASuspendedDeveloper` fails for a new write route that does not use it.
Only the developer's own switch counts here: a game suspended on its own does not stop its developer from editing.

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| POST | `/api/v1/developers/register` | 201 with the new developer; 400 invalid email, password or name; 409 `email_taken` |
| POST | `/api/v1/developers/login` | 200 `{ token, expiresIn }`; 401 `invalid_credentials` |
| POST | `/api/v1/developers/token/refresh` | 200 `{ token, expiresIn }` and a new refresh cookie, for the `clan_refresh` cookie that login set; the old refresh token stops working. 401 `invalid_refresh_token` when the session is over (no cookie, expired, logged out, password reset, or a replaced token used again, which ends that session) |
| POST | `/api/v1/developers/logout` | 204 always; ends the session of the refresh cookie and clears the cookie. Other logins of the developer stay |
| POST | `/api/v1/developers/password-reset` | 202 always, for a known and an unknown email alike; emails a reset link to a known one (see "Password reset"). 400 `invalid_email`; 429 `rate_limited` |
| POST | `/api/v1/developers/password-reset/confirm` | 204 and the password is changed, with `{ token, password }`; 400 `invalid_reset_token` (unknown, used or expired) or `invalid_password`; 429 `rate_limited` |
| GET | `/api/v1/developers/me` | 200 with the developer, including `status` (`active` / `suspended`) and `statusReason` (`non_payment` / `abuse` / `other`, else `null`) so the dashboard can explain a suspension; never the admin's internal note. 401 unless `Authorization: Bearer <developer token>` |
| POST | `/api/v1/admin/login` | 200 `{ token, expiresIn }` with an **admin** token; 401 `invalid_credentials` for an unknown email, a wrong password and a disabled admin alike |
| GET | `/api/v1/admin/me` | 200 with the admin; 401 unless `Authorization: Bearer <admin token>` of an admin who is not disabled (developer and player tokens get 401) |
| GET | `/api/v1/admin/developers` | **Admin token only.** 200 `{ developers: [{ id, email, name, status, gameCount, createdAt }], nextCursor }`, newest first. Query: `q` (part of email or name), `status` (`active` / `suspended`), `limit` (1 to 100, default 25), `cursor` (the previous `nextCursor`; `null` on the last page). 400 `invalid_query` / `invalid_status` / `invalid_limit` / `invalid_cursor` |
| GET | `/api/v1/admin/developers/{developerID}` | **Admin token only.** 200 with the developer (`status`, `statusReason`, `statusChangedAt`) and all their `games`, each with the same three fields. 404 `developer_not_found` (also for a malformed ID) |
| PUT | `/api/v1/admin/developers/{developerID}/status` | **Admin token only.** Body `{ status, reason, note }`: `status` is `active` or `suspended`; `reason` (`non_payment`, `abuse`, `other`) is **required when suspending** (400 `reason_required`; `invalid_reason`, `invalid_status`, `invalid_note` for bad values); `note` (up to 1000 characters) is internal and lives in the audit log only. 200 with the developer and their games, as `GET .../developers/{developerID}`. Suspending a developer turns off all their games without changing the game rows. The status and its audit row are written in one transaction. Setting the status the developer already has changes nothing and writes no audit row. Resuming needs no reason (the audit row then records the reason of the suspension it ends). 404 `developer_not_found` |
| PUT | `/api/v1/admin/games/{gameID}/status` | **Admin token only.** Same body and rules for one game; 200 with the game (`status`, `statusReason`, `statusChangedAt`). 404 `game_not_found` |
| GET | `/api/v1/admin/audit-log` | **Admin token only.** 200 `{ entries: [...], nextCursor }`, newest first; `targetId` limits it to one developer or game; `limit` and `cursor` as above. `note` is internal and is never shown to developers. 400 `invalid_target_id` / `invalid_limit` / `invalid_cursor` |
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
| GET | `/api/v1/public/games/{gameID}/widget-config` | **No login, open to every website.** 200 `{ version, config }`: the latest **published** version (never a draft), with a strong `ETag` (`"v3"`) and `Cache-Control: public, max-age=60`. Send `If-None-Match` to get 304. 404 `widget_not_published` (also for an unknown game ID). **403 `game_suspended`** (with `Cache-Control: no-store`) when the game or its developer is suspended, whichever it is, so players never see a billing message; the SDK then removes the panel. Not rate-limited in the service: put a CDN or proxy limit in front of `/api/v1/public/` |
| GET | `/internal/v1/games/{gameID}/config` | **For other services, not browsers.** Needs `X-Internal-Token: <PLATFORM_INTERNAL_TOKEN>` (401 `invalid_internal_token`; 404 `not_found` while the secret is not set). 200 `{ gameId, version, status, config, allowedOrigins }`: the newest **published** config, the game's allowed origins and its **effective** `status` (`suspended` when the game or its developer is, else `active`; a suspended game still gets its config), never cached. A game never published has `version: 0` and `config: null` but still its origins. 404 `game_not_found` for an unknown game |
| any | anything else | 404 `not_found` in the standard error format |
| any | from a website not in the allowed origins | 403 `origin_not_allowed` |

### Social service (port 8082)

Every `/api/v1/` route needs `Authorization: Bearer <player token>`. The game and the player
always come from the token, never from the URL or the body.

**Suspended games are refused.** While an admin has the game, or its developer, switched off, every `/api/v1/` route answers
`403 game_suspended` (checked after the token, so an invalid token still gets 401; `/health` and `/ready` are not affected).
Social learns it from the call it already makes for the clan rules: the platform's public widget config answers 403
`game_suspended` for a suspended game. The answer is kept for `SOCIAL_CONFIG_CACHE_TTL`, so a suspension and a resume both
take effect within that time. Nothing is changed or deleted: friends, clans, blocks and players stay as they were. If
the platform cannot be reached, Social keeps the last status it saw, and a game it has never seen is served (fail open).

**Deleted players are refused.** After `DELETE /api/v1/players/me`, every token of that player issued before the call (or
in the same second, as `iat` has whole seconds) answers `401 player_deleted` on every `/api/v1/` route, although it has not
expired. A token for the same `externalPlayerId` issued later starts a new, empty player (TEAM-249). If the database cannot
be asked, the request goes on (fail open) and fails on its own.

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/players/me` | 200 with the calling player; the record is created on the first call |
| DELETE | `/api/v1/players/me` | The game deleted the player's account: the game's server gets a token for the player as usual and calls this. 204, no body. In one transaction the player leaves their clan (as with `/leave`), and their friendships, pending friend requests and blocks are removed; they are renamed `Deleted player` and never found by the player search again. Their older tokens then get 401 `player_deleted` (see above). A player Social never saw: 204. Publishes `clan.member_left` (or `clan.disbanded`), `friend.removed`, `friend.request_cancelled` / `friend.request_declined`, `player.unblocked`, then `player.renamed` with the new name: the existing types, so Realtime needs no change |
| GET | `/api/v1/players?q=&limit=` | 200 `{ players: [...] }`: players of the caller's game whose name contains `q` (ignoring upper/lower case; `%` and `_` are normal characters), sorted by name, never the caller and never a deleted player. Each has `id`, `displayName`, `relation` (`none` / `friend` / `request_sent` / `request_received` / `blocked`, a player the caller blocked) and, for the two pending ones, `requestId`. Players who blocked the caller are left out. `q` is required: 400 `invalid_query` (missing, empty or over 50 characters); `limit` 1 to 100, default 100 (400 `invalid_limit`) |
| GET | `/api/v1/players/me/clan` | 200 `{ clan, role }` with the caller's clan and role (`owner` / `member`); 404 `not_in_clan` |
| GET | `/api/v1/players/me/blocks` | 200 `{ blocks: [...] }` with `id`, `displayName`, `since` (when the caller blocked them), newest first |
| POST | `/api/v1/players/{playerID}/block` | no body. 204: the caller blocked that player (see "Blocks" below); blocking again changes nothing (204, no event). 400 `cannot_block_self`; 404 `player_not_found` (also for another game's player). Publishes `player.blocked` |
| DELETE | `/api/v1/players/{playerID}/block` | 204: the block is removed; a friendship or request it ended does not come back. Not blocked: 204, no event. 400 `cannot_block_self`; 404 `player_not_found`. Publishes `player.unblocked` |
| GET | `/api/v1/clans?q=&limit=&sort=&hasSpace=` | 200 `{ clans: [...] }` of the caller's game, by default sorted by name, at most 100. `q` keeps clans whose name contains it (ignoring upper/lower case, over 50 characters gives 400 `invalid_query`) and searches **all** clans, full ones too. `limit` 1 to 100 (400 `invalid_limit`); `sort` `name` or `members`, biggest first (400 `invalid_sort`); `hasSpace=true` leaves out full clans (400 `invalid_has_space`). Featured clans = `?hasSpace=true&sort=members&limit=3` |
| POST | `/api/v1/clans` | body `{ "name": "Wolves" }`. 201 with the new clan, the caller becomes its owner. 400 `invalid_json` / `invalid_clan_name` (3 to 20 characters); 409 `clan_name_taken` / `already_in_clan`. Clans have no tags (TEAM-277): a `tag` in the body, which older SDKs send, is ignored, and no clan answer has one |
| PATCH | `/api/v1/clans/{clanID}` | body `{ "name": "Grey Wolves" }`. 200 with the clan. Only the **owner**; the name is checked like on create. The same name changes nothing (200, no event); a change of upper/lower case only is a rename. 400 `invalid_json` / `invalid_clan_name`; 403 `not_clan_owner`; 404 `clan_not_found`; 409 `clan_name_taken`. Publishes `clan.renamed` |
| POST | `/api/v1/clans/{clanID}/join` | no body. 200 with the clan and its new `memberCount`, the caller joins as a member. 404 `clan_not_found` (also for another game's clan); 409 `already_in_clan` / `clan_full`: the clan has as many members as the game allows (`clan.maxMembers` from the published config, 10 before the first publish). Lowering the limit removes nobody; a clan over it just takes no one new |
| POST | `/api/v1/clans/{clanID}/leave` | no body. 204: the caller left the clan and can join another one. A normal member is just removed. If the **owner** leaves and others remain, the member who joined first becomes the owner; if the owner is the only member, the clan is deleted. 404 `clan_not_found` (also for another game's clan); 409 `not_in_clan` (the caller is not a member of this clan) |
| GET | `/api/v1/clans/{clanID}/members` | 200 `{ members: [...] }` with `playerId`, `displayName`, `role`, `joinedAt`; owner first, then by join time. `role` is a role id of the game's `clan.roles` (see "Clan roles" below). 404 `clan_not_found` (also for another game's clan) |
| DELETE | `/api/v1/clans/{clanID}/members/{playerID}` | no body. 204: the caller removed that member (`playerId` from the member list): their role needs `canKick` and the member must have a lower role (the owner may remove anyone). The player can join a clan again at once. 403 `not_allowed`; 400 `cannot_kick_self` (leave instead); 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_left` with `reason` `kicked` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/promote` | no body. 200 `{ playerId, displayName, role }`: the member is one role up. The caller's role needs `canPromote`, the member must be lower, and the new role stays below the caller's own (nobody becomes owner). 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/demote` | no body. 200 `{ playerId, displayName, role }`: one role down, same rules. 409 `already_lowest_role`; 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| GET | `/api/v1/friends` | 200 `{ friends: [...] }` with `id`, `displayName`, `since` and `clan`, sorted by name. `clan` is the friend's clan in the same game now (`{ id, name }`), or `null` when they are in none (also after leaving, a kick or a disband); it comes from the same query, so there is no request per friend. Accept and a request sent back return the friend without `clan` |
| GET | `/api/v1/friends/requests` | 200 `{ incoming: [...], outgoing: [...] }`, newest first; each has `id`, `player` (the other player: `id`, `displayName`) and `createdAt` |
| POST | `/api/v1/friends/requests` | body `{ "playerId": "<social player ID>" }`. 201 with the new request. If that player had already asked the caller, it is accepted instead: 200 with the new friend. 400 `invalid_json` / `invalid_player_id` / `cannot_friend_yourself`; 404 `player_not_found` (also for another game's player, or a player who blocked the caller); 409 `already_friends` / `friend_request_exists` / `player_blocked` (the caller blocked that player). Publishes `friend.request_sent` or `friend.accepted` |
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

**Blocks.** A player can block another player of the same game (table `blocks`, migration `000006`). Blocking is
one-way and the blocked player is not told:

- In one transaction, under the same pair lock as every friend change, the block ends the two players' friendship and
  their pending friend request in either direction. Social publishes `player.blocked` and then `friend.removed`,
  `friend.request_cancelled` (the blocker had asked) or `friend.request_declined` (the blocked player had asked).
- The blocked player's friend requests to the blocker answer 404 `player_not_found`, and the blocker is left out of
  their search, so the block stays hidden. The blocker's own request answers 409 `player_blocked` until they unblock.
- Realtime keeps its own copy of the blocks from the two events and refuses direct messages between the pair.
- Unblocking brings nothing back: the two have to become friends again.

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
- Blocks and clans are **not** backfilled yet: social has no internal endpoint that lists blocks, and realtime has no clan chat.
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
| GET | `/api/v1/ws?token=<player token>` | WebSocket upgrade (101). Checked **before** the upgrade: 401 `missing_token` / `invalid_token` / `token_expired` / `wrong_token_type` for the token, 403 `origin_not_allowed` when the page's `Origin` is not in `REALTIME_CORS_ORIGINS` (requests without `Origin`, like Postman's, are allowed), 503 `social_unavailable` when the social service cannot be reached (realtime asks social for the player's own ID with `GET /api/v1/players/me` and the player's token, so **social must be running**), 503 `shutting_down`. After the upgrade the server first sends a `hello` frame with the player's social ID; a `ping` frame is answered with `pong`; a frame over 8 KB closes the connection (1009); a bad frame gets an `error` frame with `bad_frame` and the connection stays; the connection is closed with 4001 when the token expires and with 1001 when the service stops; a client that does not answer the server's pings (every 25 s) for 60 s is dropped; a client that does not read its frames (64 waiting) is closed with 4002 without holding anyone else up; a **suspended game** (an admin switched off the game or its developer) gets 4403 `game_suspended` instead of `hello`, and every open connection of that game is closed with it when the `game.suspended` event arrives, within seconds (other games are not touched; if platform cannot be reached the last status seen is kept, and a game never seen is served). Public URL through Traefik: `/realtime/v1/ws` |
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
**Message rules.** `message.send` is checked in this order, and the first failure answers (full table in `openapi/realtime-ws.md`): rate limit of the connection (5 in 5 s; three refused in a row close it with `1008`), not empty, member, the game's chat switches (`chat.enabled`, `directMessages`, `clanChat`), not blocked (DMs), `chat.maxMessageLength` in characters, slow mode (`chat.slowModeSeconds` per player across all tabs), then the profanity filter (`chat.profanityFilter`, whole words, replaced with `***`; the list is `services/realtime/internal/profanity/words.txt`). The game's rules come from its published config (kept in memory, updated on `config.published`); a game without a published config has the permissive defaults. Blocks come from the `player.blocked` and `player.unblocked` events into Realtime's own `blocks` table (Social publishes them on block and unblock).
Presence: a player is online while one of their WebSockets is open. When the last one closes, realtime waits 10 s; if the player did not connect again, the friends who are connected get `presence.update { playerId, status: "offline" }`, so a page refresh never shows as offline. Connecting sends friends nothing (they read `hello.onlineFriends`), and a new friendship between two online players gives each a `presence.update` `online` for the other. The 10 s timers are in memory of the one process: if realtime restarts during them, nobody is told that player went offline.
Slow mode and the rate limit live in the memory of the one Realtime process (a restart forgets them).

**Clan chat.** Realtime makes each clan's chat room from Social's clan events, so creating or joining a clan with a player token
(`POST http://localhost:8082/api/v1/clans` with `{"name":"Wolves"}`, `POST http://localhost:8082/api/v1/clans/<clan id>/join`) is all it
takes: that player's tab gets `conversation.added` with `kind: "clan"` and the `clanId`, and the members already in the clan get a
`clan_joined` notification. Send `message.send` with the room's conversation id exactly as in a DM; every member gets `message.new`.
Blocks do not apply in a clan room, and `chat.clanChat: false` in the published config answers `feature_disabled`. Leaving or being
kicked gives `conversation.removed` (and `clan_kicked`); joining again brings the room back with its whole history. In a clan room,
`online` in `members` is the state when the room was sent; live `presence.update` frames go to friends only.

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

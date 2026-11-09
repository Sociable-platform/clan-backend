# clan-backend

Backend for the clan platform: three Go services in one repository (realtime is a skeleton so far).

| Service | Owns | Port | Database schema |
|---|---|---|---|
| `services/platform` | Developers, registered games, API keys, player tokens | 8081 | `platform` |
| `services/social` | Players, clans, clan members | 8082 | `social` |
| `services/realtime` | WebSocket connections, chat, presence (skeleton: only `/health` and `/ready` so far) | 8083 | `realtime` |

## Layout

```
clan-backend/
├── go.mod                     # one Go module for the whole repo
├── docker-compose.yml         # postgres + rabbitmq + platform + social + realtime + traefik
├── deploy/postgres/init/      # creates the schemas and database users on first start
├── deploy/traefik/            # gateway: static config and the /realtime route
├── deploy/rabbitmq/           # definitions.json: exchange, queues and bindings, applied by rabbitmq-setup
├── openapi/                   # API contracts: platform.yaml, social.yaml, realtime.yaml, realtime-ws.md
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
    │   ├── migrations/        # SQL for tables in the platform schema
    │   └── Dockerfile
    ├── social/                # same structure
    └── realtime/              # same structure, no migrations yet
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
| `SOCIAL_JWT_PUBLIC_KEY_FILE` | none (required) | Platform's public key, used by social to check player tokens. Docker Compose sets it to the mounted `secrets/platform_jwt_public.pem`. |
| `REALTIME_JWT_PUBLIC_KEY_FILE` | none (required) | The same public key, for realtime. Only the public key is mounted into the realtime container. |

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
| platform | http://localhost:8081/health, http://localhost:8081/ready |
| social | http://localhost:8082/health, http://localhost:8082/ready |
| realtime | http://localhost:8083/health, http://localhost:8083/ready |
| traefik | gateway on `localhost:80` (change with `TRAEFIK_HOST_PORT`): http://localhost/realtime/v1/healthz (see "Gateway (Traefik)") |

`platform` starts only after `platform-migrate` finished successfully.

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
| `/realtime/v1/<rest>` (conversations, messages, `ws`) | `/api/v1/<rest>` |

- Check it: `curl http://localhost/realtime/v1/healthz` returns 200 `{"service":"realtime","status":"ok"}`.
- The entrypoint timeouts in `deploy/traefik/traefik.yml` are above 60 s so an idle WebSocket is not cut by the gateway.
- There is no access log on purpose: the WebSocket URL carries the player token in its query string.
- Port 80 busy on your computer? Set `TRAEFIK_HOST_PORT` in `.env` and use that port in the URLs.

## RabbitMQ events

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

## Run with go run

Start Postgres and apply the migrations in Docker, then run a service from your editor or terminal:

```bash
docker compose up -d postgres
docker compose run --rm platform-migrate     # safe to repeat
docker compose run --rm social-migrate   # creates or updates the social tables
go run ./services/platform/cmd      # uses PLATFORM_DATABASE_URL
go run ./services/social/cmd        # uses SOCIAL_DATABASE_URL
go run ./services/realtime/cmd      # uses REALTIME_DATABASE_URL
```

Set the `*_DATABASE_URL` variables from `.env.example` in your terminal or GoLand run configuration.
Without a database URL the services still start; `/health` answers 200 and `/ready` answers 503.

## Database migrations

Table definitions live in `services/<name>/migrations/` as `NNNNNN_name.up.sql` / `NNNNNN_name.down.sql`.
`docker compose up` applies the pending ones automatically: the `platform-migrate` job
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
  (`$URL` is the `-database` value from `docker-compose.yml`). On a local database it is usually quicker to run
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
```

### Integration tests (real database)

Some repository tests talk to a real Postgres and are **skipped** unless a test database URL is set.
With the Docker stack running (`docker compose up -d`, so the migrations are applied):

```bash
# PowerShell
$env:SOCIAL_TEST_DATABASE_URL="postgres://social_user:<SOCIAL_DB_PASSWORD>@localhost:5432/clan?sslmode=disable"
go test ./services/social/internal/repository/ -v
```

Each run uses new random game IDs and deletes its rows afterwards, so it does not disturb other data.

## Endpoints so far

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| POST | `/api/v1/developers/register` | 201 with the new developer; 400 invalid email, password or name; 409 `email_taken` |
| POST | `/api/v1/developers/login` | 200 `{ token, expiresIn }`; 401 `invalid_credentials` |
| GET | `/api/v1/developers/me` | 200 with the developer; 401 unless `Authorization: Bearer <developer token>` |
| GET | `/api/v1/games` | 200 `{ games: [...] }` with only the logged-in developer's games, newest first; 401 without a developer token |
| POST | `/api/v1/games` | 201 with the registered game (registers an existing game, never creates one); 400 `invalid_name` / `invalid_description`; 401 |
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
| any | anything else | 404 `not_found` in the standard error format |
| any | from a website not in the allowed origins | 403 `origin_not_allowed` |

### Social service (port 8082)

Every `/api/v1/` route needs `Authorization: Bearer <player token>`. The game and the player
always come from the token, never from the URL or the body.

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/players/me` | 200 with the calling player; the record is created on the first call |
| GET | `/api/v1/players/me/clan` | 200 `{ clan, role }` with the caller's clan and role (`owner` / `member`); 404 `not_in_clan` |
| GET | `/api/v1/clans?q=&limit=&sort=&hasSpace=` | 200 `{ clans: [...] }` of the caller's game, by default sorted by name, at most 100. `q` keeps clans whose name contains it (ignoring upper/lower case, over 50 characters gives 400 `invalid_query`) and searches **all** clans, full ones too. `limit` 1 to 100 (400 `invalid_limit`); `sort` `name` or `members`, biggest first (400 `invalid_sort`); `hasSpace=true` leaves out full clans (400 `invalid_has_space`). Featured clans = `?hasSpace=true&sort=members&limit=3` |
| POST | `/api/v1/clans` | body `{ "name": "Wolves" }`. 201 with the new clan, the caller becomes its owner. 400 `invalid_json` / `invalid_clan_name` (3 to 20 characters); 409 `clan_name_taken` / `already_in_clan` |
| POST | `/api/v1/clans/{clanID}/join` | no body. 200 with the clan and its new `memberCount`, the caller joins as a member. 404 `clan_not_found` (also for another game's clan); 409 `already_in_clan` / `clan_full` (default 10 members) |
| POST | `/api/v1/clans/{clanID}/leave` | no body. 204: the caller left the clan and can join another one. A normal member is just removed. If the **owner** leaves and others remain, the member who joined first becomes the owner; if the owner is the only member, the clan is deleted. 404 `clan_not_found` (also for another game's clan); 409 `not_in_clan` (the caller is not a member of this clan) |
| GET | `/api/v1/clans/{clanID}/members` | 200 `{ members: [...] }` with `playerId`, `displayName`, `role`, `joinedAt`; owner first, then by join time. 404 `clan_not_found` (also for another game's clan) |

### Realtime service (port 8083)

Skeleton: only the standard endpoints exist so far. The conversation list, history and WebSocket are specified in
`openapi/realtime.yaml` and `openapi/realtime-ws.md` and are built in later tasks.

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| any | anything else | 404 `not_found` in the standard error format |

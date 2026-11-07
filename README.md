# clan-backend

Backend for the clan platform: two Go services in one repository.

| Service | Owns | Port | Database schema |
|---|---|---|---|
| `services/platform` | Developers, registered games, API keys, player tokens | 8081 | `platform` |
| `services/social` | Players, clans, clan members | 8082 | `social` |

## Layout

```
clan-backend/
├── go.mod                     # one Go module for the whole repo
├── docker-compose.yml         # postgres + platform + social
├── deploy/postgres/init/      # creates the schemas and database users on first start
├── openapi/                   # API contracts: platform.yaml, social.yaml
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
    └── social/                # same structure
```

A request flows `handler -> service -> repository`. Each layer only calls the one below it.

## Rules

1. **Routing uses the standard `net/http` ServeMux** (`mux.HandleFunc("GET /api/v1/clans/{clanID}", ...)`). No Chi.
2. **Shared code goes in `pkg/` only.** A service never imports another service's code. Go enforces this: packages under `services/platform/internal/` cannot be imported from `services/social`.
3. **Each service only touches its own schema.** If platform needs social data, it calls social's internal HTTP endpoints.
4. **Errors always use one shape:** `{ "error": { "code": "clan_full", "message": "..." } }` (see `pkg/httpx`).

## First-time setup

Needs Go 1.23 or newer.

```bash
# download dependencies and create go.sum (run once, and after adding a dependency)
go mod tidy
```

The repo is one Go module, but the two services are still separate programs with separate `main` packages, ports and database schemas.
If a service ever moves to its own repository, it gets its own `go.mod` then.

## JWT signing key

Platform signs developer tokens (and later player tokens) with an Ed25519 private key. Generate one once;
it is not committed (`*.pem` is in `.gitignore`) and platform will not start without it.

```bash
mkdir -p secrets
openssl genpkey -algorithm ed25519 -out secrets/platform_jwt_private.pem
chmod 644 secrets/platform_jwt_private.pem   # local dev only: the container runs as a non-root user and must read it
```

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_JWT_PRIVATE_KEY_FILE` | none (required) | Path to the PEM file. Docker Compose sets it to the mounted `secrets/platform_jwt_private.pem`. |
| `PLATFORM_DEVELOPER_TOKEN_TTL` | `24h` | How long a developer token is valid (Go duration such as `30m`). |

## Run with Docker (recommended)

```bash
cp .env.example .env        # Windows PowerShell: Copy-Item .env.example .env
# edit .env: set the three passwords (letters and digits only)
# create the JWT signing key first (see "JWT signing key" above)
docker compose up --build
```

This starts three long-running containers and one short migration job:

| Container | URL |
|---|---|
| postgres | `localhost:5432` (change with `POSTGRES_HOST_PORT`) |
| platform-migrate | none: applies pending platform SQL migrations, then exits (see "Database migrations") |
| platform | http://localhost:8081/health, http://localhost:8081/ready |
| social | http://localhost:8082/health, http://localhost:8082/ready |

`platform` starts only after `platform-migrate` finished successfully.

On the first start, `deploy/postgres/init/01-schemas.sh` creates:

| Database user | Owns schema | Can use other schemas? |
|---|---|---|
| `platform_user` | `platform` | No |
| `social_user` | `social` | No |

The init script runs only when the database volume is empty. After changing it, reset the database with
`docker compose down -v` (this deletes all local data) and start again.

## Run with go run

Start only Postgres in Docker and apply the migrations, then run a service from your editor or terminal:

```bash
docker compose up -d postgres
docker compose run --rm platform-migrate     # safe to repeat
go run ./services/platform/cmd      # uses PLATFORM_DATABASE_URL
go run ./services/social/cmd        # uses SOCIAL_DATABASE_URL
```

Set the `*_DATABASE_URL` variables from `.env.example` in your terminal or GoLand run configuration.
Without a database URL the services still start; `/health` answers 200 and `/ready` answers 503.

## Database migrations

Table definitions live in `services/<name>/migrations/` as `NNNNNN_name.up.sql` / `NNNNNN_name.down.sql`.
`docker compose up` applies the pending ones automatically: the `platform-migrate` job
([golang-migrate](https://github.com/golang-migrate/migrate)) runs first, as the service's own database user,
so tables land in that service's schema. It remembers what already ran in a `schema_migrations` table in that
schema, so running it again does nothing.

Social has no migrations yet, and golang-migrate fails on an empty folder, so `social-migrate` is commented out in
`docker-compose.yml`. **When you add the first file to `services/social/migrations/`, uncomment the `social-migrate`
job and the `social-migrate` entry in social's `depends_on`.**

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

These files are the contract the dashboard, SDK and demo game build against.
When you add or change a route, update `Routes()` in the service's `internal/handler/handler.go` **and** its OpenAPI file in the same commit.
`go test ./...` fails if a route is missing from the file, or the file lists a route that does not exist.
To browse a file, paste it into https://editor.swagger.io or open it with the OpenAPI plugin in GoLand.

## CORS (which websites may call the API)

| Service | Variable | Default | Who calls it from a browser |
|---|---|---|---|
| platform | `PLATFORM_CORS_ORIGINS` | `http://localhost:5173` | Dashboard (clan-frontend) |
| social | `SOCIAL_CORS_ORIGINS` | `http://localhost:5174` | Demo game with the SDK |

- Comma-separated list, each written exactly as `scheme://host:port` (no trailing slash). `*` is rejected at startup.
- Requests from other websites get 403 `origin_not_allowed`.
- Requests without an `Origin` header (servers, curl) are not affected, so the game's own server can still call `/players/auth`.

## Check

```bash
go build ./...
go vet ./...
go test ./...
```

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
| any | anything else | 404 `not_found` in the standard error format |
| any | from a website not in the allowed origins | 403 `origin_not_allowed` |

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
    │   └── migrations/
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

## Run

```bash
go run ./services/platform/cmd      # http://localhost:8081/health
go run ./services/social/cmd        # http://localhost:8082/health
```

Without a database the services still start; `/health` answers 200 and `/ready` answers 503.
To use a database, set `PLATFORM_DATABASE_URL` / `SOCIAL_DATABASE_URL` (see `.env.example`).
Docker Compose with PostgreSQL comes in the next task.

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
| any | anything else | 404 `not_found` in the standard error format |

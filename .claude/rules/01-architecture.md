# Architecture

Go backend for the clan platform: two services in one repository and one Go module (`gitlab.com/clan-platform/clan-backend`, Go 1.23).

| Service | Owns | Port | DB schema | DB user |
|---|---|---|---|---|
| `services/platform` | Developers, registered games, API keys, player tokens | 8081 | `platform` | `platform_user` |
| `services/social` | Players, clans, clan members | 8082 | `social` | `social_user` |

## 1. Layout

```
clan-backend/
├── docker-compose.yml        # postgres + platform + social
├── deploy/postgres/init/     # creates schemas and DB users on first start
├── openapi/                  # API contracts: platform.yaml, social.yaml
├── pkg/                      # shared code, used by both services
│   ├── config/               # settings from environment variables
│   ├── logx/                 # JSON logger (slog)
│   ├── pg/                   # PostgreSQL pool (pgx)
│   ├── httpx/                # error format, JSON helpers, middleware, server with graceful shutdown
│   └── jwt/                  # sign (platform) and verify (social) tokens, Ed25519
└── services/<name>/
    ├── cmd/main.go           # starts the service and wires the layers
    ├── internal/
    │   ├── handler/          # HTTP: routes, reading requests, writing JSON
    │   ├── service/          # business rules, no HTTP and no SQL
    │   └── repository/       # SQL, own schema only
    ├── migrations/           # SQL for tables in the service's schema
    └── Dockerfile
```

## 2. Layering

A request flows `handler -> service -> repository`. Each layer calls only the one below it.

| Layer | Does | Must not |
|---|---|---|
| handler | Routes, decode request, call service, write JSON | Contain business rules or SQL |
| service | Business rules, validation, returns `*httpx.Error` for expected failures | Import `net/http` handlers or write SQL |
| repository | SQL against the service's own schema | Contain business rules, reference another schema |

- Wiring happens only in `cmd/main.go`: repository -> service -> handler.
- Handlers depend on a small `Service` interface defined in the handler package, not on the concrete service type.
- Pass `context.Context` as the first parameter through every layer down to the database call.

## 3. Hard rules

1. **Routing uses the standard `net/http` ServeMux** with method patterns: `mux.HandleFunc("GET /api/v1/clans/{clanID}", ...)`. No Chi or other routers.
2. **Shared code goes in `pkg/` only.** A service never imports another service's code (`internal/` enforces this).
3. **Each service touches only its own schema.** If platform needs social data, it calls social's internal HTTP endpoints. Migrations never reference another service's schema.
4. **One error shape everywhere:** `{ "error": { "code": "clan_full", "message": "..." } }`, produced via `pkg/httpx`. Codes are stable, machine-readable `snake_case`.
5. **Schemas and DB users are created in `deploy/postgres/init/01-schemas.sh`, not in migrations.** Service users cannot create schemas.
6. **A service must start without a database**: `/health` returns 200, `/ready` returns 503 `not_ready`.
7. If a service ever moves to its own repository, it gets its own `go.mod` then.

## 4. Routes and OpenAPI

- Every endpoint is listed in `Routes()` in `internal/handler/handler.go`. Add new endpoints there.
- The OpenAPI file (`openapi/<service>.yaml`) must document exactly those routes. Update both **in the same commit**.
- `go test ./...` fails if a route is missing from the file or the file lists a route that does not exist.
- Standard endpoints: `GET /health` (200 while the process runs), `GET /ready` (200 when DB answers, else 503), anything else is a JSON 404 `not_found`.

## 5. Configuration

- All settings come from environment variables via `pkg/config`; never hardcode them.
- Service-specific variables are prefixed: `PLATFORM_PORT`, `PLATFORM_DATABASE_URL`, `SOCIAL_CORS_ORIGINS`.
- Every new variable is added to `.env.example` and documented in `README.md`.
- `.env` and `*.pem` are never committed.

## 6. CORS

- `<SERVICE>_CORS_ORIGINS` is a comma-separated list of exact `scheme://host:port` values, no trailing slash. `*` is rejected at startup.
- Other origins get 403 `origin_not_allowed`. Requests without an `Origin` header (servers, curl) are unaffected.

## 7. Middleware order

`httpx.Chain(mux, httpx.Recover(log), httpx.RequestLog(log), cors)`. Keep new middleware in `pkg/httpx`.

## 8. Docs upkeep

Changes to layout, rules, endpoints, env variables, or run instructions update `README.md` in the same commit.

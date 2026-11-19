# Running the backend

Docker Compose, the gateway, running a service with `go run`, database migrations and container images. To run every part of the platform together (Studio, SDK, demo game), see [running-locally.md](running-locally.md). Back to the [README](../README.md).

## Run with Docker (recommended)

```bash
cp .env.example .env        # Windows PowerShell: Copy-Item .env.example .env
# edit .env: set the five passwords (letters and digits only)
# create the JWT signing key first (see docs/configuration.md, "JWT signing key")
docker compose up --build
```

This starts these containers:

| Container | URL |
|---|---|
| postgres | `localhost:5432` (change with `POSTGRES_HOST_PORT`) |
| rabbitmq | `localhost:5672` (AMQP), management UI http://localhost:15672 (log in with `RABBITMQ_USER` / `RABBITMQ_PASSWORD`) |
| rabbitmq-setup | none: declares the RabbitMQ exchanges, queues, bindings and policies, then exits (see [RabbitMQ events](rabbitmq.md#rabbitmq-events)) |
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

**When CI builds them:** after the tests pass, on every push to `main`. Nothing is built from `develop`, because nothing
is deployed from it. On a merge request the job only starts when you press ▶ in the pipeline (to try a branch's images
before merging).

**Tags:** every image gets the short commit SHA (`social:a1b2c3d`, a fixed version to deploy and roll back to) and
the branch name (`social:main`, the newest build of that branch; a merge request's manual build is tagged with its branch). Builds from `main` also get `latest`.

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

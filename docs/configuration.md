# Configuration, keys and accounts

The JWT signing key, every environment variable of the three services, how tokens, sessions, email verification, password reset and admin accounts work, telemetry and CORS. Back to the [README](../README.md).

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
| `SOCIAL_OUTBOX_INTERVAL` | `1s` | How long social's outbox relay waits before looking again when it had nothing to send; also its first wait after a failed send (it then doubles, up to 30 s). See [Event outbox](rabbitmq.md#event-outbox). |
| `SOCIAL_OUTBOX_RETENTION` | `168h` | How long a sent event stays in social's `outbox` table before the relay deletes it (checked once an hour). |
| `PLATFORM_OUTBOX_INTERVAL` | `1s` | The same as `SOCIAL_OUTBOX_INTERVAL`, for platform's outbox relay. |
| `PLATFORM_OUTBOX_RETENTION` | `168h` | The same as `SOCIAL_OUTBOX_RETENTION`, for platform's `outbox` table. |
| `REALTIME_JWT_PUBLIC_KEY_FILE` | none (required) | The same public key, for realtime. Only the public key is mounted into the realtime container. |
| `REALTIME_SOCIAL_URL` | `http://localhost:8082` | Where realtime reaches the social service to learn each connecting player's own ID. Docker Compose sets it to `http://social:8082`. |
| `REALTIME_BACKFILL` | `false` | `true` makes realtime read every friendship from social's `/internal/v1/friendships` once at startup, before it reads events, and store the DM conversation of each pair (see "Realtime backfill" in [endpoints.md](endpoints.md)). Needs `REALTIME_INTERNAL_TOKEN`; without it realtime stops at startup. Anything but a boolean is an error. |
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
(for a game with a team: when no owner of it has an active account, see "Team management" in [endpoints.md](endpoints.md)), `game_suspended` when only that game is. A game is served only when the game is `active` and at least one of its owners is
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

### Email verification at registration

With `PLATFORM_EMAIL_VERIFICATION=true`, registering does not create an account straight away:

1. `POST /api/v1/developers/register` checks the form, stores the registration as **pending** and emails a 6-digit code
   to the address. It answers 202. There is no developer row yet.
2. The dashboard's "enter the code" page sends the email and the code to `POST /api/v1/developers/register/verify`.
   With the right code the developer is created (201) and can log in.
3. `POST /api/v1/developers/register/resend` emails a new code when the first one did not arrive.

Rules: the code works **once** and for **15 minutes**; the **fifth wrong code** cancels the registration; a new code is
sent at most once a minute; registering again with the same address replaces the pending registration. A registration
nobody confirms simply expires (`pending_registrations`), so there are no unverified accounts and a mistyped address
is free again after 15 minutes. Limits, counted in platform's memory: 5 code emails per address and 20 per IP address
in 15 minutes, 60 verify calls per IP address.

Registering an address that **already has an account** answers the same 202 and creates nothing; that address gets
an email saying someone tried to register with it. So with verification on, registration no longer tells who has an
account (without it, the 409 `email_taken` does).

The emails go through the same SMTP settings as the password reset. Without `PLATFORM_SMTP_HOST` the code is written to
platform's log (`email not sent: no mail server is configured`), which is how you try it on your own machine.

**Off (the default)**, register creates the account at once and answers 201, as it always did, and the two extra
routes answer 404. Tests and `tools/e2e` rely on that.

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_EMAIL_VERIFICATION` | `false` | `true`: a new developer must enter the emailed code before the account is created. |

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

## CORS (which websites may call the API)

| Service | Variable | Default | Who calls it from a browser |
|---|---|---|---|
| platform | `PLATFORM_CORS_ORIGINS` | `http://localhost:5173` | Dashboard (clan-frontend) |
| social | `SOCIAL_CORS_ORIGINS` | `http://localhost:5174` | Demo game with the SDK |
| realtime | `REALTIME_CORS_ORIGINS` | `http://localhost:5174` | Demo game with the SDK |

- Comma-separated list, each written exactly as `scheme://host:port` (no trailing slash). `*` is rejected at startup.
- Requests from other websites get 403 `origin_not_allowed`.
- Requests without an `Origin` header (servers, curl) are not affected, so the game's own server can still call `/players/auth`.

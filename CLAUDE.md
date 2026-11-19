# clan-backend

The backend of Sociable (clans, friends and live chat for web games): three Go services in one module. Read `README.md` first, then `docs/overview.md`. The rest of the reference is in `docs/` (index in the README's Documentation table).

- `services/platform` (8081): developers, sessions, admins, games and their teams, API keys, widget config (draft and published), player tokens.
- `services/social` (8082): players, clans, members and roles, join requests, friends, blocks.
- `services/realtime` (8083): WebSockets, conversations, messages, read positions, presence, notifications.
- Frontend (Studio) is `../clan-frontend`, the SDK `../clan-sdk`, the example game `../demo-game`.

## Rules

1. Routing uses the standard `net/http` ServeMux (`mux.HandleFunc("GET /api/v1/clans/{clanID}", ...)`). No Chi or other router.
2. A request flows `handler -> service -> repository`; each layer only calls the one below. Business rules live in `service` (no HTTP, no SQL); SQL only in `repository`.
3. Shared code goes in `pkg/` only. A service never imports another service's code.
4. Each service touches only its own schema. Platform data from Social goes through an internal HTTP endpoint (`X-Internal-Token`) or an event; never a cross-schema query.
5. One error shape everywhere: `{ "error": { "code": "clan_full", "message": "..." } }` (`pkg/httpx`). Codes are snake_case and documented in the OpenAPI file.
6. Every query is scoped by the game ID (tenant). A player of one game must never see another game's data.
7. Events are written through the service's transactional outbox, never published straight from a request. To add one, follow `docs/events.md`, section 12 (message doc, binding in `deploy/rabbitmq/definitions.json`, publisher, consumer, tests, docs).

## Changing the API or the database

- A new or changed route updates `openapi/<service>.yaml` and `docs/endpoints.md` in the same MR. `TestOpenAPIMatchesRoutes` fails when a route and the file disagree.
- Migrations: a new numbered pair `services/<service>/migrations/0000NN_name.up.sql` / `.down.sql`, the next number after the last file. Never edit a migration that has already run. Never reference another service's schema.
- A new environment variable goes into `.env.example`, `docker-compose.yml`, the Helm chart (`deploy/helm/clan`) and `docs/configuration.md`.

## Checks

```bash
go build ./...
go vet ./...
go test -race ./...      # what CI runs; needs gcc
go run ./tools/e2e       # against a running `docker compose up` stack
```

CI runs build, vet and the race tests on `main` and on demand for an MR, not on `develop`, so run them locally before every MR. Tests that need a real database or RabbitMQ skip themselves unless their `*_TEST_DATABASE_URL` is set (`docs/development.md`).

## Deploy and secrets

- **Every merge to `main` is deployed** to the server (`.gitlab-ci.yml`, `deploy/deploy.sh`, chart in `deploy/helm/clan`). Rollbacks, logs and admins: `docs/operations.md`.
- Never print, paste or commit secrets: `.env`, `secrets/*.pem`, `deploy/helm/clan/secrets.prod.yaml`, API keys, tokens, SMTP passwords. Never commit `helm template` output.
- The production database is reached only through the SSH tunnel (`docs/operations.md`), and only read unless the team agreed otherwise.
- Load tests against the server only at a time agreed with the team, never the `stress` profile without agreement (`loadtest/README.md`).

## Git conventions

- **Branches:** `<category>/<JIRA-ID>-<short-description>`; category lowercase (`feat fix hotfix refactor test docs ci chore release`), Jira ID uppercase (this project: `TEAM-nn`), description lowercase hyphenated 2-4 words. `chore`/`release` need no ticket ID.
- **Long-lived:** `main` and `develop`, both protected. Never push to them; branch from the latest `develop`, one feature or fix per branch, MR into `develop`, delete the branch after merge. `develop` is merged into `main` for a release.
- **Commits:** Conventional Commits, `type(scope): subject`, imperative present tense, no trailing period, first line <= 72 chars; add the ticket, e.g. `fix(chat): keep the draft after a failed send (TEAM-123)`.
- **MRs:** title in commit format; small and focused; draft for WIP. Description: `## Summary`, `## Changes`, `## Type` (Feature / Bug fix / Refactor / Documentation / CI), `## Notes (optional)`.
- **Review:** share the link in the group chat; teammates react; the author resolves discussions; the Team Lead gives the only formal approval; the author merges into `develop`. Prefix feedback `blocking:` / `suggestion:`.
- Never commit secrets (`.env`, keys, tokens, `*.pem`) or broken code. Resolve conflicts locally.

## Jira conventions

- BA writes User Stories; FE/BE create sub-tasks under the story; QA creates `[QA] <story title> Testing` and logs bugs as its sub-tasks.
- Titles: `[FE]`/`[BE]` + imperative verb + what + context; bugs: `[FE]`/`[BE]` + what is broken. Sentence case, <= 80 chars, no trailing punctuation.
- Description before IN PROGRESS: `## What to deliver` (2-3 sentences) and `## Done when` (testable checkboxes).
- Status: `BLOCKED -> TO DO -> IN PROGRESS -> RESOLVED (ON REVIEW) -> QA REVIEW -> DONE`, with a comment on every change (In Progress -> Resolved: `MR opened. / MR: <link> / Branch: <name> / What changed: <one sentence>`).

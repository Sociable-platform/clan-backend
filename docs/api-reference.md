# API reference

Where each API is described. The descriptions are files in this repository, kept next to the
code they describe.

## The files

| What | File | For |
|---|---|---|
| platform's HTTP API | `openapi/platform.yaml` | Studio, the admin panel, a game's server (player tokens), the SDK (published widget config) |
| social's HTTP API | `openapi/social.yaml` | The SDK: players, clans, members, join requests, friends, blocks |
| realtime's HTTP API | `openapi/realtime.yaml` | The SDK: the conversation list and message history |
| realtime's WebSocket | `openapi/realtime-ws.md` | The SDK: every frame in both directions, error codes, close codes |
| Events between services | `docs/events.md`, `docs/event-messages.md` | Whoever adds or consumes an event: the envelope, every event's fields, who publishes and who listens |
| The realtime flows as diagrams | `docs/realtime-flow.md` | Understanding the order of things |
| The widget config | clan-frontend, `docs/config-v1/` | The document Studio produces and the SDK draws; platform validates against the same schema |

To browse an OpenAPI file, paste it into <https://editor.swagger.io>, or open it with an OpenAPI
plugin in your editor.

## Who may call what

| Caller | Proves who it is with | Can reach |
|---|---|---|
| A developer in Studio | a developer token (15 minutes, renewed with a refresh cookie) | platform: their own account, games, API keys, widget config |
| An admin in Studio | an admin token (1 hour) | platform: `/api/v1/admin/...` only |
| A game's server | the game's API key, in `X-API-Key` | platform: `POST /api/v1/players/auth` |
| A player's browser (the SDK) | a player token (1 hour) | social and realtime, for that player and that game only |
| Anyone | nothing | platform: a game's published widget config; every service's `/health` and `/ready` |
| Another of our services | a shared internal token, in `X-Internal-Token` | the `/internal/v1/...` endpoints |

A token of one kind is refused where another kind is expected: a developer token does not work
on admin endpoints, and neither works as a player token.

Every error has the same shape, whichever service answers:

```json
{ "error": { "code": "clan_full", "message": "This clan is full" } }
```

Clients should act on `code`; `message` is for people.

## How the files are kept true

Each service has a test, `TestOpenAPIMatchesRoutes` (in its `internal/handler/openapi_test.go`),
that compares the routes the service registers with the paths in its OpenAPI file. `go test ./...`
fails when a route is missing from the file or the file lists a route that does not exist. So
the three YAML files cannot drift from the routes without a red test.

What the test does not check: request and response bodies, status codes, and the two Markdown
files (the WebSocket frames and the events). Those are kept right by hand:

- When you add or change a route, update `Routes()` in the handler **and** the OpenAPI file in
  the same commit.
- When you add or change a WebSocket frame or an event, update `openapi/realtime-ws.md` or
  `docs/event-messages.md` in the same commit.
- The widget config schema exists twice: in clan-frontend (`docs/config-v1/config.v1.schema.json`)
  and here (`services/platform/internal/widgetconfig/config.v1.schema.json`). They must be the
  same file; change both together.

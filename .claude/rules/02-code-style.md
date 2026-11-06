# Code Style

Idiomatic Go. When in doubt, follow [Effective Go](https://go.dev/doc/effective_go) and the Go Code Review Comments.

## 1. Tooling gate

Before every commit, all three must pass clean:

```bash
go build ./...
go vet ./...
go test ./...
```

Code must be `gofmt`-formatted (`goimports` ordering). Do not commit broken or unfinished code.

## 2. Naming

| Item | Convention | Example |
|---|---|---|
| Packages | short, lowercase, no underscores | `httpx`, `logx`, `handler` |
| Files | lowercase, `snake_case` if multi-word | `clan_members.go` |
| Exported identifiers | PascalCase | `WriteJSON`, `ErrorBody` |
| Unexported identifiers | camelCase | `notFound`, `serviceName` |
| Constants | PascalCase / camelCase, not `UPPER_SNAKE` | `MaxBodyBytes` |
| Interfaces | behavior-named, defined where used | `Service` in `handler` |
| Acronyms | consistent case | `ID`, `URL`, `HTTP`, `JSON` |
| Tests | `<file>_test.go`, `TestThing_Case` | `TestOpenAPIMatchesRoutes` |
| JSON fields / error codes | `snake_case` | `clan_full`, `not_ready` |

Don't stutter: `clan.Service`, not `clan.ClanService`.

## 3. Structure

- Keep `main` thin: `main()` calls `run() error`; `run` wires dependencies and returns errors.
- Every package has a doc comment; every exported identifier has a comment starting with its name.
- Define interfaces in the **consumer** package and keep them small. Accept interfaces, return structs.
- Constructors are `New(...)`. No global mutable state; pass dependencies explicitly.
- Avoid `init()` and package-level variables other than constants and sentinel errors.

## 4. Errors

- Expected, user-facing failures are `*httpx.Error` created with `httpx.NewError(status, code, message)` in the **service** layer.
- Handlers write errors with `httpx.WriteErr(w, err)`; unknown errors become a generic 500 `internal_error`. Never leak internal details, SQL, or stack traces in a response.
- Wrap unexpected errors with context: `fmt.Errorf("load clan %d: %w", id, err)`. Check with `errors.Is` / `errors.As`.
- Messages are lowercase, no trailing punctuation (internal errors). Response messages are full sentences for people.
- Never ignore an error silently. `_ =` only with a comment-worthy reason (for example, writing a response body).
- No `panic` outside startup; `httpx.Recover` is the safety net, not control flow.

## 5. HTTP

- Read bodies with `httpx.DecodeJSON` (size limit, unknown fields rejected). Write with `httpx.WriteJSON`.
- Use `http.Status*` constants and `http.Method*` constants, not literals.
- Path parameters via `r.PathValue("clanID")`.
- Versioned API paths: `/api/v1/...`. Plural nouns for collections.

## 6. Database

- Use `pgx` with the pool from `pkg/pg`; always parameterized queries (`$1`), never string-built SQL.
- SQL lives only in `repository`. Return domain types, not `pgx` types, to the service.
- Migrations: `NNNNNN_description.up.sql` / `.down.sql` in `services/<name>/migrations/`.

## 7. Logging

- Use the `slog` logger from `pkg/logx` (structured, JSON). Key/value pairs: `log.Info("cors", "allowed_origins", origins)`.
- Never log secrets, tokens, passwords, or API keys.

## 8. Concurrency

- Take a `context.Context` for anything that blocks; honor cancellation.
- Every goroutine has a clear owner and a way to stop. Shutdown is graceful via `httpx.Run`.

## 9. Testing

- Standard `testing` package, table-driven tests, `t.Run` subtests. Tests live next to the code (`*_test.go`).
- Handlers are tested with `net/http/httptest`; services with fake repositories (small interfaces make this easy).
- New logic ships with tests. Every new route also keeps `TestOpenAPIMatchesRoutes` green.
- Don't depend on a real database or network in unit tests.

## 10. Security

- No secrets in code or git. Config comes from the environment.
- JWTs are Ed25519: platform signs, social verifies. Don't share private keys with social.
- Validate all input in the service layer before touching the database.

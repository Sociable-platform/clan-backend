# Deployment and operations

How the platform gets to the server, how to see what is running, and how to do the jobs that
come up: roll back, read logs, look at the database, create an admin.

This page is for the team. It names the server and describes how to reach production data, so it
must stay in the repository and never be published in Studio's public guide. It contains no
passwords, keys or tokens, and none may be added.

The backend's Helm chart has its own, longer README: `deploy/helm/clan/README.md`. This page is
the map; that one is the detail.

## What is deployed

One server (Oracle Cloud, ARM, `130.61.158.79`) runs k3s. Everything is in the Kubernetes
namespace `clan`, as four Helm releases, each deployed by its own repository:

| Release | Repository | What it is | Address |
|---|---|---|---|
| `clan` | clan-backend | platform, social, realtime, PostgreSQL, RabbitMQ | `platform.`, `social.`, `realtime.130-61-158-79.sslip.io` |
| `clan-studio` | clan-frontend | Studio | `studio.130-61-158-79.sslip.io` |
| `clan-sdk` | clan-sdk | the SDK script | `sdk.130-61-158-79.sslip.io` |
| `clan-game` | demo-game | the demo game | `game.130-61-158-79.sslip.io` |

`clan` is installed first: the other three reuse its registry pull secret and its certificate
issuer. HTTPS certificates come from Let's Encrypt through cert-manager and renew themselves.

## How a change reaches the server

**Only `main` deploys.** In every repository:

1. Work is merged into `develop` through merge requests. Nothing is built or deployed from
   `develop`.
2. A release is a merge request from `develop` to `main`.
3. The pipeline on `main` runs the tests, builds the images (tagged with the commit's short SHA)
   and runs `deploy/deploy.sh <sha>`, which copies the chart to the server over SSH and runs
   `helm upgrade --install --wait` there.
4. Helm returns only when the new pods are ready. If they are not ready in time (10 minutes for
   the backend, 5 for the others) the deploy fails and the old pods keep serving.

On a merge request the test and image jobs do not start by themselves: press the play button in
the pipeline. Run the tests on your machine before merging, because nothing else checks a branch
until `main`.

**Release the parts that depend on each other together.** A backend change and the Studio or SDK
change that uses it are separate pipelines. Merge the backend to `main` first and let it finish,
then the others. A setting that switches on something Studio must support (for example email
verification) goes in only after that Studio is live.

### CI variables

Each repository's deploy job needs protected CI/CD variables (Settings -> CI/CD -> Variables), so
`main` must be a protected branch. Their values are secrets and are never written in a file here.

| Variable | Used by | What it is |
|---|---|---|
| `DEPLOY_SSH_KEY_B64` | all four | CI's own SSH private key for the server |
| `DEPLOY_SSH_KNOWN_HOSTS` | all four | the server's host key, so CI talks to the right machine |
| `DEPLOY_SECRETS_B64` | clan-backend | the chart's secrets file |
| `DEPLOY_JWT_PRIVATE_B64`, `DEPLOY_JWT_PUBLIC_B64` | clan-backend | platform's token signing key pair |

How to produce each value is in `deploy/helm/clan/README.md`, "Deploy". To change a secret,
update the variable; the next deploy applies it.

## What is running

On the server (`ssh ubuntu@130.61.158.79`):

```bash
kubectl -n clan get pods                 # everything, with its state
kubectl -n clan get deploy -o wide       # the IMAGE column ends with the version (commit SHA)
helm list -n clan                        # the four releases and their revision
helm history clan -n clan                # the backend's past deploys
```

Or in GitLab: Operate -> Environments, in each repository.

From anywhere:

```bash
curl https://platform.130-61-158-79.sslip.io/health
curl https://social.130-61-158-79.sslip.io/health
curl https://realtime.130-61-158-79.sslip.io/health
```

`/health` says the process is up. `/ready` also checks that it reaches its database.

## Rolling back

The quickest way is GitLab: Operate -> Environments -> the environment -> run an older
deployment again. That deploys the earlier image.

By hand, from a laptop with access (Git Bash, not PowerShell):

```bash
bash deploy/deploy.sh <short sha of the version to go back to>
```

Or on the server, to the previous Helm revision:

```bash
helm history clan -n clan
helm rollback clan <revision> -n clan
```

Two things a rollback does not undo:

- **Database migrations.** Rolling back the code leaves the tables as the newer version made
  them. A migration that only adds things is safe to leave. One that removes or renames needs a
  plan before it is released, not after.
- **A published widget.** That is data, not code. A developer rolls it back in Studio.

## Logs

```bash
kubectl -n clan logs deploy/platform --tail=100       # the last 100 lines
kubectl -n clan logs deploy/social --tail=100 -f      # live; Ctrl+C stops
kubectl -n clan logs deploy/realtime --since=30m      # the last half hour
kubectl -n clan logs deploy/social -c migrate         # that service's migrations
kubectl -n clan logs deploy/platform --previous       # the pod before a crash
```

The services log JSON, one line per request, with `trace_id` when telemetry is on. Useful
searches:

| Looking for | Search the log for |
|---|---|
| A failed request | `"request failed"` or the path |
| An event that was not delivered | `outbox relay could not send` (platform, social) |
| An event realtime refused | `event rejected to the dead-letter queue` |
| Email that was not sent | `mail` in platform's log |

A pod that does not start: `kubectl -n clan describe pod <name>` shows why (image not found,
failed migration, missing secret). A pod stuck in `Init:Error` has a failed migration; read the
`-c migrate` log.

## Telemetry

Traces and metrics go through an OpenTelemetry Collector on the server to Datadog, together with
the pods' logs. The services send them when `telemetry.enabled` is true in the chart's values;
`service.version` on every trace is the commit that is running. Details, and what is traced:
configuration.md "Telemetry", and `deploy/helm/clan/README.md`.

## Looking at the production database

PostgreSQL is inside the cluster and is not open to the internet. To reach it, forward a port
through SSH. This reads production data, so do it only when you need to, and close it afterwards.

```bash
ssh -t -L 5433:localhost:15432 ubuntu@130.61.158.79 "kubectl -n clan port-forward svc/postgres 15432:5432"
```

Leave that terminal open. It maps port **5433 on your laptop** to the database. 15432 is used on
the server because 5432 there is taken. `-t` makes Ctrl+C stop the forward on the server too.

Then connect a client (DBeaver, `psql`) to:

| Field | Value |
|---|---|
| Host | `localhost` |
| Port | `5433` |
| Database | `clan` |
| User | `platform_user`, `social_user` or `realtime_user`: each sees only its own schema |
| Password | that user's password, from the chart's secrets file |

- Mark the connection as production and read-only in your client.
- Your local database is on 5432. The port is the only thing that tells the two apart: check it
  before you run anything.
- Never change data by hand to fix a problem without telling the team. Prefer a migration or an
  admin action, which leave a record.

## Create an admin

Admins are separate accounts for staff. There is no register endpoint: whoever can reach the
platform database creates one with `tools/createadmin`. It stores the password as a bcrypt hash.

With the tunnel above open, in `clan-backend`, in PowerShell:

```powershell
$pw = Read-Host "Platform DB password" -MaskInput
$env:PLATFORM_DATABASE_URL = "postgres://platform_user:$([uri]::EscapeDataString($pw))@localhost:5433/clan"
Remove-Variable pw
go run ./tools/createadmin -email person@example.com -name "Person Name"
```

The tool then waits on an empty line: type the new admin's password (8 to 72 characters) and
press Enter. It prints `created admin <email> (<id>)`. Afterwards:

```powershell
Remove-Item Env:PLATFORM_DATABASE_URL
```

and close the tunnel.

- `EscapeDataString` matters: a database password with characters such as `@` or `/` otherwise
  breaks the address (`invalid userinfo`).
- **For a local admin**, skip the tunnel and use `localhost:5432/clan?sslmode=disable` with the
  `PLATFORM_DB_PASSWORD` from your `.env`.
- One admin per person, so the audit log shows who did what. Tell the team when you create one.
- Removing an admin has no tool yet. An admin whose `disabled_at` is set cannot sign in and their
  tokens stop at once; today that is a manual update of the `admins` table.

## Events that were not delivered

Events realtime could not use go to a dead-letter queue instead of being lost. Once the cause is
fixed they can be sent again:

```bash
go run ./tools/dlq list
go run ./tools/dlq replay -id <event_id>,...
```

rabbitmq.md "RabbitMQ events", explains the queue, how long dead events are kept, and how to
read why one was refused.

## Secrets: where they are and the rules

| Secret | Lives in |
|---|---|
| Database, RabbitMQ and mail passwords, internal tokens, Datadog key | the chart's secrets file (`secrets.prod.yaml`, ignored by git) and the `DEPLOY_SECRETS_B64` CI variable; on the server, the Kubernetes Secret `clan-secrets` |
| Token signing key pair | `secrets/prod/*.pem` (ignored by git), two CI variables, and two Kubernetes Secrets |
| SSH key for deploys | a CI variable; the matching public key is in the server's `authorized_keys` |
| A game's API key | shown once to the developer; platform stores only a hash |

- Never commit a secret, paste one into a ticket or chat, or put one on a command line.
- `helm template` and `helm get values` print secrets in plain text. Do not save their output
  into the repository.
- If a secret may have leaked, replace it: change the value, update the CI variable, deploy.

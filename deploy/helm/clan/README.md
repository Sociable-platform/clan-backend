# Helm chart: clan

Installs the clan platform on Kubernetes (k3s on the Oracle server). It is built in parts:

| Part | What it adds | Ticket |
|---|---|---|
| 1 (this) | PostgreSQL, RabbitMQ, the Secrets, the registry pull secret | TEAM-173 |
| 2 | platform, social, realtime and their migration Jobs | next |
| 3 | Ingress, domain, HTTPS; then Studio and the demo game | later |

Everything is installed into the namespace `clan`, as the release `clan`.

## Files

| File | What it is |
|---|---|
| `values.yaml` | the defaults |
| `values-prod.yaml` | the Oracle server (bigger disks) |
| `values-local.yaml` | a cluster on your own computer (k3d, Docker Desktop): smaller limits |
| `secrets.example.yaml` | the shape of the secrets file. Copy it to `secrets.prod.yaml`; `secrets*.yaml` is in `.gitignore` |
| `files/01-schemas.sh` | **copy** of `deploy/postgres/init/01-schemas.sh`: creates each service's user and schema |
| `files/definitions.json` | **copy** of `deploy/rabbitmq/definitions.json`: the exchange, the queue and its bindings |

Helm can only read files inside the chart folder, so the two files are copies. **When you change the original, copy it
here too**, or docker-compose and Kubernetes drift apart.

## What gets created

| Object | Name | Notes |
|---|---|---|
| StatefulSet + Service | `postgres` (port 5432) | `postgres:16-alpine`, disk `data-postgres-0`. On the first start (empty disk) `01-schemas.sh` creates `platform_user`, `social_user`, `realtime_user` and their schemas |
| StatefulSet + Service | `rabbitmq` (5672, management 15672) | `rabbitmq:3-management-alpine`, disk `data-rabbitmq-0`. The user `clan` is created on the first start |
| Job (Helm hook) | `rabbitmq-setup` | after every install and upgrade: imports `definitions.json` (exchange `socialkit.events`, queue `realtime.events`, bindings), like `rabbitmq-setup` in docker-compose |
| Secret | `clan-secrets` | the 5 passwords and the 2 internal tokens |
| Secret | `clan-jwt-private`, `clan-jwt-public` | platform's key pair, kept apart so social and realtime only ever get the public one |
| Secret | `gitlab-registry` | only when the registry token is given: lets the cluster pull the private images |

The service names are the same as in docker-compose (`postgres`, `rabbitmq`), so the services' URLs do not change.

## Install on the Oracle server

Open the tunnel first (in its own window, see the main README), then run everything from the repository root.

**1. The secrets file** (once):

```bash
cp deploy/helm/clan/secrets.example.yaml deploy/helm/clan/secrets.prod.yaml
# fill it in: passwords from `openssl rand -hex 16`, internal tokens from `openssl rand -hex 32`
```

**2. A key pair for the server** (once; not the one from your laptop's `secrets/`, which is for local development):

```bash
mkdir -p secrets/prod
openssl genpkey -algorithm ed25519 -out secrets/prod/platform_jwt_private.pem
openssl pkey -in secrets/prod/platform_jwt_private.pem -pubout -out secrets/prod/platform_jwt_public.pem
```

**3. The registry token** (once): GitLab group `clan-platform` -> Settings -> Repository -> Deploy tokens, scope
`read_registry`. Put its username and token into `registryUsername` and `registryPassword`.

**4. Check, then install or upgrade** (the same command does both):

```bash
helm lint deploy/helm/clan -f deploy/helm/clan/values-prod.yaml -f deploy/helm/clan/secrets.prod.yaml \
  --set-file secrets.jwtPrivateKey=secrets/prod/platform_jwt_private.pem \
  --set-file secrets.jwtPublicKey=secrets/prod/platform_jwt_public.pem

helm upgrade --install clan deploy/helm/clan -n clan --create-namespace \
  -f deploy/helm/clan/values-prod.yaml -f deploy/helm/clan/secrets.prod.yaml \
  --set-file secrets.jwtPrivateKey=secrets/prod/platform_jwt_private.pem \
  --set-file secrets.jwtPublicKey=secrets/prod/platform_jwt_public.pem
```

`helm template` with the same arguments prints the YAML without installing anything.
In PowerShell, put the command on one line or end each line with a backtick (`` ` ``) instead of `\`.

**5. Check it:**

```bash
kubectl -n clan get pods,pvc          # postgres-0 and rabbitmq-0 Running 1/1, both PVCs Bound
kubectl -n clan logs postgres-0 | grep "init: created schemas"
kubectl -n clan exec postgres-0 -- psql -U postgres -d clan -c '\dn'    # schemas platform, social, realtime
kubectl -n clan exec rabbitmq-0 -- rabbitmqctl list_queues name durable  # realtime.events  true
```

## Good to know

- **The data survives** pod restarts and `helm upgrade`. It even survives `helm uninstall`: the disks (PVCs) stay
  until you delete them by hand (`kubectl -n clan delete pvc data-postgres-0 data-rabbitmq-0`), which erases all data.
- **The init script and the RabbitMQ user only run on an empty disk.** Changing a password in the secrets file later
  does not change it inside Postgres or RabbitMQ. Change it there too:
  `kubectl -n clan exec postgres-0 -- psql -U postgres -c "ALTER ROLE social_user PASSWORD '...'"` or
  `kubectl -n clan exec rabbitmq-0 -- rabbitmqctl change_password clan '...'`.
- **RabbitMQ's management UI** is not public. Open it on your laptop with
  `kubectl -n clan port-forward svc/rabbitmq 15672:15672`, then http://localhost:15672.
- **Postgres from your laptop** (DBeaver): `kubectl -n clan port-forward svc/postgres 15432:5432`, then connect to
  `localhost:15432`.

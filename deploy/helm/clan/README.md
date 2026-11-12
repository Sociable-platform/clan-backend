# Helm chart: clan

Installs the clan platform on Kubernetes (k3s on the Oracle server). It is built in parts:

| Part | What it adds | Ticket |
|---|---|---|
| 1 | PostgreSQL, RabbitMQ, the Secrets, the registry pull secret | TEAM-173 |
| 2 | platform, social and realtime, each with its migrations | ticket 4 |
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
| Deployment + Service | `platform` (8081), `social` (8082), `realtime` (8083) | images from the CI job `images`. Each pod first runs an init container with the service's `-migrate` image (`migrate up`), then the service. Probes: `/health` (startup, liveness) and `/ready` (readiness: the database is reachable). Platform mounts the private JWT key, social and realtime only the public one |

**Realtime is always one pod** and is updated with `strategy: Recreate` (the old pod stops before the new one
starts): it keeps presence, rate limits and the config cache in memory and is the only reader of `realtime.events`.
Platform and social can run more pods (`platform.replicas`, `social.replicas`).

**Migrations** run in an init container of every pod rather than in a separate Job: on the very first install a
Helm "pre-install" Job would start before the database password Secret exists. Running them in every pod is safe:
golang-migrate takes a lock in Postgres and skips what already ran. A failed migration keeps the pod in `Init:Error`
(see `kubectl -n clan logs deploy/social -c migrate`) and the old pods keep serving.

**Which version runs:** `image.tag` (default `develop`, the newest build of develop). For a fixed version that you
can roll back to, use a commit SHA: `--set image.tag=a1b2c3d` (the tags are in GitLab -> Deploy -> Container registry).

The service names are the same as in docker-compose (`postgres`, `rabbitmq`), so the services' URLs do not change.

## Install on the Oracle server

The steps below prepare the files once (1-3) and install (4) **on the server itself**: a long `helm upgrade` through
the SSH tunnel breaks when the connection drops, while one on the server keeps running. Run the commands from the
repository root on your laptop unless they say "on the server".

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

**4. Check on your laptop, copy to the server, install there** (the same command installs and upgrades):

```bash
# laptop: check the chart (does not need the cluster)
helm lint deploy/helm/clan -f deploy/helm/clan/values-prod.yaml -f deploy/helm/clan/secrets.prod.yaml \
  --set-file secrets.jwtPrivateKey=secrets/prod/platform_jwt_private.pem \
  --set-file secrets.jwtPublicKey=secrets/prod/platform_jwt_public.pem

# laptop: copy the chart (with secrets.prod.yaml) and the two keys to the server, then log in
scp -r deploy/helm/clan ubuntu@130.61.158.79:clan-chart
scp secrets/prod/platform_jwt_private.pem secrets/prod/platform_jwt_public.pem ubuntu@130.61.158.79:
ssh ubuntu@130.61.158.79

# on the server (Helm 3 is installed there: curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash)
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
helm upgrade --install clan ~/clan-chart -n clan --create-namespace --timeout 10m \
  -f ~/clan-chart/values-prod.yaml -f ~/clan-chart/secrets.prod.yaml \
  --set-file secrets.jwtPrivateKey=$HOME/platform_jwt_private.pem \
  --set-file secrets.jwtPublicKey=$HOME/platform_jwt_public.pem
# the secrets now live in Kubernetes: remove the copies
rm -rf ~/clan-chart ~/platform_jwt_private.pem ~/platform_jwt_public.pem
```

Add `--set image.tag=<sha>` to run a fixed version. `helm template` with the same arguments prints the YAML without
installing. In PowerShell, end each line with a backtick (`` ` ``) instead of `\`.
`helm rollback clan <revision> -n clan` (on the server) goes back to an earlier install; `helm history clan -n clan`
lists them.

**5. Check it:**

```bash
kubectl -n clan get pods,pvc          # postgres-0 and rabbitmq-0 Running 1/1, both PVCs Bound
kubectl -n clan logs postgres-0 | grep "init: created schemas"
kubectl -n clan exec postgres-0 -- psql -U postgres -d clan -c '\dn'    # schemas platform, social, realtime
kubectl -n clan exec rabbitmq-0 -- rabbitmqctl list_queues name durable  # realtime.events  true
kubectl -n clan get deploy                      # platform, social, realtime READY 1/1
kubectl -n clan logs deploy/social -c migrate   # the migrations of that service
kubectl -n clan logs deploy/social              # the service itself
```

Call a service from inside the cluster (distroless images have no shell or curl, so use a throwaway pod):

```bash
kubectl -n clan run curl --rm -i --restart=Never --image=curlimages/curl:8.11.1 -- \
  sh -c 'for s in platform:8081 social:8082 realtime:8083; do curl -s http://$s/ready; echo; done'
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

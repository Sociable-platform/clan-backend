# Helm chart: clan

Installs the clan platform on Kubernetes (k3s on the Oracle server). It is built in parts:

| Part | What it adds | Ticket |
|---|---|---|
| 1 | PostgreSQL, RabbitMQ, the Secrets, the registry pull secret | TEAM-173 |
| 2 | platform, social and realtime, each with its migrations | TEAM-175 |
| 3 | public URLs (Traefik IngressRoutes), HTTPS with Let's Encrypt | TEAM-176 |
| 4 | deploys from CI (`deploy/deploy.sh`, job `deploy` on main) | TEAM-177 |
| 5 | the demo game | later |

Studio and the SDK bundle are **not** part of this chart. Each is its own Helm release in the same namespace,
deployed by its own repository's pipeline: `clan-studio` from clan-frontend (`https://studio.<domain>`, TEAM-248) and
`clan-sdk` from clan-sdk (`https://sdk.<domain>`, TEAM-242). They reuse this release's registry pull secret
(`gitlab-registry`) and certificate issuer (`clan-letsencrypt`), so this chart is installed first. A new Studio or SDK
version needs no change here.

Everything is installed into the namespace `clan`, as the release `clan`.

## Files

| File | What it is |
|---|---|
| `values.yaml` | the defaults |
| `values-prod.yaml` | the Oracle server (bigger disks) |
| `values-local.yaml` | a cluster on your own computer (k3d, Docker Desktop): smaller limits |
| `secrets.example.yaml` | the shape of the secrets file. Copy it to `secrets.prod.yaml`; `secrets*.yaml` is in `.gitignore` |
| `templates/otel-collector.yaml` | the OpenTelemetry Collector (traces and metrics to Datadog), only with `telemetry.collector.enabled` |
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

## Public URLs and HTTPS

With `ingress.domain` set (`values-prod.yaml`: `130-61-158-79.sslip.io`), k3s's Traefik publishes:

| URL | Service |
|---|---|
| `https://platform.<domain>` | platform (Studio's `VITE_PLATFORM_URL`, the SDK's `platformUrl`) |
| `https://social.<domain>` | social (the SDK's `socialUrl`, the game's `VITE_SOCIAL_URL`) |
| `https://realtime.<domain>` | realtime: `/api/v1/...` and `wss://realtime.<domain>/api/v1/ws`; also the docker-compose gateway paths `/realtime/v1/<rest>` (rewritten to `/api/v1/<rest>`) and `/realtime/v1/healthz`, `/readyz` |

- **sslip.io** answers every `*.130-61-158-79.sslip.io` with `130.61.158.79`, so no DNS setup is needed. For an own
  domain: one DNS record `*.<domain>` -> `130.61.158.79`, then change `ingress.domain` (and the CORS origins).
- **HTTPS** (`ingress.tls.enabled`): cert-manager gets one Let's Encrypt certificate for the three hosts (Secret
  `clan-tls`) and renews it; `http://` answers with a permanent redirect to `https://`. Let's Encrypt checks the domain
  over plain http, so port 80 must stay open.
- **CORS:** `platform.corsOrigins` allows Studio (`https://studio.<domain>`), `social` and `realtime` the demo game
  (`https://game.<domain>`); both also allow the local dev servers.
- No access log: the WebSocket URL carries the player token.

**One-time setup on the server** (before the first install with `ingress.tls.enabled`):

```bash
# 1. cert-manager: gets and renews the certificates (its own namespace, cert-manager)
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s

# 2. Traefik's timeouts for the WebSocket (file copied from deploy/k3s/traefik-config.yaml)
kubectl apply -f ~/traefik-config.yaml
```

Without step 1, `helm upgrade` fails with `no matches for kind "Certificate"`.

**Check it** (from anywhere):

```bash
kubectl -n clan get certificate clan-tls             # READY True after about a minute
curl -s https://social.130-61-158-79.sslip.io/health  # {"service":"social","status":"ok"}
curl -sI http://social.130-61-158-79.sslip.io/health  # 308 Permanent Redirect to https://
```

**The certificate is not ready?** `kubectl -n clan describe certificate clan-tls` and
`kubectl -n clan get challenges` show why. While experimenting, set `ingress.tls.issuer: letsencrypt-staging`: its
certificates are not trusted by browsers, but its rate limits are much higher.

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

**4. Install or upgrade.** Usually you do not do this by hand any more: every merge to `main` deploys (see "Deploy"
below), and `deploy/deploy.sh` does the same from your laptop. The manual steps, for a first install or for debugging:

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

## Deploy

**Every merge to `main` is deployed.** The pipeline on `main` runs `test`, then `images` (pushes the images tagged with
the short commit SHA, for example `56bd0093`), then `deploy`, which runs `deploy/deploy.sh <sha>`. `develop` only tests
and builds images; it never touches the server. So a release is an MR `develop` -> `main`.

`deploy/deploy.sh` copies the chart, the secrets file and the two JWT keys to a private temporary folder on the server,
runs `helm upgrade --install --wait` there with `image.tag=<sha>`, prints the last revisions and the running images,
and always deletes the copies. Helm returns only when the new pods are ready; if they are not ready within 10 minutes
the deploy fails and the old pods keep serving.

**From your laptop** (Git Bash, not PowerShell's `bash`, which is WSL and has no access to your SSH key):

```bash
git rev-parse --short=8 origin/main        # the tag CI built for the newest main commit
bash deploy/deploy.sh 56bd0093             # any tag that exists in Deploy -> Container registry
```

It uses your own SSH key and your `secrets.prod.yaml` and `secrets/prod/*.pem`.

**Which version runs now:** `kubectl -n clan get deploy -o wide` (the IMAGE column ends with the tag), `helm history clan
-n clan` on the server, or GitLab -> Operate -> Environments -> production.

### Set up CI deploys (once)

The job reads five **Protected** CI/CD variables (clan-backend -> Settings -> CI/CD -> Variables), so only pipelines of
protected branches can read them: `main` must be protected (Settings -> Repository -> Protected branches). For every
variable: type Variable, environments All, Protected, and untick "Expand variable reference". In Git Bash, from the
repository root, each command copies a value to the clipboard:

| Variable | Value | Masked |
|---|---|---|
| `DEPLOY_SSH_KEY_B64` | `base64 -w0 ~/.ssh/clan_ci_deploy \| clip` | yes |
| `DEPLOY_SSH_KNOWN_HOSTS` | `ssh-keyscan -t ed25519 130.61.158.79 2>/dev/null \| clip` | no (it has spaces) |
| `DEPLOY_SECRETS_B64` | `base64 -w0 deploy/helm/clan/secrets.prod.yaml \| clip` | yes |
| `DEPLOY_JWT_PRIVATE_B64` | `base64 -w0 secrets/prod/platform_jwt_private.pem \| clip` | yes |
| `DEPLOY_JWT_PUBLIC_B64` | `base64 -w0 secrets/prod/platform_jwt_public.pem \| clip` | yes |

The CI has its own SSH key, so it can be revoked without touching anyone's personal key:

```bash
ssh-keygen -t ed25519 -N "" -C "gitlab-ci-deploy" -f ~/.ssh/clan_ci_deploy
cat ~/.ssh/clan_ci_deploy.pub | ssh ubuntu@130.61.158.79 'cat >> ~/.ssh/authorized_keys'
ssh -i ~/.ssh/clan_ci_deploy ubuntu@130.61.158.79 hostname     # prints clan-platform
```

Check the host key before you store it: `ssh-keyscan -t ed25519 130.61.158.79 2>/dev/null | ssh-keygen -lf -` must show
`SHA256:hxWH4UP41JFbBbrL9R4E26czaH91u0gin7mrR/DTqe4`.

**When a secret changes** (a password, a token, a key), update `secrets.prod.yaml` or the PEM file and the matching
variable; the next deploy applies it and restarts the pods. To revoke the CI key, delete its line (`gitlab-ci-deploy`)
from `~/.ssh/authorized_keys` on the server.

### Roll back

- **GitLab:** Operate -> Environments -> production -> an earlier deploy -> Re-deploy. It deploys that commit's images
  again (they stay in the registry).
- **Laptop:** `bash deploy/deploy.sh <older sha>`.
- **Helm, on the server:** `helm history clan -n clan`, then `helm rollback clan <revision> -n clan`. This restores the
  whole release as it was, including old settings.

Migrations are not rolled back: an older image runs against the newer tables. Our migrations only add (tables, columns,
nullable fields), which older code ignores, so this is safe. A migration that removes or renames something needs a plan
first.

### Logs and debugging

On the server (`ssh ubuntu@130.61.158.79`, then `export KUBECONFIG=/etc/rancher/k3s/k3s.yaml`), or on your laptop
through the tunnel:

```bash
kubectl -n clan get pods                           # status of everything
kubectl -n clan logs deploy/social --tail=100 -f   # a service's log, live (Ctrl+C stops)
kubectl -n clan logs deploy/social -c migrate      # its migrations
kubectl -n clan logs deploy/social --previous      # the log of the crashed container before a restart
kubectl -n clan describe pod <pod name>            # events: image pull errors, failed probes, OOMKilled
kubectl -n clan get events --sort-by=.lastTimestamp | tail -20
```

`ImagePullBackOff`: the tag does not exist or the registry token is wrong. `Init:Error`: a migration failed (see its
log). `CrashLoopBackOff`: the service stops right after starting (see `--previous`).

### Access for a teammate

1. They send you their **public** SSH key (`~/.ssh/id_ed25519.pub`), never the private one.
2. You add it on the server: `echo "ssh-ed25519 AAAA... name" >> ~/.ssh/authorized_keys` (on the server).
3. They can now `ssh ubuntu@130.61.158.79` and use `kubectl` there, or run `deploy/deploy.sh` (with their own copy
   of the secrets, which you give them privately).
4. For `kubectl` on their laptop: the access file `~/clan-oracle.yaml` on the server is k3s's
   `/etc/rancher/k3s/k3s.yaml` with the name `clan-oracle` and the tunnel port 16443 (create it once on the server:
   `sed -e 's/\bdefault\b/clan-oracle/g' -e 's#https://127.0.0.1:6443#https://127.0.0.1:16443#' /etc/rancher/k3s/k3s.yaml > ~/clan-oracle.yaml`).
   They copy it with `scp ubuntu@130.61.158.79:clan-oracle.yaml ~/.kube/clan-oracle.yaml`, add it to `KUBECONFIG`,
   and open the tunnel `ssh -N -o ServerAliveInterval=30 -L 16443:127.0.0.1:6443 ubuntu@130.61.158.79` whenever they
   use it. The file gives full control of the cluster: treat it like a password.

The server has one Linux user, so everyone with access can do everything. To take access away, delete their line from
`~/.ssh/authorized_keys`.

## Good to know

- **Telemetry:** with `telemetry.enabled: true` (set in `values-prod.yaml`) every service sends OpenTelemetry traces
  and metrics to the collector agent on its own node, at `http://<node IP>:4317`. Without an agent there, set it to
  `false`: the services run the same, but would log "telemetry export failed" warnings.
- **The collector on the Oracle server is not part of this chart.** An OpenTelemetry Collector was installed there
  separately (Helm release in the `datadog` namespace). It listens on the node's port 4317 and sends traces, metrics
  and the pods' logs to Datadog, so `values-prod.yaml` only sets `telemetry.enabled: true`.
  - **Check it:** `kubectl -n datadog get pods` and `kubectl -n datadog logs daemonset/<its name> --tail=50`.
  - **Its pipelines:** `kubectl -n datadog get configmap -o name`, then read the `relay` key of the collector's ConfigMap.
- **The chart's own collector** (`telemetry.collector.enabled`, `templates/otel-collector.yaml`) is for a cluster that
  has none. It is off by default and must stay off on the Oracle server: two collectors cannot share port 4317, and
  the second one's pod stays `Pending`. On another cluster, turn it on with `secrets.datadogApiKey` set (Datadog:
  Organization Settings -> API Keys) and `telemetry.collector.datadogSite` set to the account's site.
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

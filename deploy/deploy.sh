#!/usr/bin/env bash
# Deploys the clan Helm chart to the k3s server, with the images of one commit.
#
# It copies the chart, the secrets file and the two JWT keys to a fresh temporary folder on the
# server over SSH, runs `helm upgrade --install` there (next to the cluster, so a dropped connection
# cannot break the upgrade half way, and port 6443 never has to be public), waits until the new pods
# are ready, and always deletes the copies again.
#
# Usage, from anywhere in the repository (Git Bash on Windows, or the CI job "deploy"):
#   deploy/deploy.sh <image tag>          for example: deploy/deploy.sh a1b2c3d
# The tag is a short commit SHA (or a branch name such as develop) that the CI job "images" pushed.
#
# Settings (environment variables; the defaults fit a laptop with the files of the chart README):
#   DEPLOY_HOST          user@host of the server              (default ubuntu@130.61.158.79)
#   DEPLOY_SECRETS       the secrets file                     (default deploy/helm/clan/secrets.prod.yaml)
#   DEPLOY_JWT_PRIVATE   platform's private key, PEM          (default secrets/prod/platform_jwt_private.pem)
#   DEPLOY_JWT_PUBLIC    platform's public key, PEM           (default secrets/prod/platform_jwt_public.pem)
# ssh and scp use your normal SSH key and known_hosts; the CI job prepares its own.
set -euo pipefail

tag="${1:-}"
# The tag ends up in a command on the server, so only image-tag characters are accepted.
if [[ ! "$tag" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
  echo "usage: deploy/deploy.sh <image tag>   (a short commit SHA such as a1b2c3d, or develop)" >&2
  exit 2
fi

cd "$(dirname "$0")/.."   # the repository root

host="${DEPLOY_HOST:-ubuntu@130.61.158.79}"
secrets="${DEPLOY_SECRETS:-deploy/helm/clan/secrets.prod.yaml}"
jwt_private="${DEPLOY_JWT_PRIVATE:-secrets/prod/platform_jwt_private.pem}"
jwt_public="${DEPLOY_JWT_PUBLIC:-secrets/prod/platform_jwt_public.pem}"

for f in "$secrets" "$jwt_private" "$jwt_public"; do
  if [[ ! -s "$f" ]]; then
    echo "deploy: $f is missing or empty (see deploy/helm/clan/README.md)" >&2
    exit 1
  fi
done

echo "deploy: image tag $tag to $host"

# A folder only this user can read (mktemp -d creates it with mode 700).
remote_dir="$(ssh "$host" 'mktemp -d /tmp/clan-deploy.XXXXXX')"
cleanup() { ssh "$host" "rm -rf '$remote_dir'" || echo "deploy: could not remove $remote_dir on the server" >&2; }
trap cleanup EXIT

scp -q -r deploy/helm/clan "$host:$remote_dir/chart"
scp -q "$secrets" "$host:$remote_dir/secrets.yaml"
scp -q "$jwt_private" "$host:$remote_dir/jwt_private.pem"
scp -q "$jwt_public" "$host:$remote_dir/jwt_public.pem"

# On the server. --wait: helm returns only when the new pods are ready (or fails after 10 minutes,
# and the old pods keep serving).
ssh "$host" bash -s -- "$remote_dir" "$tag" <<'REMOTE'
set -euo pipefail
dir="$1"
tag="$2"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
command -v helm >/dev/null || { echo "deploy: helm is not installed on the server (see the chart README)" >&2; exit 1; }

helm upgrade --install clan "$dir/chart" -n clan --create-namespace --wait --timeout 10m \
  -f "$dir/chart/values-prod.yaml" -f "$dir/secrets.yaml" \
  --set image.tag="$tag" \
  --set-file secrets.jwtPrivateKey="$dir/jwt_private.pem" \
  --set-file secrets.jwtPublicKey="$dir/jwt_public.pem"

echo
helm history clan -n clan --max 3
echo
kubectl -n clan get deploy -o custom-columns='NAME:.metadata.name,READY:.status.readyReplicas,IMAGE:.spec.template.spec.containers[0].image'
REMOTE

echo "deploy: done, $tag is running"

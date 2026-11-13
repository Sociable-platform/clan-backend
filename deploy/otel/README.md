# OpenTelemetry Collector

Sends this cluster's metrics, logs and (later) traces to Datadog. It is a separate Helm
release from the `clan` chart, so `deploy/deploy.sh` and every production deploy leave it
alone; it is installed and upgraded by hand from the server.

Our services speak plain OTLP and know nothing about Datadog. Moving to another backend
means changing the `datadog` exporter in `values.yaml` and nothing else.

## What it collects

| From | Needs a service change? |
| --- | --- |
| The node: cpu, memory, disk, filesystem, network | no |
| Per pod and per container metrics (kubelet) | no |
| Every container's stdout, from `/var/log/pods` | no |
| Traces and metrics our services send over OTLP | yes, see "Sending OTLP" below |

## One-time setup

The API key is a Kubernetes secret in the `datadog` namespace; the Collector runs there so
it can read it. To create or replace it (the key is never typed on the command line, so it
does not reach the shell history or the process list):

```bash
read -rsp 'Datadog API key: ' DD_KEY; echo
kubectl create namespace datadog
printf '%s' "$DD_KEY" | kubectl -n datadog create secret generic datadog-secret \
  --from-file=api-key=/dev/stdin --dry-run=client -o yaml | kubectl apply -f -
unset DD_KEY
```

Check that the right key went in, without printing it:

```bash
kubectl -n datadog get secret datadog-secret -o jsonpath='{.data.api-key}' | base64 -d | tail -c 5; echo
```

The last characters must match the ones Datadog shows for the key.

## Install

From a copy of this folder on the server:

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl top nodes        # write these down: the node is small and already busy
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update
helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
  -n datadog -f values.yaml
```

## Check it worked

```bash
kubectl -n datadog get pods                        # Running, no restarts
kubectl -n datadog logs ds/otel-collector | head -40
kubectl top nodes                                  # compare with before
kubectl get pods -A                                # nothing evicted or restarted
```

The logs are where a mistake shows first: a component named in `service.pipelines` that the
image does not have, or a preset that was not enabled, stops the Collector at startup with a
clear message. `memory_limiter` must stay first in each processor list.

In Datadog: the node appears under **Infrastructure → Host Map**, and container output under
**Logs**. Metrics take a minute or two.

## Sending OTLP from our services

The Collector listens on the node, port 4317 (gRPC). A service reaches it through the node's
own address, which the pod reads from the downward API:

```yaml
env:
  - name: HOST_IP
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: http://$(HOST_IP):4317
```

Without that variable a service sends nothing, which is what we want locally and in tests.

## Removing it

```bash
helm uninstall otel-collector -n datadog
```

The secret and the namespace stay; delete them separately if the account is going away.

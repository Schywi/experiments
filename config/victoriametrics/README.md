# VictoriaMetrics (Cilium/Hubble metrics store)

This chart runs a single-node VictoriaMetrics instance that is both the metrics
store and the scraper for the Cilium/Hubble metrics enabled in
`config/helm/cilium/values.yaml` and `config/k3s/cilium-values.yaml`.

There is deliberately **no Prometheus Operator** in this lab, so the chart does
not use `ServiceMonitor`/`VMServiceScrape`. Instead VictoriaMetrics scrapes the
Cilium components itself through `-promscrape.config` with
`kubernetes_sd_configs`:

| Job | Selected pod | Port | Metrics source |
| --- | --- | --- | --- |
| `cilium-agent` | `k8s-app=cilium` (kube-system) | 9962 | `prometheus.enabled` datapath metrics |
| `cilium-hubble` | `k8s-app=cilium` (kube-system) | 9965 | `hubble.metrics.enabled` flow metrics |
| `cilium-operator` | `io.cilium/app=operator` (kube-system) | 9963 | `operator.prometheus` metrics |
| `hubble-relay` | `k8s-app=hubble-relay` (kube-system) | 9966 | `hubble.relay.prometheus` metrics |

The `cilium-agent` runs with `hostNetwork`, so its metrics are reached through
the host entity; the Cilium network policy in this chart allows both the pod and
host paths.

> **Prerequisite:** the running Cilium must be installed with the values in
> `config/helm/cilium/values.yaml` (or `config/k3s/cilium-values.yaml`). Cilium
> only starts exposing `:9962` (agent), `:9965` (Hubble agent), and `:9966`
> (relay) after that release is applied, so re-run
> `config/k3s/install-cilium.sh` after changing those values. You can confirm
> from VictoriaMetrics with
> `curl http://victoriametrics:8428/api/v1/query?query=up` — the `cilium-agent`,
> `cilium-hubble`, and `hubble-relay` series must be `1`.

## Query surface

- Prometheus-compatible API for Grafana:
  `http://victoriametrics.<namespace>.svc.cluster.local:8428`
- vmui UI via the Cilium Ingress Controller: `http://victoriametrics.localhost/`
  (after adding the host to `/etc/hosts`, see `scripts/hosts-entries.sh`).

## Install

Installed by Argo CD through `config/argocd/applications/victoriametrics.yaml`
into the `observability` namespace, which also hosts the Grafana chart:

```bash
kubectl apply -f config/argocd/applications/victoriametrics.yaml
```

## Storage

Metrics are retained for `retentionPeriod` (default `30d`) on a
`ReadWriteOnce` PVC (`storage.size`, default `5Gi`). The store uses the
`Recreate` strategy so the PVC is never mounted twice.

Because VictoriaMetrics tables the Cilium/Hubble series, Grafana dashboards
(`config/grafana`) resolve their panels against this store through the
provisioned Prometheus datasource.

# Grafana (Cilium/Hubble dashboards)

This chart runs Grafana with a **provisioned** Prometheus datasource pointing at
the VictoriaMetrics store in `config/victoriametrics`, plus the official
Cilium/Hubble dashboards.

There is no Prometheus Operator and no Grafana dashboard sidecar in this lab.
Dashboards are baked into a ConfigMap and mounted at `/var/lib/grafana/dashboards`
by Grafana's file provisioning provider.

## Dashboard provenance

The six JSON files in `dashboards/` are the upstream Cilium dashboards for the
pinned release (`CILIUM_VERSION=1.20.1`), taken verbatim from
`install/kubernetes/cilium/files/**/dashboards/`:

| File | Upstream source |
| --- | --- |
| `cilium-dashboard.json` | `cilium-agent/dashboards/cilium-dashboard.json` |
| `cilium-operator-dashboard.json` | `cilium-operator/dashboards/cilium-operator-dashboard.json` |
| `hubble-dashboard.json` | `hubble/dashboards/hubble-dashboard.json` |
| `hubble-dns-namespace.json` | `hubble/dashboards/hubble-dns-namespace.json` |
| `hubble-l7-http-metrics-by-workload.json` | `hubble/dashboards/hubble-l7-http-metrics-by-workload.json` |
| `hubble-network-overview-namespace.json` | `hubble/dashboards/hubble-network-overview-namespace.json` |

Each file was normalized for file-based provisioning (the `__inputs`/`__requires`
import blocks are removed, the redundant `DS_PROMETHEUS` datasource template
variable is dropped, and `${DS_PROMETHEUS}` datasource references are bound to the
provisioned datasource UID `victoriametrics`). Regenerate from a reviewed Cilium
release with:

```bash
tag=v1.20.1
base="https://raw.githubusercontent.com/cilium/cilium/${tag}/install/kubernetes/cilium/files"
for f in cilium-agent/dashboards/cilium-dashboard.json \
         cilium-operator/dashboards/cilium-operator-dashboard.json \
         hubble/dashboards/hubble-dashboard.json \
         hubble/dashboards/hubble-dns-namespace.json \
         hubble/dashboards/hubble-l7-http-metrics-by-workload.json \
         hubble/dashboards/hubble-network-overview-namespace.json; do
  curl -fsSL "${base}/${f}" |
    jq 'del(.__inputs, .__requires)
        | if (.templating.list? | type) == "array"
          then .templating.list |= map(select(.name != "DS_PROMETHEUS"))
          else . end' |
    sed 's/\${DS_PROMETHEUS}/victoriametrics/g' \
      > "config/grafana/dashboards/$(basename "${f}")"
done
```

## Data flow

```
Cilium agent / operator / Hubble relay  --(scrape)-->  victoriametrics  <--(query)--  grafana
```

The dashboards resolve their panels against the `victoriametrics` datasource, so
the charts in the screenshot (Networking Behavior, Network Policy Observation,
HTTP Request/Response rate and latency, DNS request/response) come from the
Cilium/Hubble metrics enabled in the Cilium values files.

## No authentication

Grafana runs with anonymous access mapped to the **Admin** role and the
basic-auth login form disabled (`GF_AUTH_ANONYMOUS_ENABLED`,
`GF_AUTH_ANONYMOUS_ORG_ROLE=Admin`, `GF_AUTH_BASIC_ENABLED=false`,
`GF_AUTH_DISABLE_LOGIN_FORM=true`). This mirrors the headless Argo CD and
unauthenticated Neo4j already in the repository and keeps credentials out of the
public repository. It is limited to the private local cluster.

## Access

- Through the Cilium Ingress Controller: `http://grafana.localhost/`
  (add the host to `/etc/hosts`, see `scripts/hosts-entries.sh`).
- In-cluster: `http://grafana.<namespace>.svc.cluster.local:3000`.

## Install

Installed by Argo CD through `config/argocd/applications/grafana.yaml` into the
`observability` namespace:

```bash
kubectl apply -f config/argocd/applications/grafana.yaml
```

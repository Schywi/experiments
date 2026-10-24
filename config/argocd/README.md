# Argo CD bootstrap

This directory installs Argo CD from the official `argo/argo-cd` Helm chart.
The public repository is the source of truth once changes are pushed:

```bash
config/argocd/install.sh
```

The default values deliberately provide the requested headless/no-auth Argo CD
mode while keeping the server Service internal to the k3d cluster:

- `server.insecure` is explicitly `"true"`.
- `server.disable.auth` is explicitly `"true"`.
- the built-in admin account is disabled.
- the server service is `ClusterIP`; local access is provided by the Cilium
  Ingress Controller using `argocd.localhost`.
- no repository credentials or other secrets are stored here.

This mode is intentionally limited to the private k3d development cluster.
The Cilium Ingress listener is bound by k3d to `127.0.0.1:8080`; it is not a
public LoadBalancer or host-wide binding.

The Argo route is defined in `argocd-ingress.yaml` and is applied alongside
the Hubble route by `config/k3d/install-cilium.sh`. Open
`http://argocd.localhost:8080/` from the local machine.

The Vector data-plane application is defined separately in
`applications/vector.yaml`. It renders `config/vector` into the `worm-lab`
namespace and uses the same public repository source, automated pruning, and
self-healing policy.

The observability stack is defined in `applications/victoriametrics.yaml` and
`applications/grafana.yaml`. They render `config/victoriametrics` and
`config/grafana` into the `observability` namespace from the public repository
`native-k3s-gpu-models`, with automated pruning and self-healing. Apply them after bootstrap:

```bash
kubectl apply -f config/argocd/applications/victoriametrics.yaml
kubectl apply -f config/argocd/applications/grafana.yaml
```

The bounded Worm stable handoff is defined separately under
`applications/stable/`. Those four Applications pin a full Git revision and
require an explicit manual sync. Register that directory only after choosing
Argo ownership and stopping the Tilt Worm resources; never register it
together with `applications/vector.yaml`, which describes the same Vector
resources.

## Unpushed local source mode

Argo CD cannot read a developer's host filesystem directly. Until a change is
pushed, use the local checkout explicitly with Helm/Tilt and do not pretend it
is reconciled from Git:

For a true local Argo CD source, serve a read-only branch from a Git HTTP
endpoint reachable by the cluster. Do not commit a host path; restore the
public URL before returning to normal GitOps operation.

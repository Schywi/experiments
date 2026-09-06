# Argo CD bootstrap

This directory installs Argo CD from the official `argo/argo-cd` Helm chart.
The public repository is the source of truth once changes are pushed:

```bash
config/argocd/install.sh
```

The default values deliberately provide the requested headless/no-auth Argo CD
mode while keeping the server private to the k3d cluster:

- `server.insecure` is explicitly `"true"`.
- `server.disable.auth` is explicitly `"true"`.
- the built-in admin account is disabled.
- the server service is `ClusterIP` and its ingress is disabled.
- no repository credentials or other secrets are stored here.

This mode is intentionally limited to the private k3d development cluster. Do
not expose this service through a public LoadBalancer, ingress, or host-wide
port binding.

The Vector data-plane application is defined separately in
`applications/vector.yaml`. It renders `config/vector` into the `worm-lab`
namespace and uses the same public repository source, automated pruning, and
self-healing policy.

## Unpushed local source mode

Argo CD cannot read a developer's host filesystem directly. Until a change is
pushed, use the local checkout explicitly with Helm/Tilt and do not pretend it
is reconciled from Git:

For a true local Argo CD source, serve a read-only branch from a Git HTTP
endpoint reachable by the cluster. Do not commit a host path; restore the
public URL before returning to normal GitOps operation.

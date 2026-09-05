# Argo CD bootstrap

This directory installs Argo CD from the official `argo/argo-cd` Helm chart
and defines the GitOps application for the OpenResty Cilium dashboard chart.
The public repository is the source of truth once changes are pushed:

```bash
config/argocd/install.sh
kubectl apply -f config/argocd/applications/openresty.yaml
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

The application reconciles `https://github.com/Schywi/experiments.git` at
`main`, with `config/openresty` rendered as a Helm chart into the `openresty`
namespace. The chart path is expected to exist on the selected revision.

## Unpushed local source mode

Argo CD cannot read a developer's host filesystem directly. Until a change is
pushed, use the local checkout explicitly with Helm/Tilt and do not pretend it
is reconciled from Git:

```bash
helm upgrade --install cilium-dashboard ./config/openresty \
  --namespace openresty --create-namespace
```

After pushing the change to `main`, apply the committed Application manifest
so Argo CD takes ownership of the same release. For a true local Argo CD
source, serve a read-only branch from a Git HTTP endpoint reachable by the
cluster, then apply a temporary copy of `applications/openresty.yaml` with
that endpoint as `spec.source.repoURL` and the local branch as
`spec.source.targetRevision`. Do not commit that temporary manifest or a host
path; restore the public URL before returning to normal GitOps operation.

# Argo CD bootstrap

This directory installs Argo CD from the official `argo/argo-cd` Helm chart
and defines the GitOps application for the OpenResty Cilium dashboard chart.
The public repository is the source of truth once changes are pushed:

```bash
config/argocd/install.sh
kubectl apply -f config/argocd/applications/openresty.yaml
```

The default values deliberately keep the Argo CD server private and
authenticated:

- `server.insecure` is explicitly `"false"`.
- `server.disable.auth` is explicitly `"false"`.
- the server service is `ClusterIP` and its ingress is disabled.
- no repository credentials or other secrets are stored here.

The application reconciles `https://github.com/Schywi/experiments.git` at
`main`, with `config/openresty` rendered as a Helm chart into the `openresty`
namespace. The chart path is expected to exist on the selected revision.

## Local-only authentication override

Anonymous/no-auth mode is only for a disposable, private development cluster.
Create a local ignored file named `config/argocd/secrets-local-values.yaml`
(the repository's `secrets*.yaml` rule ignores it) when this mode is required:

```yaml
configs:
  params:
    server.insecure: "true"
    server.disable.auth: "true"
server:
  service:
    type: ClusterIP
  ingress:
    enabled: false
```

Then install it explicitly:

```bash
ARGOCD_VALUES_FILE=config/argocd/secrets-local-values.yaml config/argocd/install.sh
```

Never use that override with a LoadBalancer, Ingress, port-forward shared
beyond the developer's private network, or any publicly reachable cluster.
The file must remain untracked; it contains configuration that removes the
authentication boundary even though it contains no credential.

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


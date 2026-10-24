# Kargo

Promotion controller for this cluster: it watches artifact repositories, turns
new artifacts into Freight, and moves Freight between Stages by committing to
Git and pointing Argo CD at the result.

Installed from the official chart, pinned:

| | |
|---|---|
| Chart | `oci://ghcr.io/akuity/kargo-charts/kargo` `1.12.0` |
| Namespace | `kargo` |
| Components | `kargo-api`, `kargo-controller`, `kargo-management-controller`, `kargo-webhooks-server` |
| TLS | local CA from `gen-certs.sh`; **no cert-manager** |
| Auth | **disabled** — `adminAccount.enabled: false`, no OIDC |
| API exposure | `ClusterIP` + port-forward |

## Why no cert-manager

The chart signs the API and webhooks certs with cert-manager `Certificate`
resources when `selfSignedCert` is true. This node has memory limits at 89% of
15.5 GiB, so `gen-certs.sh` signs both certs with a local CA and loads them as
plain TLS Secrets, and `install.sh` passes the CA as
`webhooksServer.tls.caBundle` so the Kubernetes API server trusts the webhook.

## Install

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/kargo/install.sh
```

Certs default to `${XDG_DATA_HOME:-$HOME/.local/share}/kargo/certs`; override
with `KARGO_CERT_DIR`. Nothing is written into the repository.

## The API refuses unauthenticated callers

Kargo has no anonymous mode. With the admin account disabled and no OIDC
provider, every API request is refused — verified on this install:

```
GET /v1beta1/projects -> 500 {"error":"internal server error"}
kargo-api log:        handled request: not allowed {"actor": "unknown actor"}
```

The controller, the reconcilers and promotions are unaffected: Freight
discovery, auto-promotion and Promotions read from the cluster, not the API.
What does not work in this mode is the `kargo` CLI and the web UI. To get them
back, set `api.adminAccount.enabled: true` in `values.yaml` with a generated
`passwordHash` and `tokenSigningKey`, reinstall, then `kargo login --admin`
once.

## Git credentials

Promotion steps push to `https://github.com/Schywi/experiments.git`, so a Stage
needs a token. It is created out of band and never committed:

```bash
GITHUB_TOKEN="$(gh auth token)" config/kargo/git-credentials.sh
```

That writes Secret `github-credentials` in the `pilot` namespace, labelled
`kargo.akuity.io/cred-type: git`, which is how Kargo matches it to the repo URL.

## Pilot: `pilot` project

A busybox `httpd` proves the pipeline end to end without touching any existing
workload: public image, immutable tags, nothing to build.

```
config/kargo/pilot/project.yaml          Project pilot (namespace pilot)
config/kargo/pilot/projectconfig.yaml    auto-promotion policy for staging
config/kargo/pilot/warehouse.yaml        image busybox + git subscription
config/kargo/pilot/stage-staging.yaml    auto-promotes, commits to the overlay
config/kargo/pilot/stage-prod.yaml       manual, only freight that passed staging
config/kargo/pilot/app/                  kustomize base + staging/prod overlays
config/argocd/applications/kargo-pilot-{staging,prod}.yaml
```

Each promotion step list is: `git-clone` → `kustomize-set-image` → `git-commit`
→ `git-push` → `argocd-update` (pins `targetRevision` to the commit and waits for
it to be Synced/Healthy at that revision).

Inspect and promote without the API:

```bash
kubectl -n pilot get warehouses,freights,stages,promotions
kubectl -n pilot create -f - <<'EOF'
apiVersion: kargo.akuity.io/v1alpha1
kind: Promotion
metadata:
  generateName: prod-
  namespace: pilot
spec:
  stage: prod
  freight: <freight-name>
EOF
```

## Verify

```bash
kubectl -n pilot get freight                      # one per discovered artifact set
kubectl -n kargo-pilot-staging get deploy pilot -o jsonpath='{..image}'
kubectl -n argocd get app kargo-pilot-staging -o jsonpath='{.spec.source.targetRevision}{"\n"}'
kubectl -n pilot get promotions
```

## What this install did (2026-10-24)

Four pods in `kargo`, ~80 MiB of memory in total (`kubectl top pod -n kargo`:
api 17Mi, controller 26Mi, management controller 20Mi, webhooks server 17Mi,
plus a nightly garbage-collector CronJob).

Freight `4c645039bc3a43178be4138cc2ae0ce2d160a0bb` was discovered from
`docker.io/library/busybox` (SemVer, `>=1.36.0 <1.39.0`, `^1\.[0-9]+\.[0-9]+$`)
and the branch. Staging auto-promoted, prod needed a manual `Promotion`, and
both ended as commits in this repository:

```
5c5d204 chore(kargo): promote busybox 1.37.0 to staging
ced5f89 chore(kargo): promote busybox 1.37.0 to prod
```

`kargo-pilot-staging` and `kargo-pilot-prod` are `Synced/Healthy` at those
commits, `Stage staging` and `Stage prod` report `Freight has been verified`,
and `config/kargo/pilot` itself is reconciled by the `kargo-pilot-resources`
Application, so the CRs are GitOps-managed and not hand-applied.

Two things worth knowing before relying on this:

- The first artifact discovery of `docker.io/library/busybox` took minutes, not
  seconds: the SemVer strategy lists the tag set and reads metadata per
  candidate. A handful of tags (`^1\.37\.0$`) answered immediately. Narrow
  `allowTagsRegexes` if discovery latency matters.
- Argo CD will not resolve a branch that has never been pushed. `targetRevision:
  feat/kargo` stays `Unknown` until `origin/feat/kargo` exists.

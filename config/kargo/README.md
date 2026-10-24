# Kargo

Promotion controller for this cluster: it watches artifact repositories, turns
new artifacts into Freight, and moves Freight between Stages by committing to
Git and pointing Argo CD at the result.

| | |
|---|---|
| Chart | `oci://ghcr.io/akuity/kargo-charts/kargo` `1.12.0` |
| Namespace | `kargo` |
| Components | `kargo-api`, `kargo-controller`, `kargo-management-controller`, `kargo-webhooks-server`, `kargo-gateway` |
| UI / API | **http://192.168.0.249/** via `kargo-gateway` (Cilium LB-IPAM, lan-pool) |
| Login | proxied: nobody types a password, the gateway injects a token |
| TLS | local CA from `gen-certs.sh`; **no cert-manager** |

## Install

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/kargo/install.sh
```

The script does three things: signs the API and webhooks certs with a local CA
(`gen-certs.sh`), creates the admin credentials (`gen-admin-secret.sh`), then
installs the chart and registers the `kargo-gateway` Application.

Certs default to `${XDG_DATA_HOME:-$HOME/.local/share}/kargo/certs`; override
with `KARGO_CERT_DIR`. Nothing generated is written into the repository.

## Why no cert-manager

The chart signs the API and webhooks certs with cert-manager `Certificate`
resources when `selfSignedCert` is true. This node has memory limits at 89% of
15.5 GiB, so `gen-certs.sh` signs both certs itself, loads them as plain TLS
Secrets, and `install.sh` passes the CA as `webhooksServer.tls.caBundle` so the
Kubernetes API server trusts the webhook endpoint.

## Login: proxied, not anonymous

Kargo has no anonymous mode. With `adminAccount.enabled: false` every request is
refused — measured on this install:

```
GET /v1beta1/projects -> 500 {"error":"internal server error"}
kargo-api log:        handled request: not allowed {"actor": "unknown actor"}
```

So the admin account exists, with a generated password and signing key in
Secret `kargo-admin-credentials` (created by `gen-admin-secret.sh`, out of band,
`api.secret.name` in `values.yaml`). The password is never typed:

```
browser -> kargo-gateway:80 -> POST /v1beta1/login  Authorization: Bearer <password>
                            <- {"idToken": "..."}
                            -> GET /v1beta1/...  Authorization: Bearer <idToken>
                            -> kargo-api.kargo.svc:443
```

`kargo-gateway` is a ~120-line Python proxy (`gateway/proxy.py`) run from a
ConfigMap on `python:3.13-alpine` — no image to build. It logs in once at
startup, injects the token into every request, and re-logs in on a 401. The
admin token TTL is `8760h`, so the UI does not bounce to a login form.

Consequences worth stating: anyone who can reach `192.168.0.249` gets admin on
Kargo, which can commit and push to this repository through the credential in
`git-credentials.sh`. That is the same LAN posture as Neo4j and Grafana, and it
is why the Service is a LAN `LoadBalancer` and not exposed further. Deleting the
gateway returns a login prompt, not an open API.

The proxy buffers response bodies, so live log streaming through it does not
work; the JSON REST API and the UI assets do.

## Address

`kargo-gateway` pins `io.cilium/lb-ipam-ips: 192.168.0.249` from `lan-pool`
(`192.168.0.240-250`). `240` ingress, `241` hubble, `242` cartography, `243`
assistant, `244` fuwa, `245` grafana, `246` victoriametrics, `247` argocd, `249`
kargo.

`.248` is **not** free: a `kargo-gateway` Service was briefly applied into the
`default` namespace without a `namespace:` field, Cilium's LB-IPAM allocated
`.248` to `default/kargo-gateway`, and the allocation did not come back when
that object was deleted (`lan-pool` reports 9 used, 2 available). The manifests
now carry `namespace: kargo`.

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
config/kargo/gateway/                    the login proxy, Service and ConfigMap
config/argocd/applications/kargo-{pilot-staging,pilot-prod,pilot-resources,gateway}.yaml
```

Each promotion step list is: `git-clone` → `kustomize-set-image` → `git-commit`
→ `git-push` → `argocd-update` (pins `targetRevision` to the commit and waits for
it to be Synced/Healthy at that revision).

Both the Kargo CRs (`config/kargo/pilot`) and the gateway (`config/kargo/gateway`)
are reconciled by Applications, so they follow the same GitOps rule as the rest
of `config/`.

## Verify

```bash
curl -s http://192.168.0.249/v1beta1/projects | head -c 200 # JSON, no credentials
kubectl -n pilot get freight,stages,promotions
kubectl -n kargo-pilot-staging get deploy pilot -o jsonpath='{..image}'
kubectl -n argocd get app -l app.kubernetes.io/part-of=kargo-pilot
```

## What this install did (2026-10-24)

Five pods in `kargo`, ~100 MiB of memory in total (`kubectl top pod -n kargo`:
api 17Mi, controller 26Mi, management controller 20Mi, webhooks server 17Mi,
gateway ~25Mi, plus a nightly garbage-collector CronJob).

Freight `4c645039bc3a43178be4138cc2ae0ce2d160a0bb` was discovered from
`docker.io/library/busybox` (SemVer, `>=1.36.0 <1.39.0`, `^1\.[0-9]+\.[0-9]+$`)
and the branch. Staging auto-promoted, prod needed a manual `Promotion`, and
both ended as commits in this repository:

```
5c5d204 chore(kargo): promote busybox 1.37.0 to staging
ced5f89 chore(kargo): promote busybox 1.37.0 to prod
```

`kargo-pilot-staging` and `kargo-pilot-prod` are `Synced/Healthy` at those
commits, `Stage staging` and `Stage prod` report `Freight has been verified`.

Through the gateway, with no credentials in the request:

```
GET /                          -> 200 text/html, <title>Kargo</title>
GET /assets/index-*.js         -> 200, 8.3 MB
GET /v1beta1/projects          -> 200 {"items":[{"kind":"Project",...}]}
GET /v1beta1/projects/pilot/stages -> 200
```

Two things worth knowing before relying on this:

- The first artifact discovery of `docker.io/library/busybox` took minutes, not
  seconds: the SemVer strategy lists the tag set and reads metadata per
  candidate. A narrower `allowTagsRegexes` answered immediately.
- Argo CD will not resolve a branch that has never been pushed. `targetRevision:
  feat/kargo` stays `Unknown` until `origin/feat/kargo` exists.

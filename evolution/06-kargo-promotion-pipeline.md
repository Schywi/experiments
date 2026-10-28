# Phase 6 — promotion with Kargo, and a planner that reads the whole sentence (2026-10-24 → )

**Branch:** `feat/kargo`, forked from `cluster-evolution` at `2404f70` · **tracked
paths:** [`../config/kargo/`](../config/kargo/README.md),
[`../apps/assistant/`](../apps/assistant/README.md),
[`../research/features/assistant/`](../research/features/assistant/README.md) ·
**Applications added:** `kargo-gateway`, `kargo-pilot-resources`,
`kargo-pilot-staging`, `kargo-pilot-prod`

## The question

By the end of Phase 5 the cluster reconciled itself from git: Argo CD reads a
branch, and whatever the branch says is what runs. The gap was on the other side
of the loop. A new image tag, a new artifact, a change that has to pass staging
before it reaches production — all of that was a human editing a manifest and
pushing.

Kargo is the piece that moves an artifact between Stages by committing to git and
pinning Argo CD at the resulting commit. The question this phase answers:
**can the promotion step itself be a commit, with the same review and the same
reversibility as the rest of the platform?**

The assistant work in the same branch answers a narrower question. Its keyword
router returned the first match in a sentence, so "any dropped packets in the
worm-lab namespace, and take a screenshot of browser-pool-xnxp" answered one of
the two asks and silently discarded the other, depending on which tuple matched
first. One LLM call that reads the sentence and returns the list of tasks
replaces the router.

## What exists, and where it lives

| Piece | Path | Reconciled by |
|---|---|---|
| Kargo install script and values | `config/kargo/{install.sh,values.yaml,gen-certs.sh,gen-admin-secret.sh}` | run once by hand (`install.sh`) |
| Login proxy in front of the UI | `config/kargo/gateway/` | Application `kargo-gateway` |
| Pilot Warehouse, Stages, overlay base | `config/kargo/pilot/` | Application `kargo-pilot-resources` |
| Pilot workload, per-stage overlay | `config/kargo/pilot/app/` | `kargo-pilot-staging`, `kargo-pilot-prod` |
| Git credential for promotion pushes | `config/kargo/git-credentials.sh` | out of band, never committed |

Kargo runs from `oci://ghcr.io/akuity/kargo-charts/kargo` `1.12.0` in namespace
`kargo`; the UI and API are served from `192.168.0.249` through `kargo-gateway`,
a Cilium LB-IPAM address from `lan-pool` (`192.168.0.240-250`).

## Install without cert-manager

The chart signs its API and webhooks certificates with cert-manager
`Certificate` objects when `selfSignedCert` is true. This host has no
cert-manager, and adding one for two certificates on a node whose memory limit
sits at 89% of 15.5 GiB (`config/kargo/README.md`) costs more than it returns.

`gen-certs.sh` signs both certificates with a local CA and loads them as plain
TLS Secrets in `${XDG_DATA_HOME:-$HOME/.local/share}/kargo/certs`. `install.sh`
passes the CA to the chart as `webhooksServer.tls.caBundle` so the API server
trusts the webhook endpoint. Nothing generated is written into the repository.

## Login: a proxy, because Kargo has no anonymous mode

With `adminAccount.enabled: false` every request is refused. Measured on this
install:

```
GET /v1beta1/projects -> 500 {"error":"internal server error"}
kargo-api log:        handled request: not allowed {"actor": "unknown actor"}
```

So the admin account exists — password and signing key generated into Secret
`kargo-admin-credentials` by `gen-admin-secret.sh` — and the password is never
typed:

```
browser -> kargo-gateway:80 -> POST /v1beta1/login  Authorization: Bearer <password>
                            <- {"idToken": "..."}
                            -> GET /v1beta1/...  Authorization: Bearer <idToken>
                            -> kargo-api.kargo.svc:443
```

`gateway/proxy.py` is a ~140-line Python proxy run from a ConfigMap on
`python:3.13-alpine`, so there is no image to build. It logs in at startup,
injects the token into every request and re-logs in on a `401`. The admin token
TTL is `8760h`.

The UI decides whether to show a login form by looking for `auth_token` in
`localStorage` rather than by asking the API, so the gateway also rewrites the
served HTML with a bootstrap script that sets `auth_token` and `refresh_token`
before the bundle runs (`48f5775`, `459c980`). After a gateway change the old
page holds no token and the bundle is cached: one hard reload is part of the
procedure.

## The pilot, end to end

A busybox `httpd` proves the pipeline without touching any existing workload:
public image, immutable SemVer tags, nothing to build.

```
config/kargo/pilot/project.yaml          Project pilot (namespace pilot)
config/kargo/pilot/projectconfig.yaml    auto-promotion policy for staging
config/kargo/pilot/warehouse.yaml        image busybox + git subscription
config/kargo/pilot/stage-staging.yaml    auto-promotes, commits to the overlay
config/kargo/pilot/stage-prod.yaml       manual, only freight that passed staging
config/kargo/pilot/app/                  kustomize base + staging/prod overlays
```

Each stage's step list is `git-clone` → `kustomize-set-image` → `git-commit` →
`git-push` → `argocd-update`, which pins `targetRevision` to the commit it just
produced and waits for the Application to be `Synced/Healthy` at that revision.
The credential for the push is Secret `github-credentials` in the `pilot`
namespace, labelled `kargo.akuity.io/cred-type: git`, created out of band from
`gh auth token` and never committed.

Freight `4c645039bc3a43178be4138cc2ae0ce2d160a0bb` was discovered from
`docker.io/library/busybox` (SemVer `>=1.36.0 <1.39.0`, `^1\.[0-9]+\.[0-9]+$`)
and from the branch. Staging auto-promoted and production needed a manual
`Promotion`. Both ended as commits in this repository, which is the property the
phase set out to get:

```
5c5d204 chore(kargo): promote busybox 1.37.0 to staging
ced5f89 chore(kargo): promote busybox 1.37.0 to prod
```

`kargo-pilot-staging` and `kargo-pilot-prod` are `Synced/Healthy` at those
commits and both Stages report `Freight has been verified`.

## The assistant: one call decides what is being asked

The defect, from `pipeline.py` `plan()` on the Qwen path:

```python
if any(p in lowered for p in _SANDBOX_ROUTE):     # "screenshot" is in this tuple
    sandbox = _sandbox_batch(lowered, question)
    if sandbox:
        return sandbox, "route:sandbox", 0.0      # the rest of the sentence is gone
```

One sentence can carry two requests and the router returns one. `4ca2dd5` puts
an evaluator in front: a single LLM call that reads the sentence and returns

```json
{ "tasks": [ { "tool": "query_prometheus", "why": "dropped packets in worm-lab" },
             { "tool": "sandbox_shot",     "why": "screenshot of browser-pool-xnxp" } ] }
```

The evaluator decides *what is being asked*; it never decides how a tool
behaves. Each tool keeps its own specialist, including the PromQL writer that
repairs its query once (`67b1c9e`), argument filling for the values the evaluator
omits (`b7abb74`), and a single retry when the evaluator answers in prose
(`f10c458`).

## Recipes: ordered chains with a handoff

A task list cannot express "open the URL the last step produced". A recipe can,
and it is a Python function over a dict — no framework:

```
sentence
  └─ evaluator (1 LLM call) ──> { "recipe": "observe_url", "args": { "promql_hint": … } }
        └─ recipe (deterministic, no LLM)
             1. query_prometheus(promql)   -> fact, url = grafana link
             2. create_sandbox(kind=claim) -> id
             3. sandbox_open(id, url)      -> navigated
             4. sandbox_shot(id)           -> frame, deep_link
                   └─> correlate (1 LLM call) -> answer with the frame in it
```

The state a recipe carries is `{question, args, facts, url}`; `url` is the
handoff, and it comes from a link the platform already produced, so no pod-IP
lookup and no address mapping enters the assistant (`e58ee40`, `5cbeab4`).

## The sandbox specialist is a client

`apps/assistant/sandbox.py` is an HTTP client over the console API
(`CONSOLE_URL`, default `http://192.168.0.250`). Routing, readiness, the CDP
token and frame freshness belong to the console; the assistant reports what the
API returns and does not recompute routability. Tools exposed to the model:
`sandbox_list`, `sandbox_open`, `sandbox_shot`, plus a console create that
returns the new id and its deep link.

Ids carry a hyphen by construction (`browser-pool-…`, `claim-…`, `bare-…`), and
`extract_id` requires one, so a sentence that merely contains the word
"sandbox" is not read as an id (`5c17fa8`). An action against an id the console
does not list is refused (`86bcb03`). When the console's JSON surface answers
`404` the tool raises and the pipeline records `ok: false` with the error as
evidence; there is no fabricated inventory (`1e2d90e`, `a561374`).

`tool_schema.py` validates a call before it runs: unknown tools, wrong types and
missing required arguments are rejected with an error string and recorded as a
failed fact, so a malformed plan degrades to the evidence actually gathered
rather than reaching a tool with half its arguments (`436563f`).

## Links, which are the part a reader checks

`445ef0f` replaced placeholder output with links the reader can click: PromQL
written by the model against the metric names the platform actually scrapes,
Grafana and Hubble URLs built from the same facts, and the Argo CD application
deep link on the hash route Argo serves (`bf8546d`). `6ba9142` restricts links to
the questions that need them, so an answer does not arrive carrying three
buttons for one fact.

## Manifests tracked in this phase

`config/gpu-exp/llm-gpu.yaml` (`4c0ba7d`) and `config/gpu-exp/assistant-e4-gpu.yaml`
(`6dd3442`) are the E4 A/B deployments, previously applied by hand. They are
tracked here; registering them as Applications remains open (`ROADMAP.md` §4).

## Verify it

```bash
cd apps/assistant && python3 -m pytest -q             # unit tests, no cluster
bash config/ci/validate.sh                            # charts + kubeconform
curl -s http://192.168.0.249/v1beta1/projects | head -c 200   # JSON, no credentials
kubectl -n pilot get freight,stages,promotions
kubectl -n kargo-pilot-staging get deploy pilot -o jsonpath='{..image}'
kubectl -n argocd get app -l app.kubernetes.io/part-of=kargo-pilot
```

## Limits

- **Anyone who can reach `192.168.0.249` has Kargo admin.** The gateway holds the
  admin password and the credential that pushes to this repository. The Service
  is a LAN `LoadBalancer` for that reason and is not exposed further. Deleting
  the gateway returns a login prompt, not an open API.
- **`.248` is gone.** A `kargo-gateway` Service was briefly applied into the
  `default` namespace without a `namespace:` field; Cilium LB-IPAM allocated
  `default/kargo-gateway` `.248`, and the allocation did not return when that
  object was deleted. `lan-pool` reports 9 used, 2 available. The manifests now
  carry `namespace: kargo`.
- **No log streaming through the proxy.** It buffers response bodies; the JSON
  REST API and the UI assets work, live logs do not.
- **First discovery is slow.** The first SemVer scan of `docker.io/library/busybox`
  took minutes because it enumerates the tag set and reads metadata per
  candidate; a narrower `allowTagsRegexes` answered immediately.
- **Two of the four Kargo Applications read `feat/kargo`.** `kargo-gateway` and
  `kargo-pilot-resources` track the branch; `kargo-pilot-staging` and
  `kargo-pilot-prod` are pinned by `argocd-update` to the promotion commits
  `5c5d204` and `ced5f89`. The environments therefore survive a branch
  retirement and the two resource Applications do not: Argo CD cannot resolve a
  branch that has never been pushed and will not follow one that was deleted, so
  `targetRevision` has to be re-pointed before the branch goes away.
- **Five pods, ~100 MiB** in `kargo` on this node (`kubectl top pod -n kargo`:
  api 17Mi, controller 26Mi, management controller 20Mi, webhooks server 17Mi,
  gateway ~25Mi), plus a nightly garbage-collector CronJob.
- **The sandbox tools depend on the console API.** With it absent, the tools fail
  closed with the error as evidence. The assistant holds no RBAC on the sandbox
  resources; every mutation it proposes goes through the console.

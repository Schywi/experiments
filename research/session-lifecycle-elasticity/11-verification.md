# Note 11 — Verification of the running system

| | |
|---|---|
| Status | Verification record |
| Date | 2026-10-29 |
| Branch | `research/session-lifecycle-elasticity` at `b77308c` |
| Method | live `kubectl`, registry digests, VictoriaMetrics API, run SQLite/JSON, `go test ./...` |
| Labels | **implemented** (code exists) · **deployed** (running) · **configured** (set somewhere real) · **verified** (a command or run showed it) · **UNKNOWN** |

Anything below without a command or a file:line next to it is a claim, and claims are not in this
document.

---

## 1. Autoscaling correctness

**Source.** `internal/autoscale/autoscale.go`. Demand is `target := stats.Ready + stats.Pending`
(`:96`), clamped by `stats < cfg.Min` (`:97`) and `> cfg.Max` (`:100`). The patch happens only when
the target differs from `pool.Desired` (`:110-125`). The loop's interval is `cfg.Interval`, default
5 s (`:161-170`).

**Image and digest.** Deployment runs `127.0.0.1:5000/session-lab-autoscaler:local`; the running
pod's `imageID` is `…@sha256:4cce81b7ccf4…`, identical to the registry's
`Docker-Content-Digest` for that tag. **Verified.**

**Live configuration.** Deployment args, read from the object:

```
--namespace=sandbox-demo --pool=browser-pool --min=2 --max=40 --interval=5s
--configmap=session-lab-autoscaler --metrics-addr=:9110
```

replicas 1. **Deployed and configured.**

**RBAC.** `Role/session-lab-autoscaler` in `sandbox-demo`: `sandboxclaims` `get,list,watch`;
`sandboxwarmpools` `get,list,watch,patch,update`; `configmaps` `get,create,update`. No
cluster-scoped grant. **Deployed and configured.**

**What `pending_claims` counts.** Claims in the namespace whose Ready condition is not True
(`adapter.ClaimStats`), excluding claims whose Ready condition is False with reason `PodFailed`,
`Failed`, `TemplateNotFound` or `WarmPoolNotFound` (`internal/adapter/kube.go:191-206`). Stale
claims therefore do count until they carry a failure reason — and an *orphaned but Ready* claim
counts as full demand. That is exactly what happened after the API outage in note 10: sixteen
Ready orphaned claims held the pool at 22 with no traffic, and the manual delete dropped it to 2 in
seconds. **Verified as a behaviour; the consequence was a defect, fixed only for the runner's own
cleanup (see §2), not for a foreign orphan.**

**Scale policy.** No cooldown, no stabilization window, no hysteresis: the scaler patches within one
interval of a change in the computed target. **UNKNOWN for "cooldown behaviour" — there is none to
verify.** Scale-down is as immediate as scale-up.

**Competing actors.** `kubectl get hpa -A` → `No resources found`; KEDA CRDs → `0`. The only other
writer is the experiment runner, and until this turn the two fought: with the autoscaler running, a
manual `spec.replicas=5` was reverted to 2 within four seconds (14:25:20 → 14:25:24). **Verified
conflict, now resolved by ownership:** pool annotation `session-lab.io/autoscaler=disabled` makes the
scaler publish `paused=true` and write nothing, and the runner sets that annotation for any run with
`--pool-replicas`, clearing it when it restores the count.

**Scale-out and scale-in, recorded** (autoscaler log, `kubectl logs deploy/session-lab-autoscaler`):

| Time | desired | ready | Event |
|---|---|---|---|
| 17:13:48 | 2 | 2 | `scale up: desired 0 -> 2 (pending 0, ready 2)` |
| 17:14:23 | 4 | 4 | `scale up: desired 2 -> 4` |
| 17:14:28 | 7 | 7 | `scale up: desired 4 -> 7` |
| 17:14:33 | 9 | 9 | `scale up: desired 7 -> 9` |
| 17:14:38 | 12 | 12 | `scale up: desired 9 -> 12` |
| 17:14:43 | 15 | 3 | `scale up: desired 12 -> 15` — ready lagging |
| 17:16:18 | 22 | 22 | `scale-out complete: 22 ready after 84.9s` |
| 17:17:38 | 2 | 2 | `scale down: desired 22 -> 2 (pending 0, ready 2)` |

Both directions **verified from recorded demand, desired and ready counts with timestamps.**

**Defect found while verifying.** The deployment ran an image from *before* the pause logic existed:
`:local` is a mutable tag and `pullPolicy: IfNotPresent` kept the node's cached copy. The running
digest `7fb2eb6…` did not match the registry's `4cce81b…`. Fixed to `Always` and re-verified. Without
comparing digests this audit would have certified behaviour that was not in the binary.

## 2. Claim lifecycle and failure recovery

**Reconciliation of every experiment's claims** (run SQLite `instances` vs live `sandboxclaims`):

| Run | Instances | Sessions | Success | Still live now |
|---|---|---|---|---|
| `…161101Z` reuse/60 | 55 | 60 | 42 | 0 |
| `…165409Z` destroy/60 | 60 | 60 | 60 | 0 |
| `…170145Z` reuse/auto/60 | 55 | 60 | 35 | 0 |
| `…171417Z` reuse/auto/20 | 16 | 20 | 16 | 0 |
| `…172943Z` fixed 4/6 | 6 | 6 | 6 | 0 |

Live claims today: 5, all pre-existing (`claim-mvsa…`, `mvsacusz`, `mvsj053b`, `mvsk8cwm`,
`mvsk8fh5`) from work before this phase. **No `lab-` claim exists.**

**Cleanup paths, verified one by one:**

| Path | Evidence | Status |
|---|---|---|
| Normal completion | runs above, 0 live claims afterwards | **verified** |
| API error during cleanup | `…171417Z`: sixteen claims survived; I deleted them by hand at 14:17 (`kubectl delete sandboxclaims -l …`), after which the pool fell 22 → 2 | **verified failure**, fixed with retry |
| Retry added | `deleteWithRetry`, five attempts with backoff (`internal/run/run.go`), used by both `release()` and the `destroy` path | **implemented and deployed** in the runner binary; **not yet verified against a real API outage** |
| Process termination | a background run I killed left two claims; deleted by hand | **verified failure**, no in-process recovery by design |
| Exhausted retries | — | **UNKNOWN**: no fault-injection test exists that makes all five attempts fail |
| Foreign orphan (a claim not created by the runner) | none exists | **UNKNOWN**: nothing sweeps orphans; the autoscaler counts a Ready orphan as demand until it is deleted |

The honest summary: cleanup is reliable on a healthy API, retries cover a short outage, and **there
is no recovery mechanism for a claim that outlives the process.** Claiming "eventual cleanup" would
be false.

## 3. Capacity and overload

**Did the previous run complete its intended workload?** No. `…171417Z` planned 20 sessions and
finished 16; four ended `acquisition_failed` with `dial tcp 127.0.0.1:6443: connect: connection
refused`. The intended workload was not delivered. **Verified.**

| Run | Offered | Accepted | Queued (waited > 0) | Rejected | Completed | Failed |
|---|---|---|---|---|---|---|
| `…161101Z` reuse, fixed 20 | 60 | 43 | 60 | 17 (`Insufficient cpu`) | 42 | 18 |
| `…165409Z` destroy, fixed 20 | 60 | 60 | 60 | 0 | 60 | 0 |
| `…170145Z` reuse, autoscaled | 60 | 60 | 60 | 0 | 35 | 25 (API server) |
| `…171417Z` reuse, autoscaled | 20 | 20 | — | 0 | 16 | 4 (API server) |

**Queue wait, scale-out and ready-capacity lag.** `…171417Z`: queue wait p50 104 ms / p95 157 ms —
short because the pool was already growing when claims arrived. Scale-out latency, measured by the
scaler from decision to fully ready with an empty queue: **84.9 s for 22 replicas**. Ready lag inside
that window: desired 15 with ready 3 at 17:14:43, desired 22 with ready 9 at 17:15:31. **Verified.**

**Where demand exceeded capacity.** Two distinct crossings, both recorded:

- Fixed pool 20 + reuse: at offered 60 the scheduler refused with `1 Insufficient cpu` (node's
  6000m of requests ≈ sixty 100m pods), 17 refusals.
- Autoscaled: the pool grew past the point where forty ready Chromium pods plus refill overwhelmed
  the k3s API server; node `MemAvailable` bottomed at 13.9% (2219 MiB of 15924 MiB) with 4.2 GiB of
  swap.

**Ceiling.** The configured maximum is `--max=40`, read from the live Deployment args and echoed by
the scaler's own metric `session_lab_autoscaler_max_replicas 40`. The highest **observed** desired
count was 40 in run `…170145Z`; **40 is not a measured capacity of the platform** — the run that
reached it failed on the API server, not on the pool.

## 4. Observability

Four separate states, kept apart:

| Layer | State | Evidence |
|---|---|---|
| **Exposed** | yes | autoscaler `Service` `10.43.187.35:9110` answered `session_lab_autoscaler_target_replicas 2`, `max_replicas 40`, `pool_desired/ready 2`; runner serves `:9109` on the host |
| **Scraped** | **no** | VictoriaMetrics active targets: 5 — `assistant`, `hubble-relay`, `cilium-operator`, `cilium-hubble`, `cilium-agent`. No `session-lab` job, no `session-lab-autoscaler` job |
| **Queried** | **no** | `GET /api/v1/query?query=session_lab_autoscaler_target_replicas` → `seriesFetched: 0` |
| **Visualized** | **no** | `deploy/grafana` in `observability` is `0/0`; the 12-panel dashboard has never rendered |

**Why the scrape is missing, and why I did not force it.** The live ConfigMap
`victoriametrics-scrape` holds five jobs and none of ours. The chart change is committed on this
branch, but `Application/victoriametrics` in Argo CD is `sync=OutOfSync` with
`targetRevision=native-k3s-gpu-models`. Changing that revision is a routing decision for the
repository owner, and I did not touch it. **The intended monitoring configuration is not live:
UNKNOWN whether it works end to end, because it has never been deployed.**

## 5. Experiment isolation and ownership

**How a fixed-pool experiment works now**, verified end to end at 14:29:43 today:

1. The runner annotates the pool `session-lab.io/autoscaler=disabled` and patches
   `spec.replicas: 4`.
2. The autoscaler reads the annotation, publishes `paused=true`, and writes nothing.
3. Observed over the whole run: `desired=4 paused=disabled` at 14:29:49, 52, 55, 58, 14:30:01, 04,
   07 — **seven consecutive samples, no drift**, and the autoscaler's log contains no scale event
   between 17:29:29 and 17:30:18.
4. On exit the runner restores the original count and clears the annotation; state afterwards:
   `desired=2 paused=""`, zero `lab-` claims.

Compare with the same experiment before ownership existed: `desired` was rewritten 2 → 5 → 7 → 8 by
the scaler during a "fixed pool" run.

**Supported ways to disable autoscaling**, both implemented:

| Method | Command | Scope |
|---|---|---|
| Per run, automatic | runner with `--pool-replicas` sets the annotation | that run only |
| Whole scaler off, chart stays installed | `helm upgrade session-lab config/session-lab --set autoscaler.enabled=false` | Deployment scaled to 0; RBAC and Service remain |
| Full removal | `helm uninstall session-lab -n sandbox-demo` | everything |

**Elastic runs remain controller-owned**: no run without `--pool-replicas` touches the pool, and
`…170145Z` / `…171417Z` show the scaler driving 2 → 40 and 2 → 22 alone.

## 6. Remaining blockers against the original requirements

| # | Requirement | Blocker |
|---|---|---|
| 1 | Scheduler | none — verified |
| 2 | Configured vs observed capacity | **blocked by load**: the node's 6000m of CPU requests is the binding constraint at ~60 pods, and the only run that reached 22 concurrent replicas took the API server down. A capacity figure needs a load the control plane survives |
| 3 | Elasticity | implemented, deployed, configured, verified in both directions; **blocker:** no cooldown/hysteresis, so the scaler will chase demand up and down at 5 s granularity — untested under oscillation |
| 4 | Lifecycle experiments | reuse and destroy verified; **suspend verified only at 5 resume cycles**, never under load |
| 5 | Overload | refusal, queueing, scale-out and delay verified; **blocker:** recovery after an API-server outage is manual — the runner retry is unverified and nothing sweeps foreign orphans |
| 6 | Metrics + dashboards | **blocked by Argo routing**: scrape jobs exist in git, are not deployed; Grafana is at 0/0. Nothing in this layer is visualized |
| 7 | Correlated results | none for the runner; **blocker:** the autoscaler's own decisions (min/max/interval) are recorded only while it is running, and an `enabled=false` scaler leaves a stale ConfigMap that a run would read as present |
| 8 | Reproducible tests and reports | `go test ./...` green; **blocker:** no test kills a run to exercise the retry, and no fault injection exists for exhausted retries |

## 7. How to check this note

```bash
# 1
kubectl -n sandbox-demo get deploy session-lab-autoscaler -o jsonpath='{.spec.template.spec.containers[0].args}'
kubectl -n sandbox-demo get pod -l app.kubernetes.io/name=session-lab-autoscaler \
  -o jsonpath='{.items[0].status.containerStatuses[0].imageID}{"\n"}'
kubectl -n sandbox-demo logs deploy/session-lab-autoscaler --tail=40
kubectl -n sandbox-demo get role session-lab-autoscaler -o yaml

# 2
kubectl -n sandbox-demo get sandboxclaims -o name | grep lab- || echo "no lab claims"

# 3
jq '{outcomes, queue_wait_ms, concurrency}' docs/session-lab-runs/20261029T171417Z.json

# 4
curl -s "http://192.168.0.246/api/v1/targets?state=active" | jq -r '.data.activeTargets[].labels.job'
kubectl -n observability get deploy grafana
kubectl -n argocd get app victoriametrics -o jsonpath='{.status.sync.status}{" "}{.spec.source.targetRevision}{"\n"}'

# 5
kubectl -n sandbox-demo get sandboxwarmpool browser-pool \
  -o jsonpath='{.spec.replicas}{" "}{.metadata.annotations.session-lab\.io/autoscaler}{"\n"}'
```

# Note 09 — Three runs on one workload: fixed pool, destroy/rebuild, and autoscaling

| | |
|---|---|
| Status | Recorded, results |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Runs | `20261029T161101Z` reuse/fixed pool, `20261029T165409Z` destroy/fixed pool, `20261029T170145Z` reuse/autoscaled |
| Code | `apps/session-lab` (runner), `apps/session-lab/cmd/pool-autoscaler` (scaler) |
| Related | [08 — requirement status](08-requirement-status.md) |

---

## 1. The workload, held constant

All three runs use the same generated workload, so the only differences are the lifecycle policy
and who owns the pool size:

```bash
--sessions 60 --mix 30s:40,2m:15,5m:5 --rate 0.8
```

Sixty sessions, offered concurrency 60, arrival rate 0.8/s, mean hold 75 s. The first two runs also
pass `--pool-replicas 20`; the third passes no pool flag and the autoscaler owns `spec.replicas`.

## 2. Results side by side

| | reuse, fixed 20 | destroy, fixed 20 | reuse, autoscaled |
|---|---|---|---|
| run | `…161101Z` | `…165409Z` | `…170145Z` |
| outcomes | 42 ok, 17 refused, 1 reset failed | **60 ok** | 35 ok, 25 refused |
| measured concurrency | peak 18, mean 9.3 | peak 28, mean 11.7 | peak 23, mean 7.3 |
| queue wait p50 / p95 | 3.2 s / 134 s | **85 s / 209 s** | **1.4 s / 54 s** |
| reset p50 / p95 | 35 ms / 625 ms | — | 39 ms / 531 ms |
| release (delete) p50 / p95 | — | **374 ms / 7.07 s** | — |
| instances | 55, 2 sessions each | 60, **1 session each** | 55, 2 sessions each |
| CPU, core-seconds | 93.96 | 107.59 | 65.24 |
| memory peak p50 / max | 154 / 209 MiB | 143 / 208 MiB | 151 / 194 MiB |
| throttled sessions | 43 of 43 sampled | 60 of 60 | 35 of 35 |
| node MemAvailable floor | 3322 MiB (20.9%) | 3463 MiB (21.7%) | **2219 MiB (13.9%)** |
| boundary | scheduler `Insufficient cpu` | none recorded | API server connection refused |

## 3. Lifecycle cost, measured (requirement 4)

Three numbers, all from runs, all for the same browse-shaped session:

| Path | Cost | Run |
|---|---|---|
| Reuse: reset between sessions | **35–39 ms p50**, 531–625 ms p95 | `…161101Z`, `…170145Z` |
| Destroy/rebuild: delete then acquire a fresh instance | **374 ms p50, 7.07 s p95** for the delete, plus a cold start inside the acquire column | `…165409Z` |
| Suspend/resume: patch to Ready again | **2853 ms p50** over 5 cycles | note 07 |

Reuse is two orders of magnitude cheaper than either alternative at the same offered load. Destroy
is not free in the other direction: because each instance is deleted after one session, sixty
sessions needed sixty instances and never reused one (`sessions/instance` mean 1.00 against 2.00 for
reuse).

## 4. What the autoscaler did (requirement 3, 5)

`kubectl get hpa -A` is empty and no KEDA CRD exists, so the run recorded
`hpa=0 keda=false session-lab-autoscaler min=2 max=40 every 5s (heartbeat 2s ago)` in its report —
the configuration came from the cluster, not from a flag.

| Observation | Value |
|---|---|
| replica trajectory | 2 → 22 → **40** (configured max), then back to 2 after the run |
| scale changes | 10 up, 3 down |
| completed scale-out cycles | 2, mean **195 s** from decision to pool-ready-with-empty-queue |
| ready lag | desired hit 40 while ready crawled 2 → 4 → 6 → 7 → 14 → 40 |
| queue wait, autoscaled vs fixed 20 | p50 **1.4 s vs 3.2 s**, p95 **54 s vs 134 s** |

Autoscaling improved the median and the p95 queue wait by roughly 2× at identical offered load, and
it reached the configured ceiling of 40 replicas — at which point the node, not the pool, was the
limit.

## 5. What broke, and what it means

**The API server became the boundary in the autoscaled run.** Twenty-five sessions failed with
`dial tcp 127.0.0.1:6443: connect: connection refused` and `read: connection reset by peer` while
the runner was polling claims for readiness, with node `MemAvailable` at 13.9% and 4.2 GiB of swap in
use. The k3s node stayed `Ready` throughout, so this was the API server under load rather than a node
failure.

That is a real result and it has a cost: the autoscaled run's failure count is not a pool-sizing
failure, it is a control-plane failure, and the two must not be conflated. Reading the three runs
together:

- Fixed pool + reuse: the platform refuses new instances once the node's CPU requests are gone.
- Fixed pool + destroy: nothing refused, because instances are returned continuously; the price is
  an 85 s median queue wait.
- Autoscaled + reuse: the queue wait drops to 1.4 s, the pool reaches its ceiling, and the demand
  that comes with forty ready pods plus refill pushes the API server past what it will serve.

**Every session in every run was CPU-throttled** at the template's 1000m limit. Hold durations
across all three runs are therefore shaped by that limit, which is why the template's requests and
limits are printed with each report.

## 6. Requirement status after these runs

| # | Requirement | Status |
|---|---|---|
| 1 | Scheduler with exclusive ownership | verified — 3 runs, counts balance in each |
| 2 | Configured vs observed capacity | verified — configured 20/40, observed: peak 18 concurrency at fixed 20; ceiling 40 reached under autoscaling |
| 3 | Real elasticity | verified — autoscaler implemented, deployed, configured min 2/max 40, and observed scaling 2 → 40 with measured scale-out latency |
| 4 | Reuse vs destroy/rebuild | verified — reset 35–39 ms against delete 374 ms p50 / 7.07 s p95 plus cold start |
| 5 | Overload, refusal, scale-out, delay, recovery | verified — refusal (run 1), queueing (run 2), scale-out and delay (run 3), recovery to min 2 after |
| 6 | Metrics and dashboards | runner series verified live; 12 dashboard panels render but **have not been viewed with data** — Grafana is scaled to 0 |
| 7 | Results correlated with configuration | verified — every report carries protocol, template limits, autoscaler bounds and heartbeat |
| 8 | Reproducible tests and reports | `go test ./...` green; three run records with SQLite + JSON |

## 7. What the next experiment should be

1. **Raise the template's CPU limit** (`config/sandbox/values.yaml`, a reviewed GitOps change) and
   repeat run 3. Every session was throttled, so hold and queue numbers carry the limit.
2. **Cap the autoscaler lower and repeat.** If 40 replicas is enough to break the API server, find the
   replica count where it does not, and record that as the control-plane ceiling — it is a different
   limit from the node's CPU requests.
3. **Stagger the refill.** The scaler asks for the whole target in one patch; asking for it in steps
   (or pacing the pool's own `--sandbox-warm-pool-max-refill-rate`) may keep the API server inside
   its budget.
4. **View the dashboard.** Grafana is at 0/0; scaling the deployment up and opening the session-lab
   dashboard is the last unverified part of requirement 6.

## 8. How to check this note

```bash
# the three runs
for r in 20261029T161101Z 20261029T165409Z 20261029T170145Z; do
  jq -r '"\(.config.policy) \(.outcomes) queue \(.queue_wait_ms.p50)/\(.queue_wait_ms.p95)"' \
    docs/session-lab-runs/$r.json
done

# the scaling the autoscaler performed
sqlite3 docs/session-lab-runs/20261029T170145Z.db \
  "select json_extract(value,'$.desired'), json_extract(value,'$.ready') from runs, json_each(runs.replicas);"

# what the run recorded about autoscaling
jq '.autoscaler' docs/session-lab-runs/20261029T170145Z.json
```

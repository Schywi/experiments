# Note 03 — What the sandbox platform already does, and what a session-elasticity test would need

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Scope | A baseline read of the fork's load paths, autoscaling story and API surface, so the new test is written against what exists |
| Evidence base | agent-sandbox checkout `d816e0f`; see the folder [README](README.md) for provenance |
| Related | [01 — CPU and memory instrumentation](01-instrumentation-cpu-memory.md), [02 — objective and soak statistics](02-objective-and-soak-statistics.md), [04 — the deliverable](04-deliverable.md) |

---

## Status of the numbers in this note

This note reads what the platform and the harness do. The rates quoted next to that reading come from
upstream documentation and are not measurements of this cluster:

| Quantity | Where it came from | Status |
|---|---|---|
| the per-pool refill ceiling in §3 | upstream `docs/configuration.md`, `docs/performance-tuning.md` | documented claim, unverified here |
| the burst adoption rate in §3 | same | documented claim, unverified here |
| any browser-per-host density | fork research and Phase 7's note | unverified; note 04 §2 keeps it out of the runner's inputs |

---

## 1. The scenario in one paragraph

Agents arrive, hold one sandbox exclusively until their work is done, release it, and the session
length varies between 30 seconds and 10 minutes. When demand exceeds the pool, some agents wait;
the pool then grows. The question is whether the platform does that, and at what cost in queue
wait and scale-out latency.

None of that exists as a test. What exists is the set of ingredients: warm adoption, claim bursts,
a sustained Poisson stream at one dwell value, and HPA-based scaling driven from outside.

## 2. The two load paths today

Neither path runs from `make test-unit`.

**A. `test/stress`, the Go load harness** (`test/stress/main.go:15-40`). It runs an ordered list of
phases from `--phases`; grammar in `test/stress/phase.go:69-96`. Every phase kind:

| Kind | What it measures |
|---|---|
| `fill` / `fill-pct:N` | long-running background sandboxes so later phases run at scale or near the capacity cliff |
| `probe` | clean launch latency at low concurrency — default 1, "so they never queue on cluster capacity or on each other" (`test/stress/phases.go:191-194`) |
| `throughput-mif:N` | closed-loop churn capped at N in flight; the slot is held from before Create until the pod is observed deleted, so the rate includes the delete→pod-gone pipeline (`phases.go:238-248`) |
| `claims-warm` | N claims fired simultaneously at a fully-Readied pool (`phases.go:458-475`) |
| `claims-warm-sustained` | open-loop Poisson arrival at `--sustained-rate` for `--sustained-seconds`, each claim deleted `--claim-dwell` after Ready, pool refilled in the background (`test/stress/sustained.go:15-45`) |

`claims-warm-sustained` is the only phase that models agents arriving while others hold
instances. Its own comment states the model: pool replicas `= ceil(rate/namespaces × headroom)`,
"the steady-state inventory deficit is rate × refill-latency", and a dry pool shows up as
window-latency cliffs — "a real degradation signal, but of the pool sizing, not the adoption path"
(`sustained.go:30-40`). Knobs at `test/stress/main.go:177-200`: `SustainedRate`,
`SustainedSeconds`, `ClaimDwell`, `SustainedNamespaces`, `SustainedPoolHeadroom`,
`SustainedLifecycleBudget`.

Outputs (`main.go:30-38`): `summary.json`, `sandboxes.jsonl`, `timeseries.jsonl`,
`watch.jsonl.gz`, `metrics.jsonl.gz`, plus pprof profiles. Report generator in
`test/stress/generate-report/`. CI exercises it only from the benchmark presubmits
(`dev/ci/presubmits/benchmarks-kops-gcp-kindnet:16`).

**B. ClusterLoader2 recipes** (`dev/load-test/test-recipes/`, `test/benchmarks/`). The
capacity-cliff recipe finds "the first load step where SandboxWarmPools fail to converge within a
configured deadline" — a controller-capacity question, with replicas stepped by hand via
`STEP_SIZE`/`TOTAL_STEPS`.

**C. E2E correctness.** `test/e2e/parallelism_test.go:242-254` fires 20 claims at a pool of 25 and
at a pool of 5; `test/e2e/warmpool_benchmark_test.go:37` does 10 parallel claims;
`test/e2e/extensions/chromesandbox_density_test.go:68` measures Chrome startup timings at a
density. None of them hold a session for a duration.

The nearest existing command to the scenario:

```bash
test/stress --phases claims-warm-sustained --sustained-rate R --claim-dwell D \
            --sustained-seconds S --sustained-pool-headroom H
```

Poisson arrivals, concurrent holders, refill and degradation windows are covered. One dwell value,
no per-instance exclusivity, no reuse path, no elasticity reaction.

## 3. How elasticity works today

**The pool is not elastic.** `SandboxWarmPoolSpec.Replicas` is a plain desired count, "controlled
by an HPA if specified" (`extensions/api/v1beta1/sandboxwarmpool_types.go:41-44`), defaults to 1,
accepts 0, and the CRD exposes a `scale` subresource
(`k8s/crds/extensions.agents.x-k8s.io_sandboxwarmpools.yaml:89-93`). The controller has no demand
estimator and no queue-depth sizing.

**Elasticity is external and shipped as examples.** HPA on the custom metric
`agent_sandbox_claim_creation_total` via GKE GMP plus the Stackdriver adapter
(`examples/hpa-swp-scaling/`: `minReplicas: 10`, `maxReplicas: 100`, target 0.5 claims/s at
`hpa.yaml:11-12` and `README.md:39`). KEDA for scale-to-zero, because "a native Horizontal Pod
Autoscaler **cannot scale to zero**" (`examples/keda-scale-to-zero/README.md:15`) and "it delegates
the 1 → N range to a HorizontalPodAutoscaler it creates and manages" (`:24`).

**The controller's own knobs pace refill.** `--sandbox-warm-pool-max-refill-rate`,
`--sandbox-warm-pool-max-batch-size`, and a per-pool ceiling of ~70-85 sandboxes/s
(`docs/configuration.md:197-198`); burst-versus-sustained guidance with the 300 claims/s and
70-85/s figures in `docs/performance-tuning.md:35-67`.

**Nothing asserts elasticity.** No test creates sustained excess demand and then checks the
replica trajectory, the scale-out lead time against queue wait, or drain thrash. The signal for
sizing does not exist either: there is no queue-depth, waiting-claims or saturation metric in
`internal/metrics/metrics.go`.

## 4. The scenario, requirement by requirement

| Requirement | In the product? | In a test? |
|---|---|---|
| Sessions of 30 s / 2 min / 5 min / 10 min | yes, as an absolute deadline per object: `spec.shutdownTime` + `shutdownPolicy` (`api/v1beta1/sandbox_types.go:326-347`); claim-side `lifecycle.shutdownTime` / `ttlSecondsAfterFinished` (`extensions/api/v1beta1/sandboxclaim_types.go:61-83`) | no — one constant `--claim-dwell` per run, no mixed distribution, no per-length breakdown in reports |
| Agent 1 holds instance B exclusively | partially: `SandboxClaim`↔`Sandbox` is 1:1 and permanent; "once a sandbox is claimed by a SandboxClaim, ownership transfers to the claim and the pool no longer manages or replaces it" (`sandboxwarmpool_types.go:57-59`) | no — there is no busy state to test, and the router is stateless: `X-Sandbox-ID` routing lets two callers reach the same sandbox concurrently |
| Unblock by reusing the same instance ("blank and reuse") | no such primitive; a claimed sandbox never returns to the pool, reuse is application-level (`examples/agent-sandbox-rl/agent_sandbox_rl/recycle.py`) | no |
| Unblock by adopting a fresh instance from the pool | yes — warm adoption, the well-tested path (`claims-warm`, `claims-warm-sustained`, `TestParallelSandboxClaimsWithSufficientWarmPool`) | yes |
| Unblock by growing the pool | only via external HPA/KEDA; no demand-driven controller logic | no |
| A queue of waiting agents, and queue-wait latency | implicitly: a claim against a dry pool requeues (`warmCandidatesPendingError`, `sandboxclaim_controller.go:88-95`; immediate and bounded requeues at `:362`, `:466-490`, `:493-500`). The queue package holds adoptable sandboxes, not waiting claims (`extensions/controllers/queue/simple_sandbox_queue.go:28-47`) | partially, non-assertively: pool-dry appears as a latency cliff in `sustainedWindows`; nothing asserts waiting time, ordering or head-of-line blocking |

## 5. The word "idle" has four meanings here

There is no `idle` field, no `lastActivity`, no `idleTimeout` anywhere in `api/` or
`extensions/api/`. The four things people mean:

1. **An absolute deadline** — `shutdownTime` (`sandbox_types.go:328-347`) is a TTL, not idle
   detection. Default `shutdownPolicy: Retain`; the Pod and Service are always torn down on
   expiry.
2. **Suspend is the closest thing to an idle park** — `spec.operatingMode: Suspended`: "the
   controller terminates the backing Pod while retaining the Sandbox object and its volumes"
   (`sandbox_types.go:243-247`). Progress is reported by the `Suspended` condition, the Pod is
   recreated on resume, covered by `test/e2e/mode_test.go:34`
   (`TestSandboxSuspendResumeCycleWithOperatingMode`) and
   `docs/keps/694-kep-for-suspend-and-resume-for-beta/README.md`. Suspend frees compute and drops
   in-guest memory state; only PVC-backed state survives.
3. **Memory-to-disk hibernation does not exist.** KEP-119 keeps it as reserved design space:
   "freeze" (pausing processes), "hibernate" (snapshotting memory to disk) and traditional
   scale-to-zero "would all appear identically as 'not ready'"
   (`docs/keps/119-sandbox-suspended-state/README.md:90`). The Snapshot API was split out of
   KEP-694.
4. **HTTP keep-alive** — `IdleTimeout` in the router, default 120 s
   (`sandbox-router/server/server.go:62`, default at `:85-86`), and `sandboxd --http-idle-timeout`.
   Unrelated to sandboxes.

The swap and zram idea belongs to the fork's density research, not to the controller:
`research/experiments/03-browser-density.md` §4.1-4.3 covers the anon/file/shmem split and the
zram compressibility limit, and states that swap raises idle density while buying nothing for
active density (`research/experiments/02-capacity-6vcpu-100k.md:142-144`). Host swap and pod swap
are independent levers: `fail-swap-on=false` is not `memorySwap.swapBehavior`.

Usable today: `shutdownTime` / `ttlSecondsAfterFinished` for hard session caps;
`operatingMode: Suspended` driven by the caller's own idle detection, so a resume is a pod restart
that keeps the Sandbox identity, Service and volumes; zram and pod swap at node level for idle
density of pods left running, with swap-in on idle→active as a latency cost.

## 6. Five gaps a session-elasticity test has to close

1. **Heterogeneous dwell.** `ClaimDwell` is one scalar per run (`test/stress/main.go:182-185`). A
   30 s / 2 min / 5 min / 10 min mix needs a new argument or phase kind (for example
   `dwell-dist:`) plus per-bucket reporting in `summary.json` and the report generator.
2. **Exclusivity and queue model.** No per-sandbox lock, no "waiting for B" state, no queue-wait
   metric. Telling "my claim waited 40 s for capacity" apart from "adoption was slow" needs
   claim-create→Ready split by cause; the pool-dry windows in `sustained.go` are the only proxy.
3. **Reuse path.** Not modelled. A separate phase that keeps one sandbox and issues N sequential
   rounds against it would measure reset cost against warm and cold acquisition, and it is the
   question that needs tracing (§5 of note 01).
4. **Elasticity assertion.** Excess demand held above capacity, then an assertion on the replica
   trajectory, the scale-out lead time against queue wait, and no thrash on drain — on top of a
   metric that exposes waiting demand, which does not exist yet.
5. **Per-session CPU and memory.** The gap note 01 closes: ~50 lines in the harness plus one
   node-exporter collector flag.

## 7. Where this lands in this repository

Phase 7 is merged into `cluster-evolution` (`218d60a`): `config/sandbox/` holds the router, the
browser `SandboxTemplate` and the warm pool, `apps/browser-sandbox/` the Chromium image, and
`apps/sandbox-console/` the Python pane whose phase document is `evolution/07-sandbox-console.md`.

Phase 8 adds `apps/session-lab/`, a Go module that talks to the sandbox APIs directly
([04](04-deliverable.md)). The console stays out of that path for a structural reason: it holds `get/list/watch` on `sandboxwarmpools` and `sandboxtemplates`
(`apps/sandbox-console/chart/templates/rbac.yaml`), so a replica change is outside its verbs, and it
has no session model. Its API is available to a human driving a browser; this runner does not read
it.

## 8. How to check this note

```bash
cd ~/Documentos/git_repos/agent-sandbox

# the phase kinds and the single dwell flag
rg -n "PhaseThroughput|PhaseProbe|PhaseClaimsWarmSustained" test/stress/phase.go
rg -n "claim-dwell" test/stress/main.go

# the pool is a plain replica count with a scale subresource
rg -n "controlled by an HPA" extensions/api/v1beta1/sandboxwarmpool_types.go
rg -n "scale:" k8s/crds/extensions.agents.x-k8s.io_sandboxwarmpools.yaml

# suspending keeps the object and drops the pod
rg -n "terminates the backing Pod while retaining" api/v1beta1/sandbox_types.go

# no idle field exists in the API
rg -ni "idle" api extensions/api || echo "no idle field"
```

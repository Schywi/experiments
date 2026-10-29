# Note 04 — The deliverable: a sandbox session scheduler and experiment runner

| | |
|---|---|
| Status | Approved for implementation |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Scope | A Go program that generates session demand, owns session state, drives the sandbox APIs, measures what happens, and reports where the platform broke |
| Code | `apps/session-lab/` — new Go module in this repository |
| Related | [01](01-instrumentation-cpu-memory.md), [02](02-objective-and-soak-statistics.md), [03](03-what-exists-today.md), [`../rfc-sandbox-console.md`](../rfc-sandbox-console.md) |

---

## 1. What gets built

Four parts, one binary:

| Part | Responsibility |
|---|---|
| Session generator | creates N sessions with configured arrival times and hold durations, in four length buckets |
| Scheduler + SQLite | instance availability, ownership, exclusive leases, waiting sessions, lifecycle events, terminal outcomes |
| Sandbox adapter | acquire, hold, release, reset, suspend, resume, destroy and recreate against the `agents.x-k8s.io` / `extensions.agents.x-k8s.io` APIs |
| Measurement + report | lifecycle timings, CPU and memory per session and per instance, queue wait, reuse cost, scaling behaviour, and a single recommendation for the next run |

The boundary between the two lifecycles is the design: **the scheduler owns the experiment's session
lifecycle, the sandbox platform owns the instance lifecycle.** The runner connects to the platform
through its public API and does not rebuild it.

## 2. The rule about numbers

Two rules, and they govern everything in this directory.

1. **The runner imposes no ceiling.** There is no maximum concurrency, no maximum session count, no
   clamp on the pool, no "refuse past N live sandboxes". It scales the warm pool up to what the
   operator asks for, drives demand past whatever the platform can take, and records where it
   stopped. The boundary is output, not input.
2. **No value from any document is an input.** Every number the runner needs is either a flag the
   operator sets or a value it reads from the API at run time: node allocatable, pool status,
   template shape, current replica count. Prior claims in this repository and in the fork's research
   are hypotheses. The notes 01-03 in this folder carry inherited figures, marked as unverified;
   whatever a run produces supersedes them, and the correction goes into the note with the run that
   replaced it.

A per-session deadline exists (`--session-deadline`, off by default) because a session that never
gets an instance would otherwise hold the run open forever. It is a deadline on one session, not a
limit on how many run.

## 3. The lifecycle the runner records

| Event | Recorded |
|---|---|
| Session requested | arrival timestamp |
| Instance acquired | queue wait; warm or cold acquisition |
| Instance becomes ready | startup and acquisition latency |
| Session begins | hold start |
| Session ends | actual hold duration |
| Instance released | release and reset duration |
| Instance destroyed | deletion completion time |
| Replacement becomes ready | rebuild time |

Every timestamp is committed to SQLite as it happens, before the next action, so a run that dies
mid-flight — OOM kill of the runner, node pressure, an interrupted API call — still leaves a
readable run with the boundary visible.

A simulated agent is a scheduled job that acquires an instance, holds it, and releases it. No LLM
and no second process.

## 4. Terminal outcomes

One fixed value per session, countable and safe as a label:

`success`, `timeout`, `acquisition_failed`, `reset_failed`, `destroy_failed`, `lost_unknown`.

Failures never hide in free text. A session still waiting when the run ends is written as waiting
with its measured queue time, and appears in the report as its own row, outside the failure counts.

## 5. The four experiments

The runner exposes the lifecycle as interchangeable policies, so a run states which one it used.

| # | Policy | What it measures |
|---|---|---|
| A | Reuse — hold the instance, reset the browser, hand it to the next session | reset duration, accumulated resource use across sessions on one instance |
| B | Destroy and rebuild — delete after each session, acquire fresh | deletion, creation and readiness latency per session |
| C | Suspend and resume — `spec.operatingMode` between sessions | resume latency, and which state survives; in-guest memory is not assumed to survive |
| D | Capacity and elasticity — drive demand above what the pool can serve | queueing, failures, scale-out, and whether new instances arrive before waiting sessions give up |

## 6. The three elasticity configurations

The runner records which one it ran, and reports "elasticity not tested" when capacity was never
exceeded.

1. **Fixed pool.** No automatic scaling. Every instance occupied, what happens to the next session.
2. **Autoscaling as found.** The runner lists HPA and KEDA objects at run time and reports what is
   configured and whether it moves. An empty list is a finding, with the query that produced it.
3. **Controlled scaling.** Change the configuration, repeat the identical workload, compare. The
   runner patches the warm pool's replica count itself, at a recorded timestamp, and measures
   scale-out trigger → replica created → Ready latency against queue wait.

Each run stores the configuration, the replica trajectory, pending sessions and timestamps.

## 7. What the report must tell us

**Session behaviour**

| Metric | Shape |
|---|---|
| sessions completed / failed | count and rate |
| concurrent sessions | mean and peak |
| queue wait | p50 / p95 / p99 |
| acquisition and release | p50 / p95 / p99 |

**Resource consumption**

- CPU core-seconds per session, plus average and peak CPU.
- Memory peak and working-set peak per session.
- Cluster CPU and memory over the run.
- OOM kills, CFS throttling and memory pressure, each as its own record.

**Elasticity and lifecycle**

- Requested versus ready versus busy instances over time.
- Waiting sessions and time spent waiting.
- Scale-out trigger → replica creation → Ready latency.
- Creation, deletion, reset and resume durations.
- Failed or stuck lifecycle operations.

**Recommendation.** One field naming the constraint that broke first and the next experiment worth
running. A run with no recommendation is an incomplete run.

## 8. Out of scope for this version

No custom autoscaler. No eBPF agent. No 10,000-session campaign. No distributed scheduler. No new
observability platform. No browser driving beyond the reset and acquire paths the experiments above
need.

The console at `apps/sandbox-console/` is not in the path, and not because of anything it measures:
it holds `get/list/watch` on `sandboxwarmpools` and `sandboxtemplates` and `create/delete` only on
`sandboxes` and `sandboxclaims` (`apps/sandbox-console/chart/templates/rbac.yaml`), so it cannot
change a pool's replica count, and it has no session model. Its measured timings stay where they
are; this runner does not read them.

## 9. How to check this note

```bash
# the module this note describes
ls apps/session-lab/go.mod apps/session-lab/cmd/session-lab/main.go

# the RBAC that keeps the console out of the scaling path
rg -n -A 3 "sandboxwarmpools" apps/sandbox-console/chart/templates/rbac.yaml

# no ceiling in the runner: no clamp on concurrency, pool size or session count
rg -ni "max_?concurrent|maxconcurrent|ceiling|clamp" apps/session-lab/ || echo "no ceiling"
```

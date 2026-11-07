---
title: 'Slot ownership and browser health: what the two 3000-session runs show'
kind: audit
state: validated
date: 2026-11-07
branch: 'docs-site'
commits: [2aded3a, 25162db]
tags: [session-lifecycle, slots, health, saturation, state-validated]
---

# Slot ownership and browser health: what the two 3000-session runs show

Two runs of the same workload, one that finished and one that did not, and the
difference is not the queue, the arrival rate or the memory ceiling. It is one
broken browser that the slot pool kept handing work to because it was the only
instance still showing free capacity.

| | |
|---|---|
| Status | Findings, no fix applied |
| Date | 2026-11-07 (runs), 2026-11-07 (note) |
| Phase | session lifecycle |
| Branch | `docs-site` |
| Related | [pool split](20-cpu-gpu-pool-split.md), [manual runbook](21-manual-scaling-runbook.md), [attribution](../swap-capacity/01-attribution-across-classes.md) |

Evidence: `docs/session-lab-runs/20261107T033326Z.{json,db}` (run A) and
`docs/session-lab-runs/20261107T211551Z.{json,db}` (run B). **[M]** for anything
read out of those files, **[D]** for what the source says, **[I]** for arithmetic
that follows from both, **[U]** for what neither can answer.

## The workload both runs offered

```
3000 sessions, fixed arrival 35/s
holds        1500 x 30s, 900 x 2m, 600 x 5m
policy       reuse, contexts_per_browser 8, cdp_transport ws
pool         browser-pool-gpu, template read from the pool
boot_concurrency  unset (0 = unbounded), admit unset, workers unset (goroutine per session)
```

## What each run did

| | Run A `20261107T033326Z` | Run B `20261107T211551Z` |
|---|---|---|
| success | 2992 **[M]** | 1619 **[M]** |
| failed | 8 `lost_unknown` **[M]** | 1378 `reset_failed`, 3 `acquisition_failed` **[M]** |
| instances | 38 **[M]** | 43 **[M]** |
| slots (instances x 8) | 304 **[I]** | 344 **[I]** |
| peak concurrency | 236 (78 % of slots) **[I]** | 336 (98 % of slots) **[I]** |
| mean sessions per instance | 78.9 (cumulative) **[M]** | 37.7 (cumulative) **[M]** |
| acquire p50 | 1325325 ms (22.1 min) **[M]** | 981916 ms (16.4 min) **[M]** |
| admission p50 | 0.0011 ms **[M]** | 0.0012 ms **[M]** |
| node cpu peak / total | 1.98 / 6 cores **[M]** | 2.81 / 6 cores **[M]** |
| host memory floor | 1736 MiB (10.9 %) **[M]** | 2900 MiB (18.2 %) **[M]** |
| swap used | 7755 MiB **[M]** | 8986 MiB **[M]** |
| OOM kills | 0 **[M]** | 0 **[M]** |
| failures on one instance | 8, all on `-5` **[M]** | 1378, all on `-43` **[M]** |

`SELECT instance, COUNT(*) FROM sessions WHERE outcome='reset_failed' GROUP BY instance`
returns one row for run B: `lab-20261107t211551z-43 | 1378`. Its `instances` row
reads `sessions = 0`, so that browser never completed a single session **[M]**.

## The mechanism

**Every healthy browser was full.** At 98 % occupancy the pool had no free slot to
give. The list of slots looks like this just before session 1159:

```
instances  1..42     InUse 8 / Capacity 8    no free slot, skipped
instance   43        InUse 0 / Capacity 8    8 free slots, all broken
```

**The pool prefers the busiest instance, so the broken one becomes the only
candidate.** `takeFreeLocked` (`internal/runtime/slots/slots.go:201-220`) walks the
entries, skips any at `InUse >= Capacity`, and picks the highest `InUse` among the
rest. Full instances are invisible; the empty, dead one wins by default **[D]**.

**The failure path returns the slot instead of removing the instance.**
`internal/runtime/session/shared.go:188-195`:

```go
if err != nil {
    r.lg.Error("isolate failed", …)
    spool.Give(lease)                       // slot goes back
    r.settle(session.ID, model.OutcomeResetFailed, "browser context: "+err.Error(), &session)
    return                                  // no spool.Retire(lease.Instance)
}
```

`Give` decrements `InUse`, so the broken instance returns to 0/8 and becomes the
next session's first choice. That is the loop that produced 1378 failures across
sessions 1159-3000 **[D]** + **[M]**.

**Readiness never covered the port the run used.** The SandboxTemplate probes
`/json/version` on port `cdp` = 9222, the bridge
(`config/sandbox/templates/sandboxtemplate.yaml:59-64`). The run used
`--cdp-transport ws`, which dials the `cdp-forward` sidecar on 9224 **[D]**. The
bridge answers 502 only for its own upstream failures (`apps/browser-sandbox/cdp-bridge.py:313`);
a dead forwarder is invisible to both.

Timeline for instance 43, from its `instances` row and session 1159 **[M]**:

```
arrival of session 1159        1794086184949659314
claim created                  1794086873346304436   (+688 s waiting for a claim)
instance 43 created            1794086873000000000
instance 43 Ready              1794087062135980172   (+189 s to become Ready)
session 1159 reset_failed      1794087065509584055   (+3.4 s later, 9224 refused)
instance 43 deleted            1794087508615914753   (run teardown)
```

## The capacity model, as built

The pool is a counting semaphore per instance, held in process memory **[D]**:

```
slots.Instance{ Capacity: 8, InUse: n }        slots.go:18-22
Take   : InUse < Capacity  -> InUse++          slots.go:203-219
Give   : InUse-- , pool.inUse--                slots.go:114-129
Retire : entry removed, pool.inUse -= InUse    slots.go:133-146
guard  : one sync.Mutex per pool               slots.go:45
```

Free slots go 8 → 7 → … → 0 on booking and back up on release. Nothing sets the
counter to a value; it only moves by one, and the entry disappears at `Retire`.

Three counts of "capacity" exist, and only the first is authoritative:

```
1  in-process slots pool     Capacity=8 per instance, InUse per instance    authoritative
2  warm pool replicas        desired/ready from the autoscaler              a hint
3  Kubernetes admission      cpu 10m, memory 128Mi request                  unrelated to contexts
```

There is no health dimension on any of them. `InUse` says a slot is taken; nothing
says the browser behind it can answer CDP. `Retire` is reached only by
`--policy destroy` (`shared.go:248-263`) or when a cached ws connection reports an
error (`isolate.go:148-162`). An instance that fails at dial time has no cached
connection to watch, so nothing retires it **[D]**.

## Edge cases the code walks into

| # | Case | What happens today |
|---|---|---|
| 1 | Browser accepts the TCP dial but never answers CDP | isolate fails, slot returned, instance stays (this run) |
| 2 | Browser dies mid-hold | `closeIsolated` fails, outcome `lost_unknown`, then `Give` — the instance is returned to the pool dead (`shared.go:264-289`) |
| 3 | All instances full, one unusable | the unusable one absorbs every new session (this run) |
| 4 | Claim Ready, transport dead | readiness covers 9222, ws uses 9224 |
| 5 | A session fails at acquisition | `acquisition_failed`, no retry, no requeue |
| 6 | Instance retired while leases are held | `Give` finds no entry and does nothing; the accounting stays consistent, the sessions still fail |
| 7 | Pool empty and 3000 goroutines call `Take` | one creator at a time (`provisioning`), the rest wait on a channel: no stampede, but also no bound on how many instances get created |
| 8 | Boot spikes overlap | `boot_concurrency` unset in both runs; the only guard is the opt-in gate |
| 9 | Memory limit 840Mi with 8 contexts | per-session memory p50 34 MiB, p99 340 MiB, max 394 MiB; the pod limit binds before the node does |
| 10 | Swap allowance | 8 MiB in run A, 16 MiB in run B — kubelet's LimitedSwap arithmetic, far below a browser's working set |

## What the report misleads you about

| Metric | Reads as | Actually |
|---|---|---|
| `mean_sessions_per_instance` 37.7 | 37 concurrent sessions per browser | cumulative successes / instances; concurrency is capped at 8 by the pool |
| `queue_wait p50` 0.0012 ms | the queue is idle | the admission gate only; claim→ready is `acquire`, 16-22 minutes |
| `boot_cpu_peak_max` 15.6 | one browser used 15.6 cores | impossible on a 6-core node; the sampler divides a cgroup delta by a short interval **[U]** |
| `max_sessions_per_instance` 54 | 54 contexts in one Chromium | cumulative count of completed sessions |

## What this leaves open

| Open | Evidence that would settle it |
|---|---|
| Why instance 43 never answered on 9224 | its pod logs; the pods are gone, so a rerun with the health gate instrumented is the only way |
| Whether the boot-peak sampler is wrong | compare `boot_cpu_peak_cores` against `cpu.stat` deltas over a fixed interval |
| Whether 8 contexts at 840Mi is over-subscribed | per-instance memory peak with contexts counted, which the report does not carry |
| Whether a retry would have saved run B | the run had 42 healthy instances and 1378 failures; a single retry to a different instance would have moved them |

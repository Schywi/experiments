---
title: 'Autoscaler signals: the demand formula is self-referential, and the pressure guard watches the wrong resource'
kind: analysis
state: hypothesis
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, autoscaler, capacity, control, state-hypothesis]
---

# Note 27 — Autoscaler signals

| | |
|---|---|
| Status | Analysis of the shipped autoscaler and a design for its inputs. No controller change built |
| Date | 2026-11-09 |
| Scope | What the autoscaler reads today, why both inputs mislead on this host, and which signals are available without new infrastructure |
| Related | [25 — capacity ramp](25-capacity-ramp-and-provisioning.md) · [26 — claim binding](26-claim-binding-and-warm-taxonomy.md) · [22 — queue and multiplexing audit](22-queue-multiplexing-audit-and-latency-breakdown.md) |

Evidence base: `apps/session-lab/internal/infra/pool/autoscale.go`, `internal/metrics` in the
agent-sandbox tree, and `config/session-lab/`.

## 1. What it reads today

```go
slots  := stats.Ready + stats.Pending                    // :132  claims holding + claims waiting
demand := ceil(slots / contextsPerBrowser)               // :137
target := demand
case PressureFull > 25 || Utilization > 0.85: target = desired - 1   // :157-158
case PressureFull < 5  && Utilization < 0.70: target = demand        // :159-160
else:                                        target = desired        // :162
if BootConcurrency > 0 && target > desired+BootConcurrency { target = desired + BootConcurrency }
clamp(target, Min, Max)
```

Two inputs, and each is wrong for this host in a different way.

## 2. The demand input describes the fleet it already has

`stats.Pending` counts claims that exist and are not Ready. On this runner almost nothing is in that
state, for three independent reasons:

| Waiter | Has a claim? | Counted in `Pending`? |
|---|---|---|
| session blocked inside `admission.Gate` | no | no |
| session blocked inside `slots.Pool.Take` waiting for a lease | no | no |
| claim bound to a *booting* warm-pool sandbox | yes | **no — it is bound** |
| claim built from template, not yet Ready | yes | yes |

[I] The third row is the common one under load (note 26 §1), and the first two are the 2,600 sessions
of note 22 §4.2. `Pending ≤ 1` in practice, because `slots.Pool` allows one cold path at a time
(note 25 §1). So `demand = (Ready + Pending)/8` evaluates to approximately the current replica count
— the controller is measuring itself. This is the mechanism behind the measured
`Pending = 0/1 while thousands waited`.

## 3. The pressure input watches CPU, and the resource that runs out is memory

`NodeLoad` carries `Utilization` (cores in use / cores available) and `PressureFull` (CPU PSI full
avg10). There is no memory term at all. The order run in note 22 stopped at 38 pods with 14.3 GiB of
RAM plus 7.7 GiB of zram in use against a 16 + 16 GiB envelope; that was a memory stop, and the
guard that is supposed to see saturation would not have seen it. Meanwhile the CPU figure the guard
watches stayed low: mean 1.97 of 6 cores in that run.

Two consequences:

- Inside the hysteresis band (`5 ≤ pressure ≤ 25`, `0.70 ≤ util ≤ 0.85`) the rule is *hold*, so a
  memory-saturated node can sit there indefinitely.
- Below the band it grows toward demand, which on the older host meant growing toward an OOM. The
  blind demand input is what prevented that by accident.

The runs since then reached 44 instances in the ledger and 58+ in a later, unrecorded run
([note 24](24-capacity-ledger.md)), so the wall is further out than the older note's estimate. Where
it is remains unmeasured, and that gap is what makes a memory term in `NodeLoad` a *measurement*
first and a control change second.

## 4. Signals that already exist

| Signal | Where it lives | Readable by the autoscaler today? | Cost |
|---|---|---|---|
| `status.replicas`, `status.readyReplicas` of the pool | the `SandboxWarmPool` object | **yes** — its Role grants `get, list, watch` on `sandboxwarmpools` | one more field in a `Get` it already performs |
| boots ordered but not serving | `replicas − readyReplicas` | yes, derived | free |
| per-pool metrics (`pending`, `ready`, replicas) | agent-sandbox `internal/metrics/warmpool_collector.go` | only through Prometheus, which the controller does not query | a new client and a scrape path |
| runner-side waiter depth | `metrics.Recorder.waiting` gauge; `admission.Queue.Snapshot()` per class | **no** — the runner is a host process, off-cluster | a channel: an object in the pool namespace, or a metrics endpoint the cluster can reach |
| node memory | `/proc/meminfo`, `/proc/pressure/memory` | yes, the same way CPU PSI is read | one more field on `NodeLoad` |

The first two lines are the important ones: the deficit `replicas − readyReplicas` is exactly the
pipeline the controller needs and cannot currently see, and it needs no new object and no new
permission.

## 5. What the controller should compute

```
servable_slots = readyReplicas × contextsPerBrowser
demand_slots   = waiters + bound_but_booting_sessions     (from the runner, if published)
target_replicas = ceil(demand_slots / contextsPerBrowser)
order          = max(0, target_replicas − replicas)        (replicas already includes in-flight)
```

The last line matters: `replicas` counts sandboxes that exist but are not Ready, so ordering
`target − replicas` is already a base-stock rule — it does not double-order the pipeline. The
controller's problem is upstream of that arithmetic; it is that `target` itself is computed from a
number that cannot move.

Adding a memory term is a separate, smaller change: `MemAvailable` below a floor, or memory PSI full
above a band, should hold or shrink the same way CPU pressure does. It cannot be calibrated until
the host's ceiling is measured and appended to [the capacity ledger](24-capacity-ledger.md), whose
newest row stops at 44 instances.

## 6. What a better signal does and does not buy

| | Effect |
|---|---|
| A truthful `target` | the pool is asked for the capacity that is actually needed, instead of being asked for what it already has |
| Ordered with the current `k = 1` provisioning | **time-to-capacity does not change.** The fleet still grows one cold path per 40 s (note 25); an honest target only tells it how far to go |
| Ordered with `MaxProvisioning > 1` | the two changes together are what move the tail |

So the signal fix is necessary and not sufficient, and the two notes are one experiment with two
dials. That is worth stating because the external review treated the signal as the top lever (see
[queue 05](../queue-scheduling/05-outside-review.md) §2a).

## 7. The experiment

1. **Dry, no cluster:** replay a recorded run's rows through the controller's formula and compare the
   three candidate demand inputs — `Ready + Pending` (today), `waiters`, and `waiters + booting` — as
   time series against the workload that was actually offered. The measure is how far each lags or
   leads the true demand.
2. **Live, one arm:** replace the demand term with published waiter depth (a small committed object
   in the queue-lab or sandbox namespace) and keep `k = 1`. Expect no change in time-to-capacity and
   a change in *when growth stops* — the controller should stop at the right size instead of
   overshooting or stalling.
3. **Live, both dials:** the same signal with `MaxProvisioning = 4` (note 25 §5), which is the arm
   that can move the tail.

## 8. Predictions

| # | Prediction | Falsified if |
|---|---|---|
| P1 | Today's demand term tracks the current replica count within ±1 for the whole of a run | it separates from the replica count and follows load |
| P2 | With a truthful target and `k = 1`, time-to-N-pods is unchanged | the fleet grows faster on the same workload |
| P3 | A CPU-only pressure guard never stops a run before the memory ceiling | a run stops on pressure while memory is comfortable |
| P4 | Adding memory pressure changes when growth stops, not how fast it grows | the growth slope changes with the pressure term |

## 9. Sources

| Claim | Source |
|---|---|
| demand formula, hysteresis, clamp | `internal/infra/pool/autoscale.go:126-180` [I] |
| `NodeLoad` fields | same file, `:26-32` [I] |
| `Pending = 0/1` while thousands waited | note 22 §4.2 [M] |
| CPU mean 1.97/6, memory 89 % at 38 pods | note 22 §1-2 [M] |
| pool status fields | agent-sandbox `extensions/api/v1beta1/sandboxwarmpool_types.go:96,100` [I] |
| Role grants | `config/session-lab/templates/role.yaml:15-19` [I] |
| `waiting` gauge | `internal/platform/metrics/metrics.go:180-195` [I] |
| 44 instances, 352 slots | [note 24](24-capacity-ledger.md) [M] |
| 58+ instances survived | owner, 2026-11-09; unrecorded (note 24) |

---
title: 'The capacity ramp: one cold path at a time, and why the pod cap is not the lever'
kind: analysis
state: hypothesis
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, capacity, provisioning, latency, state-hypothesis]
---

# Note 25 — The capacity ramp

| | |
|---|---|
| Status | Analysis. The mechanism is read from the code and the arithmetic checked against a recorded run; the fix is designed and not built |
| Date | 2026-11-09 |
| Scope | Why the fleet grows at one pod per cold start, what that costs in minutes, and which knob actually bounds it |
| Related | [22 — queue and multiplexing audit](22-queue-multiplexing-audit-and-latency-breakdown.md) · [24 — capacity ledger](24-capacity-ledger.md) · [26 — claim binding](26-claim-binding-and-warm-taxonomy.md) · [27 — autoscaler signals](27-autoscaler-signals.md) · [queue 03](../queue-scheduling/03-hold-length-sensitivity.md) |
| Fleet baseline | 44 instances × 8 = 352 slots, run `20261107T235847Z` ([note 24](24-capacity-ledger.md)). A 58+ instance run is reported and unrecorded |

Labels: **[M]** measured, **[D]** derived, **[I]** inferred from code, **[H]** prediction.

## 1. The mechanism

Three facts, each one line of code:

1. `slots.Pool`'s `Provide` callback is `Runner.acquireInstance` (`internal/runtime/session/shared.go:24-61`),
   which creates the claim **and then blocks on `r.ad.WaitReady`** (line 47). Claim creation is one
   API POST; the function does not return until the sandbox is serving.
2. `slots.Pool.Take` admits **one** provider call at a time (`internal/runtime/slots/slots.go:152-170`):
   while `p.provisioning > 0`, every other caller parks on a shared channel and re-loops when it
   closes.
3. There is **no flag** for that bound. `rg provisioning internal/platform/config/config.go` returns
   nothing. `--boot-concurrency` is a second gate above it (`shared.go:175`, released at `:218`),
   defaulting to 0 = unbounded.

So the exclusive counter that was meant to stop "a burst of sessions each creating an instance"
also serialises the 40-second boot, because the two costs live inside one function. The result is a
fleet that grows at **8 slots per 40 s = 0.2 slots/s**, regardless of how many sessions are waiting
or how many pods the node could hold.

The 25.3-minute ramp in the recorded run [M, note 22] is this identity: 38 pods from a handful,
~36 sequential cold paths at 40 s each, ≈24 min [D].

## 2. What the ramp costs, as arithmetic

With `k` cold paths in flight, `L` the boot, `s` leases per pod, the fleet grows at `k·s/L` slots per
second, so cumulative capacity is `C(t) = (k·s/L)·t` and work completed is `∫C = (k·s/(2L))·t²`.

For the recorded workload — 3,000 sessions, mean hold 111 s, so **333,000 slot-seconds** — against
the ledger's newest measured fleet of 352 slots:

| `k` | growth | 352 slots reached at | makespan | reading |
|---|---|---|---|---|
| 1 (today) | 0.2 slots/s | 29.3 min | **30.4 min** | the ramp outlives the work |
| 4 | 0.8 slots/s | 7.3 min | **19.4 min** | −36 % |
| ∞ (no ramp) | — | 0 | **15.8 min** | the floor any ramp is measured against |

[D] Two things fall out, and both are counter-intuitive enough to be worth stating plainly:

- **Raising the pod cap changes almost nothing while `k = 1`.** At `k = 1` the work finishes when
  `0.1·t² = 333,000`, i.e. at t = 1,825 s, by which point the fleet has reached ~46 instances. A cap
  of 44, 58 or 80 gives a makespan within a minute of each other, because the last instances are
  created after the queue is empty. The cap and the ramp are different knobs, and only the ramp is
  binding.
- **More pods with the same `k` can make the tail slightly worse**, because the later pods extend the
  ramp while the earlier ones cannot serve work that has not arrived.

## 3. The two identities to keep

| Identity | Meaning |
|---|---|
| `C(t) = k · 8 · t / L` | capacity is linear in time, and its slope is the whole story |
| `T_drain = √(2 · W · L / (k · s))` | with `W` slot-seconds of work, the fleet serves it in this time while ramping |

For a fleet already at `C`, the same work drains in `W / C`. The ramp case is therefore
`√(2WL/(ks))` against `W/C`, and the ramp wins whenever `L` is large relative to the workload's
duration — which is the short-hold regime of [queue 03](../queue-scheduling/03-hold-length-sensitivity.md).

## 4. What is not known

| Unknown | Why it matters | How to measure |
|---|---|---|
| `L(k)` — boot time as a function of concurrency | a Chromium boot peaks near a core; four at once on 6 cores may slow each and start failing the readiness probe (30 × 2 s = 60 s against ~40 s solo), and a failed `WaitReady` **deletes the claim** (`shared.go:50`) and re-acquires — churn rather than growth | sweep `k`, compare `Instance.ReadyNS − CreatedNS` per instance, count `WaitReady` failures in the store's events |
| Where the host actually stops | the ledger's rows stop at 44 instances and a later run is reported at 58+; the envelope is unchanged (`MemTotal 16.3 GB`, `SwapTotal 16.8 GB`, read 2026-11-09), so per-pod memory is lower than the older estimate or zram carries more | append a ledger row in [note 24](24-capacity-ledger.md); needs a run id |
| Whether the pool controller creates `k` sandboxes concurrently | if it serialises internally, raising `k` in the runner changes nothing | pod `creationTimestamp` deltas for a burst of pending claims |

## 5. The change the experiment needs

One additive field, defaulting to today's behaviour so the control arm is the absence of a setting:

```go
// internal/runtime/slots
type Options struct {
    ...
    // MaxProvisioning bounds instance creations in flight. One is what every
    // existing run does; a larger value lets the fleet grow k pods per boot
    // instead of one, at the cost of k concurrent CPU spikes on the node.
    MaxProvisioning int
}
```

and the runner's `boot` gate is the natural source for `k`, since it is already derived from the
measured boot CPU peak in preflight (`run.go:341-352`). Two gates doing the same job at different
layers is the defect; one gate, in the pool, is the fix.

**Shipped:** `slots.Options.MaxProvisioning`, `--max-provisioning`, and the publication of the
bound in the autoscaler's settings — see
[queue 06](../queue-scheduling/06-autoscaler-v2.md). The `k` sweep in §7 has not run.

## 6. The ordering question inside the ramp

A cold path is an **order** with lead time `L` and yield `s`. What a pool should place is a
base-stock quantity, not one order per waiter:

```
orders_needed = max(0, ceil(W / s) − P − S)
W = sessions waiting now
P = cold paths in flight (each yields s slots in ~L)
S = ready, unbound sandboxes
```

The failure the naive rule produces is over-ordering: an order placed at `t` lands at `t+L`, by
which time the boots already in flight have delivered the capacity that motivated it. On this host
that surplus is not merely idle, it is memory. The counter for it is computable from rows the store
already writes: **instances where a free slot existed for ≥ 1 s during `[created, ready]`** — boots
that were already unnecessary when they landed. Call it the redundant-boot rate; it prices
"my claim was worthless".

With a deterministic `L`, the reorder point is `P_target = ceil(λ_w · L / s)` where `λ_w` is the rate
at which sessions *become waiters* (not the arrival rate). This workload admits something stronger:
`schedule.Plan` holds every arrival offset and hold before the run starts, so the required capacity
curve is computable at `t = 0` and the boot schedule is that curve shifted back by `L`. That is an
**oracle prewarm** arm, and it bounds what any adaptive controller can achieve — the same role
`oracle-sjf` plays in the queue study.

## 7. The experiment

| Arm | Capacity policy | What it isolates |
|---|---|---|
| A | today: one cold path | the control |
| B | `MaxProvisioning = 4` and `= 8` | the ramp slope |
| C | base-stock: `orders = max(0, ceil(W/8) − P − S)`, re-evaluated each second | over- vs under-ordering |
| D | oracle feed-forward from `schedule.Plan`, boots `L` ahead of need | the ceiling |

Metrics, all from existing rows plus the `MaxProvisioning` change: time to N pods; `L` distribution
per `k`; `WaitReady` failures and claim deletions; redundant-boot rate; per-session acquire
p50/p95; and the three-way wait split from note 02's `--replay` mode.

## 8. Predictions

| # | Prediction | Falsified if |
|---|---|---|
| P1 | `k = 4` roughly halves the makespan on the recorded shape | the makespan moves less than 20 % |
| P2 | `L` rises with `k`, and `WaitReady` failures appear above some `k` on 6 cores | `L` is flat to `k = 8` |
| P3 | The cap between 46 and 80 pods is irrelevant at `k = 1` | a bigger cap shortens the makespan at `k = 1` |
| P4 | `D ≈ C ≫ B ≫ A` on redundant boots | the base-stock rule orders as often as the naive one |

## 9. Sources

| Claim | Source |
|---|---|
| one cold path at a time | `internal/runtime/slots/slots.go:152-170` [I] |
| `Provide` waits for readiness | `internal/runtime/session/shared.go:24-61`, `WaitReady` at :47 [I] |
| no configuration for it | `internal/platform/config/config.go` has no such flag, checked 2026-11-09 [I] |
| 25.3-minute ramp, 38 pods, ~40 s boot | note 22 §1-2 [M] |
| 44 instances / 352 slots, the newest ledger row | [note 24](24-capacity-ledger.md), run `20261107T235847Z` [M] |
| the makespan table | [D] from §2 at 352 slots |
| 58+ instances reported | owner, 2026-11-09; unrecorded, listed in note 24 |
| `MemTotal`/`SwapTotal` | `/proc/meminfo`, read 2026-11-09 [M] |

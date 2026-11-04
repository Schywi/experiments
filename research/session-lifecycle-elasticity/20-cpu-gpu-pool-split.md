---
title: 'CPU/GPU pool split, per-session routing'
kind: analysis
state: hypothesis
phase: 8
date: 2026-11-02
branch: 'research/session-lifecycle-elasticity'
tags: [session-lifecycle, gpu, state-hypothesis]
---

# Note 20 — CPU/GPU pool split, per-session routing

| | |
|---|---|
| Status | Design note, implementation follows — retained for routing and device containment, not for capacity (see §0) |
| Date | 2026-10-31 |
| Phase | 8 — session lifecycle elasticity |
| Branch | `research/session-lifecycle-elasticity` |
| Related | [19](19-gpu-pool-a-b.md), `config/sandbox/`, `apps/session-lab/internal/runtime/session/` |

## 0. Why this split survives note 19

[19](19-gpu-pool-a-b.md) settled the capacity question this note was originally motivated by: at 30
concurrent sessions, node CPU was **1.53 cores against 1.58 of 6** with and without acceleration,
because `session-lab`'s hold samples cgroups and never requests a frame. A GPU tier therefore buys
**no extra sessions per node**, and nothing below depends on it doing so.

What the split is still for:

1. **Containment of a single exclusive device.** There is one card and one accelerated browser at a
   time (`amd.com/render: 1`, and it currently reports `0/0`). A distinct pool is what stops a GPU
   request from being placed on every session that would like one.
2. **Routing by workload class, which is independent of capacity.** Sessions that capture frames —
   screenshot loops, VLM grounding ([E5](../../benchmark/experiments/e5-vlm-grounding/RESULTS.md)),
   the console's tap-to-screenshot — measure **−26 % CPU per browser** when accelerated
   ([19](19-gpu-pool-a-b.md) §1). Sessions that only hold a page gain nothing.
3. **The routing mechanism is the deliverable**, not a capacity win: one pool name per session
   instead of one per process. §2–§6 stand unchanged.

What it is explicitly **not** for: raising sustainable concurrency. The recorded ceiling under this
workload is CPU requests and the control plane (notes 12, 13, 18), not rendering.

## 1. What this is

Two sandbox tiers served from one cluster: a CPU-only pool and a GPU pool. A
session is routed to one of them at claim time by its workload class. This
replaces "one pool name per process" with "one pool name per session".

## 2. Object chain already supports it

`SandboxClaim` names a `SandboxWarmPool`, which names a `SandboxTemplate`, which
carries the pod spec. Two templates + two pools therefore produce two kinds of
pod, and the claim decides which. No controller change: the controller adopts
from the pool named in the claim.

## 3. Routing decision

| Route | Where | What changes |
|---|---|---|
| class | `runtime/session.poolFor` | map class → pool from `RunConfig.PoolByClass` |
| per session | `schedule.Planned.Pool` | set at generation, overrides the class map |
| fallback | `RunConfig.Pool` | current behaviour when neither exists |

One function, one call site per acquire path:

```
poolFor(session) string:
  Planned.Pool if set
  else PoolByClass[class] if mapped
  else Pool
```

The autoscaler and `infra/kube` stay untouched: `Acquire` already takes the pool
name as an argument, and each pool is sized from its own claim count.

## 4. Config

`--pool-by-class normal=gpu-pool,bulk=cpu-pool`, parsed into
`RunConfig.PoolByClass map[string]string`. A class with no entry falls through to
`--pool`. The existing `--pool` stays the default for every class.

## 5. Shared-browser path

`--contexts-per-browser > 1` uses one slot pool per warm pool, created lazily:

- `Runner.spools map[string]*slots.Pool` replaces the single `spool`.
- `spoolFor(pool)` builds the pool with a provider that calls
  `acquireInstance(ctx, pool)`.
- `acquireInstance` and the retry take the pool name instead of reading
  `r.cfg.Pool`.
- `watchBrowser` retires a dead browser from every slot pool (retire is a no-op
  for pools that do not hold the instance).

## 6. Isolate path

`session()` computes the pool once with `poolFor(planned)` and passes it to
`Acquire`; the stored session row carries that pool name instead of `r.cfg.Pool`.

## 7. Out of scope

The console (`SANDBOX_WARM_POOL`) keeps one pool per console instance; the router
picks the console, not the pool. Pool replication, autoscaling and admission
stay class-based and unchanged.

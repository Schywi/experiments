---
title: '03 — The Gemini design, reviewed against this cluster'
date: 2026-10-31
kind: analysis
state: unknown
tags: [cpu, state-unknown]
---

# 03 — The Gemini design, reviewed against this cluster

The paper proposes a five-plane architecture: token-bucket ingress, predictive-headroom scheduler,
Redis two-phase leases, Firecracker microVMs with snapshot restore, and CFS burst. Below, each
mechanism is judged on one criterion: **what problem in our code does it solve, and what measurement
would prove it?**

## Where the paper is right about us

| Claim | Verdict for this cluster |
|---|---|
| Browser workloads are bimodal: idle at rest, large transient spike at startup | **True, and we had no measurement of it.** Fixed by `session_lab_node_cpu_cores` + `session_lab_instances_booting` (this change) |
| CPU limits amplify latency (CFS quota, multi-threaded quota exhaustion) | **True and measured**: 248 of 360 sessions were throttled under `cpu: "1"`; hold latency was an artefact of the quota. The limit is gone (note 12) |
| Concurrent admissions all observe the same cool node, then collide | **True in our scheduler**: `--admit` defaults to unbounded and the pool cannot see CPU. In `20261031T024806Z` it scaled into `Insufficient cpu` |
| Token bucket / sliding window to shape arrival | **Directionally right, cheapest form fits us**: our admission gate already has the shape; it just needs a bound and to be held through the boot |

## Where it does not apply

| Proposed | Why it is wrong here |
|---|---|
| **Resting CPU 3–10 mCPU per session** | Measured: 19–42 mCPU per session as a share of a shared pod, and 1.26–4.67 core-seconds per session over a 30–75 s hold. A density model built on 5 m over-promises by 4–8× |
| **`N = 6000/200 = 30` or `N = 300–500` sessions** | Both ignore memory requests (256Mi per pod) and the 110-pod cap. On this node the ceiling is ~52–66 browsers, i.e. 168–528 contexts at 8 each |
| **Redis two-phase provisional leases, TTL, CAS** | We have one scheduler, one node, one process. `slots.Pool` already serialises provisioning (`slots.go:74-100`). A distributed ledger solves contention we cannot have |
| **Predictive headroom with exponential decay** | Requires `C_spike`, `C_rest`, `T_init`, `λ_decay` per page class. We have none of them yet; the ladder produces them before any decay model is justified |
| **Firecracker microVMs + snapshot restore** | Not a scheduler change: it replaces the sandbox runtime. Our cold start is 12–80 s and dominated by image + Chromium boot, not by namespace setup. Out of scope, and the platform under test is `agent-sandbox`, not a KVM VMM |
| **`cpu.max.burst`** | No CPU limits means no quota to bank. Bursting is what happens already by default |
| **Envoy/NGINX ingress rate shaping** | We generate load inside one process; there is no external ingress to shape. The equivalent is the admission gate |
| **"95 % of capacity idle at rest"** | Our resting load is not idle: 52 browsers × ~47 mCPU share ≈ 2.4 cores of a 6-core node before any page work |

## What we take from it

1. **The failure mode is real and it is ours**: overlapping boots on a node whose average CPU looks
   low. Mechanism to add: a **boot permit** (implemented: `--boot-concurrency`, held from claim
   request until the browser has a context).
2. **Measure the spike before modelling it**: node CPU + booting gauge, 1 s resolution.
3. **Derive thresholds from baseline runs**, not from a paper. The ladder in this folder's README
   does that.
4. **Do not add a dependency to solve a race that cannot occur** in a single-scheduler, single-node
   system.

## The one place the paper's TOCTOU framing is exactly right

Our equivalent of time-of-check/time-of-use is not in the datastore; it is:

```
t0  two sessions admitted, node CPU 40% (cool)
t1  both call Take; one provisions, the other waits — correct
t2  both browsers boot at once, each spawning hundreds of millicores
t3  node CPU 100%, every peer session slows down, boots take longer
t4  longer boots mean more boots overlap → the paper's positive feedback loop
```

`--boot-concurrency` breaks the loop at t2 with a counter, which is all a single-node scheduler
needs. A token bucket over initialization *energy* becomes worthwhile only if the measured spike is
large enough that counting boots is too coarse — and that is a measurement, not an argument.

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)

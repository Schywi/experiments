# Bounded replication, HAProxy, and autoscaling — how they differ

This note explains what the Worm experiment in this repository actually is,
and why it is **neither** a load balancer like HAProxy **nor** a Kubernetes
Horizontal Pod Autoscaler. It ends with an ELI5 version and the per-worker
memory budget.

## TL;DR

The Worm lab is a **bounded, observable self-replication protocol**. A worker
Pod asks for a sibling; a Go controller alone turns that request into a
Deployment scale change, and it refuses to go past `spec.maxReplicas`. It
borrows Kubernetes' scaling *primitive* (the `Deployment` `scale` subresource
plus a `ResourceQuota` backstop), which is why people reach for the words
"autoscaling" or "HAProxy". But the *trigger*, the *actor*, and the *purpose*
are all different.

- HAProxy is about **routing traffic** across backends that already exist.
- Autoscaling (HPA) is about **matching capacity to load** using metrics.
- This lab is about **emergent growth by intent** — a Pod reproduces because it
  asked to, not because traffic or CPU rose.

## Part 1 — What each thing is

### HAProxy (a traffic multiplexer)

HAProxy accepts *incoming* connections and spreads them across a set of
backends, with health checks, timeouts, and retries. It never creates or scales
a process. Its job is distribution, not growth.

### Kubernetes HPA (autoscaling)

The Horizontal Pod Autoscaler runs a control loop that reads a metric (CPU,
memory, requests-per-second, or a custom metric), computes a desired replica
count from a target, and patches the workload's `scale` subresource — **up and
down** — so capacity tracks demand. The decision is autonomous and
metric-driven.

### This repository (bounded replication by intent)

A worker Pod starts, sends **one idempotent replication intent** to the Go
controller (`POST /v1/replication-intents`), then emits telemetry samples.
Distinct intent IDs increment `status.desiredReplicas`; the controller
reconciles the worker `Deployment` to that count, never above
`spec.maxReplicas`. Workers hold **no Kubernetes credentials** and cannot scale
themselves.

Relevant code:

- `apps/controller/internal/intent/service.go:47-49` — the cap check
  (`ErrCapReached` once `desiredReplicas >= maxReplicas`).
- `apps/controller/internal/controller/worm_controller.go:34-53` — clamps the
  desired count to `spec.maxReplicas` and patches `deployments/scale`.
- `apps/worker/src/main.rs` — the worker's single, retry-safe intent, then
  sample emission.
- `apps/worker/chart/templates/resourcequota.yaml` — the namespace memory
  backstop.

## Part 2 — Side by side

| Dimension | HAProxy | Kubernetes HPA | This Worm lab |
| --- | --- | --- | --- |
| Primary job | Distribute connections | Match capacity to load | Bounded self-replication |
| Scale trigger | none (no scaling) | a metric crosses a target | a worker **asks to reproduce** |
| Who decides | operator-configured routing | control plane, autonomous | custom controller, on request |
| Direction | n/a | up **and** down | monotonically grows toward the cap |
| Unit being scaled | backends (pre-existing) | request servers | independent telemetry emitters |
| Load balancing | core purpose | none | **none** |
| Statefulness of replicas | interchangeable | interchangeable | each has its own identity (`WORM_ID`) |
| Ceiling mechanism | n/a | `maxReplicas` | `spec.maxReplicas` + `ResourceQuota` |
| Kubernetes primitive reused | none | `deployments/scale` | `deployments/scale` (shared with HPA) |

The **only** gene the lab shares with HPA is the mechanism: desired-vs-current
reconciliation against the `Deployment` `scale` subresource, with a hard
ceiling. The *policy* — reproduce on an intent, never shrink, do not read load
metrics — is the opposite of HPA. And it shares essentially nothing with
HAProxy, because nothing here distributes traffic.

## Part 3 — Why "self-replicating" ≠ "autoscaling"

1. **Trigger.** HPA reacts to observed load. The Worm reacts to an *intent* the
   Pod sends about itself. A worker reproduces even at zero traffic.
2. **Actor.** HPA is autonomous: the control plane decides. Here the Pod only
   *requests*; the controller *decides*, and the Pod has no API credentials.
3. **Direction.** HPA shrinks when load falls. The Worm's `desiredReplicas` is
   cumulative and only ever increases up to the cap.
4. **Intent identity.** Repeated intents are deduplicated by ID (idempotency).
   HPA has no such identity — it just recomputes a target.
5. **Purpose.** The point is a *visible, bounded* replication protocol that
   Kubernetes can observe, restart, constrain, and stop — not throughput
   management. See `blog/2026-09-05-bounded-lua-wasm-replication.md`.

## Part 4 — ELI5 (explain like I'm five)

**HAProxy is a traffic cop.** Cars (connections) come down a road, and the cop
waves each one to one of several open lanes (servers) so no lane gets jammed.
The cop never builds new lanes.

**Autoscaling is a smart restaurant manager.** When the dining room is full,
the manager hires more waiters; when it empties, the manager sends some home.
The manager watches how busy it is and matches staff to the crowd.

**This project is a living cell that reproduces on purpose — but with a
growth cap.** Each cell (worker Pod) says "I want to make one baby." A strict
parent (the Go controller) hears the request and builds exactly one more cell.
The parent has one rule: **never more than 20 cells**. If a cell asks past 20,
the parent says no.

- The cells can't build babies themselves — they must ask. (Workers have no
  Kubernetes keys.)
- The cells don't get busy or quiet; they just keep making samples and asking.
  So it is not the "smart restaurant" (autoscaling).
- Nothing routes cars to lanes. So it is not the "traffic cop" (HAProxy).

It's a **bounded family tree**, not a traffic system and not a staffing system.
Kubernetes is the building inspector that watches, can restart a cell, and
enforces the "no more than 20" rule with a hard quota.

## Part 5 — Per-worker memory budget

Each worker declares:

```yaml
resources:
  requests:
    memory: 600Ki
  limits:
    memory: 32Mi
```

Source: `apps/worker/chart/values.yaml`.

- **Request: 600 KiB (~0.59 MiB) per worker.** Kubernetes requires integer
  bytes, so `0.6Mi` is rejected — the accepted equivalent is `600Ki`.
- **Limit: 32 MiB per worker.** Because request < limit the worker is
  *Burstable* on memory (it reserves 600 KiB, may burst to 32 MiB, and is
  OOM-killed above it).
- **Measured real usage: ~0.35 MiB RSS** (356 KiB VmRSS / VmHWM) for the
  release static-musl binary — about 1% of the limit. See the measurement
  method below.
- **Namespace ceiling: 640 MiB** via the `worm-worker-cap` ResourceQuota
  (`apps/worker/chart/templates/resourcequota.yaml`), enforced on **both**
  `requests.memory` and `limits.memory`.
- **Worker cap: still 20.** The `limits.memory` quota (`640 MiB ÷ 32 MiB = 20`)
  and the controller's `spec.maxReplicas: 20` (`apps/controller/chart/values.yaml`)
  continue to bind. Lowering the request alone does **not** raise the cap — the
  32 MiB limit still caps at 20. Raise `maxReplicas`, the quota, the limit, and
  node memory together to go higher.

Two independent walls enforce 20: the controller refuses the 21st intent, and
the quota makes a 21st Pod unschedulable. A desired count above the quota is
expected to remain **pending**, not to bypass the cap (`README.md`).

### How the 0.35 MiB figure was measured

Build the worker exactly as its `Containerfile` does (Rust 1.86.0,
`x86_64-unknown-linux-musl`, `opt-level=z`, LTO, `strip`, `panic=abort`),
then run it and read `/proc/<pid>/status`:

```
VmRSS : 356 kB   (resident)
VmHWM : 356 kB   (peak)
VmSize: 652 kB   (virtual)
Threads: 1
binary: 467 KB on disk
```

The worker is a single-threaded static binary that does one TCP POST and then
sleeps and prints one small JSON line per second, so its resident set stays
under 1 MiB.

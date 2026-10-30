# Note 15 — Capacity plane and demand plane: classes, browser contexts, packaging

| | |
|---|---|
| Status | Design decision, not implemented |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Code read | `run.go`, `schedule.go`, `adapter/kube.go`, `apps/browser-sandbox/cdp-bridge.py` |
| Follows | [14 — leaked claims and polling storm](14-leaked-claims-and-polling-storm.md), [13 — client-go throughput and density limits](13-client-go-apf-and-density-limits.md) |

---

## 1. The split, in plain terms

Two things are happening at wildly different speeds, and today one object carries both.

**Slow: capacity.** A `SandboxClaim` that adopts a warm sandbox, or cold-starts a pod. Takes 100 ms warm, 40–80 s cold. Kubernetes does this well and should keep doing it.

**Fast: allocation.** "Which instance serves session 7, and can I have a slot right now?" Takes microseconds. Today the answer is a claim per session, so a 600-session run asks Kubernetes to make 600 slow decisions.

```
CAPACITY PLANE (k8s, seconds)                 DEMAND PLANE (our runtime, milliseconds)
┌────────────────────────────────────┐        ┌──────────────────────────────────────┐
│ SandboxWarmPool.spec.replicas      │        │ admission: class → queue position     │
│ claim → sandbox → pod              │◄───────┤ slot allocator: (instance, context)   │
│ scheduling, eviction, node pressure│        │ lease, hold, release, dispose         │
│ autoscaler: deficit → replicas     │───────►│ session lifecycle, store, metrics     │
└────────────────────────────────────┘        └──────────────────────────────────────┘
              every 5 s                              every 20–3000 ms
```

The only number crossing the boundary is **slots needed** and **slots available**.

## 2. Admission control: three classes, three rules

No caps. Nothing is rejected by us; rejection stays whatever the platform does (`Insufficient cpu`), recorded as a boundary event.

| Class | Queue position | Triggers new capacity | Interruptible |
|---|---|---|---|
| `normal` | first | yes | no |
| `batch` | after normal | yes, only after the deficit survives one autoscaler interval | no |
| `bulk` | last, may starve | **never** | yes — suspend/resume, not kill |

That is the whole design. One queue per class, one ordering rule, one boolean per class for "may ask for capacity".

```
admit(session):
    if slot := allocator.TryTake():          # any free (instance, context)
        return slot
    enqueue(session.class, session)
    if classMayScale(session.class) and deficitPersisted(interval):
        capacity.Request(+1 claim)
```

**On the question I left open last round — the third item you asked me to explain.** I had written "reserved[normal] = N idle slots held for interactive traffic". Concretely that means: the allocator keeps a few slots empty and refuses to give them to `batch`/`bulk`, so a `normal` arrival never waits behind bulk work. It costs idle capacity and needs a number nobody can derive honestly. So drop it: **no reservation in v1.** Classes only change queue order and who may scale the pool. If interactive starvation shows up in a run, that is the signal to add reservation with a measured number behind it.

## 3. Browser contexts instead of one claim per session

One Chromium can hold several isolated browsing contexts, and the bridge already carries arbitrary CDP commands (`cdp-bridge.py:283`):

```
Target.createBrowserContext          → browserContextId      ~5–20 ms
Target.createTarget{url, browserContextId}
   ... the session runs here ...
Target.disposeBrowserContext         → state gone              ~2–5 ms
```

What a context separates and what it does not:

| Separates | Shares |
|---|---|
| cookies, localStorage, IndexedDB, cache | the Chromium process |
| permissions, origin storage | the cgroup (so pod-level CPU/memory) |
| no residue from the previous session | the main thread, the GPU process |

Accepted trade-off (your call, recorded here): **a browser crash takes every context in it.** Eight contexts means a blast radius of eight sessions instead of one. A `Target.targetCrashed` watchdog plus rebinding the affected sessions to a fresh instance is the mitigation, and it is worth adding at the same time as the multiplexing, not later.

Contexts are created per session and disposed at release. Reusing a context would carry state, which is the thing `about:blank` was pretending to fix.

## 4. What changes, minimally

| Today | v1 after |
|---|---|
| lease = claim (`run.go:3`) | lease = `(instance, browserContextId)`, a claim holds 1..N slots |
| `Reset` → `about:blank` | dispose context + create the next one (~25–45 ms, close to today's measured reset p50 of 35 ms) |
| instance count = concurrency | instance count = ceil(slots / `--contexts-per-browser`) |
| `--workers` bound | class queue + slot allocator |
| sampler keyed on pod UID | per-session CPU = pod CPU ÷ contexts in use, **labelled "shared"**; cgroup stays the instance truth |

`--contexts-per-browser 1` reproduces today's behaviour exactly, so the existing run records stay comparable and nothing breaks while the feature is unproven.

Density gain is in claims and pods, not CPU:

| | now | 6 contexts per browser |
|---|---|---|
| schedulable pods (CPU requests) | ~27 | ~27 |
| concurrent sessions | 28 measured | up to 162, CPU-bound before that |
| claims created for 600 sessions | 600 (+600 deletes) | ~100 (+100) |
| extra memory per slot | — | ~40–80 MiB renderer + shared browser |

The claim churn is what took the API server down at 22–40 replicas. That is what this removes.

## 5. Autoscaler becomes slot-based

```
target_slots    = ready_sessions + queued(normal) + queued(batch)
target_replicas = clamp(ceil(target_slots / contexts_per_browser), min, max)
```

`bulk` never moves the pool. The existing `session_lab_autoscaler_*` series switch from claims to slots, and the dashboard reports slots as well as pods.

## 6. Packaging: what "infra vs runtime" looks like in Go

Current layout is flat — eleven siblings under `internal/`, and the plane boundary exists only in comments:

```
internal/{adapter,autoscale,config,measure,metrics,model,report,run,schedule,store}
```

Proposed, with the boundary expressed as directories. This is idiomatic Go: `internal/` already prevents outside import, `cmd/` holds binaries, and grouping by plane makes "who may touch the API server" a compile-time fact rather than a review rule.

```
apps/session-lab/
├── cmd/
│   ├── session-lab/            # experiment runner binary
│   └── pool-autoscaler/        # capacity binary
└── internal/
    ├── runtime/                # DEMAND PLANE — imports infra/sandbox, never infra/kube
    │   ├── schedule/           # arrivals, plan, buckets
    │   ├── admission/          # classes, queue, gate (next step)
    │   ├── session/            # lease, hold, release (was run/)
    │   └── report/             # JSON + text summary
    ├── infra/                  # CAPACITY PLANE — the only k8s caller
    │   ├── sandbox/            # Adapter interface, Instance, ClaimStats, Fake
    │   ├── kube/               # kube.go + watch.go + cgroup path helpers
    │   ├── pool/               # autoscale (package pool)
    │   └── measure/            # cgroup, metrics-server, boundary, Reading
    └── platform/               # shared, plane-agnostic
        ├── config/
        ├── model/
        ├── store/
        └── metrics/

Applied in `e888f24`. `Reading` moved from the runner into `infra/measure` because
`measure` was importing the runtime to name its own return type; the runner now
aliases it (`type Reading = measure.Reading`), so the dependency points one way.
Only `cmd/session-lab` imports `infra/kube` — checked with `rg "infra/kube" internal/runtime/`.
```

The one rule that pays for itself: **`internal/runtime/*` must not import `internal/infra/kube`.** It imports `internal/infra/sandbox` (the interface), which is exactly the seam that lets the scheduler and admission be tested without a cluster — and the seam that keeps Kubernetes on the capacity side.

Move mechanics: `git mv` per package plus import rewrites, no behaviour change, `go build ./... && go test ./...` as the proof. Do it as one commit before the admission/context work, so later diffs are about behaviour instead of paths.

## 6a. What shipped

| Piece | Where |
|---|---|
| `Class` (normal/batch/bulk), rank, `MayScale`, `Interruptible` | `internal/platform/model/model.go` |
| `Queue` (per-class counts, never rejects) and `Gate` (bounded concurrent admissions, class-ordered, cancellation-aware) | `internal/runtime/admission/admission.go` |
| `--class` and `--admit` flags; class recorded in the run config | `internal/platform/config/config.go` |
| Claim label `session-lab.io/class`; bulk claims counted in their own bucket | `internal/infra/kube/kube.go`, `watch.go` |
| Pool sizing ignores bulk: `target = ready + pending` | `internal/infra/pool/autoscale.go` (unchanged path, new data) |
| Admission wait recorded per session and printed | `internal/runtime/session/run.go`, `internal/runtime/report/report.go` |

Deliberately not built yet: browser contexts (`--contexts-per-browser`), a slot allocator, preemption.
The gate bounds *concurrent acquisitions*, which is the part that hurts the API server; the slot
allocator only becomes meaningful once one Chromium serves several sessions.

## 7. Parallel research that may decide the class question

A separate experiment, run by another agent under the `report` area, is measuring what happens to
the *cost* of a session when its memory is compressed or swapped: whether a suspended or idle
sandbox pays a latency tax when it wakes, and whether `batch` and `bulk` traffic can be parked in
swap/zram instead of holding resident capacity.

If that holds up, it answers the batch/bulk question better than an admission class can: a class
that only reorders a queue keeps the capacity reserved, while compressed or swapped idle state
releases it. None of it is production evidence yet, so this note does not depend on it — the classes
in §2 stand on their own and are the smaller change.

## 8. What this deliberately leaves out

- Per-context CDP metrics beyond what section 4 lists. Pod CPU divided by contexts is honest if labelled; a per-target flamegraph is not needed to answer a capacity question.
- Preemption between classes (`bulk` suspended to make room for `normal`). Recorded as an option, unbuilt.
- Reservation of idle slots.
- Any second scheduler process. One scheduler owns the queue, as today.

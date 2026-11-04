---
title: 'Phase 8 — session lifecycle elasticity (2026-10-29 → 2026-11-02)'
date: 2026-11-04
kind: narrative
phase: 8
tags: [session-lifecycle, autoscaling]
---

# Phase 8 — session lifecycle elasticity (2026-10-29 → 2026-11-02)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../apps/session-lab/`](../apps/session-lab/README.md),
[`../config/session-lab/`](../config/session-lab/README.md) · **design notes:**
[`../research/session-lifecycle-elasticity/`](../research/session-lifecycle-elasticity/README.md)
(22 notes, `01`–`20` plus `08.1`, `08.2` and a handoff)

Phase 7 could show a sandbox was a browser. It could not answer the question an
operator asks next: **how many of these fit, what does one cost, and what breaks
first?** That is what this phase exists to answer, and it is the first phase in
this repository whose product is a *number* rather than a component.

## The question

Three claims needed evidence before anything could be claimed about capacity:

1. What does one session cost in CPU and memory? The cgroup gives a per-pod
   number, not a per-session one, and `working_set ≈ current − inactive_file` is
   an accounting trap ([`01`](../research/session-lifecycle-elasticity/01-instrumentation-cpu-memory.md)).
2. How many sessions fit before something refuses? Measured at 60
   offered: **42 succeeded, 17 were refused by the scheduler**, queue wait p95
   **134 s**, node memory down to **20.9 %** ([`07`](../research/session-lifecycle-elasticity/07-overload-and-suspend.md)).
3. What does lifecycle policy change? Reuse resets a session in **35 ms**;
   destroy pays **374 ms plus a cold start** ([`09`](../research/session-lifecycle-elasticity/09-results-three-runs.md)).

## What shipped

| Piece | Where | Size |
|---|---|---|
| Session scheduler, experiment runner, report writer | `apps/session-lab/cmd/session-lab` + `internal/runtime/{schedule,session,report}` | 54 files / 12,044 lines, 22 Go test files |
| Infra adapters (kube, pool, sandbox, measure) | `apps/session-lab/internal/infra/` | part of the above |
| Store and metrics | `apps/session-lab/internal/platform/{store,metrics,model}` | part of the above |
| Pool autoscaler, deployed in-cluster | `apps/session-lab/cmd/pool-autoscaler`, chart `config/session-lab/` | 7 files / 191 lines |

The report contract is the point: every run emits the concurrency, the session
count, the pool size, the template shape it read from the API, and the outcome
enum per session. A claim without a run id is not a claim this phase accepts.

## What was measured, and what broke

- **Elasticity is real and bounded.** The pool scaled **2 → 22 → 2**, with
  **84.9 s** to scale out, and hit its **40-replica ceiling**; the next boundary
  was the API server, not the workload ([`10`](../research/session-lifecycle-elasticity/10-autoscaler-deployed.md)).
- **CPU requests must not be limits.** 248 of 360 sessions were CFS-throttled at
  the template's 1000m limit, which makes hold latency an artifact of the quota
  rather than a property of the workload ([`12`](../research/session-lifecycle-elasticity/12-cpu-requests-not-limits.md)).
- **Claims leak, and they cost capacity.** Sixteen leaked claims held the
  autoscaler at 22 replicas with no traffic, and `WaitReady`'s 100 ms poll is a
  per-session 10 req/s storm ([`14`](../research/session-lifecycle-elasticity/14-leaked-claims-and-polling-storm.md)).
- **The control plane is the ceiling.** kine commits to SQLite in **1–3 s** under
  write pressure, so 600 sessions is not a workload limit, it is a control-plane
  limit ([`18`](../research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md)).

## Why this layer exists

Because "it scales" and "it holds 600 sessions" are the two easiest claims to make
and the two hardest to support. The runner imposes no ceiling and no figure from a
document is an input to it: concurrency, session count, pool size and worker count
are whatever the operator asks for, and node capacity is read from the API at run
time. That rule is why the notes keep contradicting their own earlier drafts in
public — [`08.1`](../research/session-lifecycle-elasticity/08.1-requirement-status.md)
exists solely to separate implemented / deployed / configured / verified.

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `33c18ec` | 2026-10-29 | `feat(experiments-y6n): session-lab runner, adapter and scheduler` |
| `0924ee6` | 2026-10-29 | `feat(experiments-y6n): deploy the pool autoscaler in the cluster` |
| `3a4069e` | 2026-11-02 | `Derive boot concurrency from measured peaks (experiments-272)` |
| `0500212` | 2026-11-02 | `feat(experiments-y6n): cpu request 10m, pool ceiling 120, and the run path documented` |

45 commits touch `apps/session-lab`, 6 touch `config/session-lab`, all inside
2026-10-29 → 2026-11-02.

## What this phase leaves open

The CPU-density question it could not answer — how far requests can be pushed
below limits before the API server suffers — becomes
[phase 11](11-cpu-oversubscription.md). The slot model that made a session smaller
than a pod becomes [phase 9](09-contexts-slots-cdp.md). The autoscaler still does
not read CPU pressure; it sizes pools from claim counts
([`18`](../research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) §4).

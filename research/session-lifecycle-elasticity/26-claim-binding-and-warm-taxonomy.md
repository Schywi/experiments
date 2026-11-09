---
title: 'What a claim binds: an unready sandbox may be adopted, so "warm" in our reports measures pre-existence'
kind: audit
state: validated
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, sandbox, claims, warm-pool, audit, state-validated]
---

# Note 26 — What a claim binds, and what our "warm" number measures

| | |
|---|---|
| Status | Audit of the claim controller in the agent-sandbox tree, and of our own warm/cold accounting |
| Date | 2026-11-09 |
| Scope | Three outcomes a `SandboxClaim` can have, the latency each carries, and the label this repository puts on them |
| Related | [22 — queue and multiplexing audit](22-queue-multiplexing-audit-and-latency-breakdown.md) · [25 — capacity ramp](25-capacity-ramp-and-provisioning.md) · [27 — autoscaler signals](27-autoscaler-signals.md) |

Evidence base: `agent-sandbox` checkout `d816e0f`, controllers under `extensions/controllers/`, and
this repository's `apps/session-lab/internal/infra/kube`.

## 1. The claim path, as written

A claim does not create a pod. `Kube.Acquire` creates a `SandboxClaim` referencing a warm pool
(`internal/infra/kube/kube.go:138-162`) and returns; the controller binds a sandbox to it. What it
binds depends on what is in the pool's in-memory queue:

| Outcome | When it happens | Latency the caller sees | What we label it |
|---|---|---|---|
| **Ready sandbox adopted** | a pooled sandbox is Ready and unclaimed | the claim binds, the browser answers | `warm` — correct |
| **Booting sandbox adopted** | no Ready candidate exists, but an unready one is in the queue | the claim binds, the caller waits out the remainder of a ~40 s boot | `warm` — **wrong** |
| **Built from template** | the queue is empty for that pool | template apply, scheduling, full cold boot | `cold` — correct |

The middle row is a deliberate design choice in the controller, and it is the one worth
understanding.

## 2. Three places that produce it

1. **Adoptability ignores readiness.** `isAdoptable`
   (`extensions/controllers/sandboxclaim_controller.go:2867-2890`) checks the deletion timestamp, the
   warm-pool label, the template-hash label and the owner reference. There is no readiness test.
2. **The queue is filled at sandbox creation.** The `Sandbox` watch adds a key on the transition
   `!oldAdoptable && newAdoptable` (`.go:2829-2839`), which fires when the object is created, and
   again when its node changes. A booting sandbox is therefore a queue candidate for its whole boot.
3. **The claim prefers Ready and then settles.** In the candidate loop the controller keeps the first
   unready sandbox it sees — `// Sandbox is valid but NOT Ready. Keep the first unready sandbox we
   found as fallback` (`.go:1198-1206`) — and when no Ready candidate remains it adopts that one:
   `adoptingFallback = true; return fallbackSandbox` (`.go:1116-1119`).

For an interactive allocator this is defensible: binding early gives the caller a reservation and a
deadline instead of an unbounded queue position. For a latency measurement it is corrosive, because
the reservation looks like a fast path from every angle except the one that matters.

## 3. The label this repository puts on it

`kube.go:206`:

```go
instance.Warm = sandbox.GetCreationTimestamp().UnixNano() < instance.CreatedNS
```

Warm means *the sandbox object existed before my claim* — not *I avoided a boot*. Combined with §1's
middle row, a session can be recorded `warm` and still wait the full 40 s. Every warm/cold split this
repository has published, including note 22's, is therefore a split by pre-existence, and a "warm
p50" that reads well may be a boot in disguise.

`infra/sandbox` adds a second, per-run counter (`sandbox.go:252`: `warm := f.WarmInstances == 0 ||
f.acquired <= f.WarmInstances`), which inherits the same definition.

## 4. What to change, in order of value

| Change | Why | Cost |
|---|---|---|
| Record the sandbox's readiness **at claim time**, and define `warm` as "the sandbox was Ready when bound" | makes the header honest and the metric usable | one field on `model.Session`, one comparison at `kube.go:206` |
| Keep the current flag as `pre_existing` beside the new one | the old numbers stop being ambiguous without being rewritten | one label |
| Report the **fraction of claims that waited > 5 s after binding** | this is the size of the middle row, per run | derivable from the new field |
| Feed the same fact into the wait split | the boot a claim inherits appears today as the "boot permit" gap in note 01's three-way split | no new instrumentation |

None of these change behaviour; they change what the run says about itself.

## 5. What this does not explain

The controller's fallback cannot create capacity. When the queue is empty the claim takes the
`SandboxCreatePending` path (`.go:832`) and pays a full build. Which of the three outcomes dominates
a given run is a function of how far ahead of demand the pool is, which is note 25's subject and
note 27's control problem.

## 6. Sources

| Claim | Source |
|---|---|
| claim creation only, no pod | `apps/session-lab/internal/infra/kube/kube.go:138-162` [I] |
| adoptability has no readiness test | agent-sandbox `extensions/controllers/sandboxclaim_controller.go:2867-2890` [I] |
| queue filled at creation | same file, `:2829-2839` [I] |
| unready fallback adopted | same file, `:1198-1206` and `:1116-1119` [I] |
| empty queue builds from template | same file, `:832` [I] |
| `warm` means pre-existence | `apps/session-lab/internal/infra/kube/kube.go:206` [I] |
| ~40 s cold boot | note 22 §2.2 [M] |

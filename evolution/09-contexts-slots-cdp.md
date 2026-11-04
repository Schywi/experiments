---
title: 'Phase 9 — Browser contexts, slots, and a CDP client in Go (2026-10-30)'
date: 2026-11-04
kind: narrative
phase: 9
tags: [session-lifecycle, slots, cdp]
---

# Phase 9 — Browser contexts, slots, and a CDP client in Go (2026-10-30)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../apps/session-lab/internal/runtime/slots/`](../apps/session-lab/README.md),
[`../apps/session-lab/internal/runtime/browser/`](../apps/session-lab/README.md),
[`../apps/cdp-forward/`](../apps/cdp-forward/),
[`../apps/browser-sandbox/`](../apps/browser-sandbox/README.md) · **design notes:**
[`16.1`](../research/session-lifecycle-elasticity/16.1-browser-contexts-and-slots.md),
[`17`](../research/session-lifecycle-elasticity/17-cdp-client-in-go.md)

Phase 8 measured a session as a pod. This phase is the discovery that a pod is
the *wrong* unit for that measurement, and the machinery that replaces it.

## The question

One pod runs one Chromium, and one Chromium can carry many browser contexts. So
what is a session — the pod, the context, or the claim? The phase 8 notes were
written against the pod, which means their per-session CPU figures were really
per-pod figures, and the ceiling they reported was a pod ceiling.

Three things had to exist before the question could be answered:

1. A **slot** as a first-class identity: `slot = (instance, browserContextId)`,
   with an allocator that hands out up to eight per browser and refuses the ninth.
2. **Contexts created over CDP**, not by launching more browsers — one process,
   eight contexts, disposed at release.
3. A way to **reach a sandbox's DevTools socket from the runner**, since the pods
   publish nothing routable and `kubectl port-forward` per session does not scale
   to a ladder run.

## What shipped

| Piece | Where | What it does |
|---|---|---|
| Slot allocator | `apps/session-lab/internal/runtime/slots/` | Hands out one context per session, up to `--contexts-per-browser` per instance, retires a dead browser from every pool |
| CDP client | `apps/session-lab/internal/runtime/browser/` | `conn.go` + `session.go` + `browser_test.go`: sessions, events and per-context metrics the HTTP shim cannot provide |
| Forwarding sidecar | `apps/cdp-forward/` | Raw TCP bridge from the runner to a pod's DevTools port |
| Browser image | `apps/browser-sandbox/` | Chromium built to answer CDP over the bridge; `dfa017a` carried a flattened CDP session through it |

The two layers are the durable idea of this phase, and they are still the right
way to talk about capacity:

| Layer | Unit | Bounded by |
|---|---|---|
| `infra` | one pod per session | node CPU, node memory, admission |
| `session` | eight contexts per pod | the slot allocator, and the capture budget behind the bridge |

## The correction this phase forces on the notes

`17` was filed as **"Design, not implemented"** and `15` said its admission
classes were **not implemented**. The code exists in both cases:
`internal/runtime/browser/` carries a working CDP client with a transport switch
(`browser.go`, `conn.go`, `session.go` plus a live integration test), and
`internal/runtime/admission/` carries the classes, the queue and the gate.

Both status rows have since been corrected — `15` and `17` now say implemented,
and `17` marks which single work-order step is still outstanding (the ws transport
is not yet the default path). The lesson is worth keeping anyway: press-on notes
are written before the code, and a status row is not re-read when the code lands.
This is the second time it happened in this directory, so treat any claim of "not
implemented" in these notes as a claim about its own date, not about today.

## What this phase measured, and what it makes unknowable

- **The slot ceiling is 168 contexts**, not 600 sessions
  ([`18`](../research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) §2).
- **A browser is ~559 MiB**, so density is a memory question before it is a slot
  question ([`13`](../research/session-lifecycle-elasticity/13-client-go-apf-and-density-limits.md)).
- **The cgroup cannot see inside a slot.** Per-slot CPU is the pod's number
  divided by the contexts in use, which [`16.2`](../research/session-lifecycle-elasticity/16.2-session-wall-ui.md) §5 draws as `shared` rather than pretending
  it is a measurement.

That last point is the price of the smaller unit: the accounting unit got finer
and the instrument did not follow it.

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `a35b4e2` | 2026-10-30 | `feat(experiments-y6n): browser contexts and a slot allocator` |
| `d117244` | 2026-10-30 | `feat(experiments-y6n): a CDP client, and the ws transport behind a flag` |
| `25b30c0` | 2026-10-30 | `feat(experiments-y6n): a Go forwarding sidecar beside the browser` |
| `dfa017a` | 2026-10-30 | `feat(experiments-y6n): carry a flattened CDP session through the bridge` |

All four landed on one day, which is why this phase reads as a single move rather
than a sequence.

## What this phase leaves open

Per-slot CPU needs an instrument the cgroup does not provide — the CDP
`Performance` domain is the candidate, and it is unmeasured. The transport switch
is behind a flag, so the WebSocket dependency `17` §8 worried about is still
optional and still untested in a ladder run.

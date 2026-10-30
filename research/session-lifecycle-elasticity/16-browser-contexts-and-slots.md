# Note 16 — Browser contexts, slots, and the two layers to load-test

| | |
|---|---|
| Status | Implemented, unit-tested, not yet run against the cluster |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Code | `internal/runtime/slots/`, `internal/runtime/session/shared.go`, `internal/infra/kube/kube.go` |
| Follows | [15 — capacity plane and demand plane](15-capacity-plane-and-demand-plane.md) |

---

## 1. Two layers, two questions

The 10-minute baseline used to mean one thing. It now means two, and they must not be compared:

| Layer | One pod carries | The run measures |
|---|---|---|
| `--layer infra` | 1 session | claims, sandboxes, pod scheduling — the capacity plane |
| `--layer session` | up to 8 sessions | browser contexts, the session mechanic, and CPU per session inside a shared browser |

`--layer infra` is the pre-context behaviour: `--contexts-per-browser 1`, one claim per session.
`--layer session` passes `--contexts-per-browser 8` (override with `--contexts-per-browser N` or
`SESSION_LAB_CONTEXTS`). A 600-session run at 8 contexts asks for roughly 75 claims instead of 600.

## 2. What a session is now

```
session ──1:1── browser context ──N:1── Chromium (pod) ──1:1── claim
```

`Target.createBrowserContext` → `Target.createTarget{url, browserContextId}` → session runs →
`Target.closeTarget` + `Target.disposeBrowserContext`. All four go through the in-pod bridge
(`POST /cdp`), the same transport the console uses, because Chromium keeps DevTools on loopback.

What a context separates: cookies, localStorage, IndexedDB, cache, origin storage. What it shares:
the Chromium process, the cgroup, the main thread. **A browser crash takes every session in it** —
that is the price of density, and it is why the per-session records still carry the instance id.

Closing the context replaces the old `Page.navigate about:blank`. Measured reset cost stays in the
same range (context create + dispose, tens of milliseconds), but the state guarantee is real: a new
context has no residue, while `about:blank` in a shared browser never did.

## 3. The slot allocator

`internal/runtime/slots` owns the arithmetic:

- A slot is `(instance, index)`. Capacity per instance is `--contexts-per-browser`.
- `Take` returns a free slot; if every known instance is full it calls the provider, which acquires
  and readies a claim. That provider is the **only** path from the demand plane into the capacity
  plane, so claims are created for slot deficits, never for sessions.
- Only one creation runs at a time; sessions that arrive during it wait and then re-check, because
  the new instance's free slots are usually enough. Without this, sixteen simultaneous sessions
  created sixteen instances instead of two.
- `Give` returns a slot. A freed slot is reused before any capacity is requested, and instances are
  drained by preference (an instance with more sessions in use is filled first).
- An idle instance is **not** deleted under `reuse`: it is exactly what the next session needs.
  Under `destroy` the instance is retired and deleted when its session ends.

## 4. What per-session CPU means now

The cgroup counter belongs to the pod. With eight sessions in one Chromium, it belongs to eight
sessions. The runner divides the sample by the number of contexts in flight on that instance when
the session started, and marks the row `cpu_shared=true`. The report prints:

```
browsers: 8 sessions per Chromium; 3 of 4 sessions shared a browser, so their CPU is the pod's
usage divided by the contexts in flight
```

This is an average, and the label says so. A per-context number would need CDP
`Performance.getMetrics` per target, which measures main-thread task time — a different quantity
from `usage_usec`, so mixing them in one column would be wrong. Left out on purpose (note 15 §8).

## 5. Flags

| Flag | Meaning |
|---|---|
| `--contexts-per-browser N` | sessions one Chromium serves; `1` is the historical shape |
| `--target-url URL` | page each session opens inside its context (default `about:blank`) |
| `--class normal\|batch\|bulk` | admission class; bulk never grows the pool |
| `--admit N` | concurrent admissions; `0` is unbounded |

`--policy suspend` is refused with `--contexts-per-browser > 1`: parking a pod is not a property a
browser context has.

## 6. Tested

`go test ./...` — 11 packages green. New: a browser holds eight sessions in one instance; the ninth
creates a second; a freed slot is reused before capacity is requested; a refused capacity request is
reported rather than swallowed; retire drops state; `InUseOn` is the divider; and two runner tests —
sixteen overlapping sessions fit in at most three instances, each in its own context, and shared
rows are marked.

## 7. Still open

- No live run yet. The first `--layer session` run is what proves the CDP path against a real
  Chromium; the bridge is the only untested hop.
- Per-context CPU (CDP) and per-context memory (renderer RSS) are not collected.
- Preemption of bulk sessions to make room for normal ones.
- `apps/session-lab/README.md` still describes the runner without the two layers; the script's usage
  block does.

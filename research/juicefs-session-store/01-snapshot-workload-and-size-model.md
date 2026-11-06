---
title: 'The snapshot workload: what is captured, how big, how often'
kind: analysis
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
tags: [session-lifecycle, storage, sizing, state-hypothesis]
---

# 01 — The snapshot workload: what is captured, how big, how often

| | |
|---|---|
| Status | Model, no measurements |
| Date | 2026-11-05 |
| Depends on | [16.1 — slots](../session-lifecycle-elasticity/16.1-browser-contexts-and-slots.md), [18 — capacity](../session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) |
| Feeds | [03 — the experiment plan](03-isolated-experiment-plan.md) |
| Labels | slot counts, unit costs and node figures are **[M]** from the notes cited above; every rate and volume in §4 and §5 is **[I]**, arithmetic over them; the three size profiles in §2 are **[U]** until E0 |

---

## 1. The unit that gets captured

```
browser (pod, one Chromium, one cgroup)
  └── context (slot)  ← the capturable unit; an in-memory partition, no directory on disk
        └── session   ← one lease; the thing an operator thinks is moving
```

Because contexts are in-memory partitions, capture goes through the DevTools protocol against the
shared Chromium. One session's snapshot is the union of what these verbs return for that
context's targets:

| State | CDP surface | Notes |
|---|---|---|
| cookies | `Storage.getCookies` | includes HttpOnly; per browser, filtered by context |
| `localStorage` | `DOMStorage.enable` + `DOMStorage.getDOMStorageItems` per origin | one round trip per origin, one per item page |
| IndexedDB | `Storage.indexedDB.requestDatabaseNames` → `requestDatabase` → `requestData` | paginated per object store; the expensive one |
| `sessionStorage` | `DOMStorage` (session-storage namespace) | not part of Playwright's `storageState`; capturing it adds fidelity that survives a reload only if the page is restored, not navigated |

`internal/runtime/browser` already exposes `Conn.Do` / `Session.Do` as a generic CDP call, so no new
transport is needed; what is missing is the walk and the serialisation.

Two exclusions that matter for the size model:

- **The browser profile directory is not the unit.** With eight contexts in one Chromium there is
  no per-slot profile to copy, and copying a live profile of a running browser is unsafe anyway.
- **Memory is not checkpointed.** A session's cgroup pages belong to the pod; freeing them is what
  `--policy suspend` and the swap study already address, and they are a different question from
  moving a session's identity.

## 2. Size model

Three profiles, with the reasoning that produces the number. All three are **[U]** until E0 runs;
the lab generates its corpus from these so a wrong guess moves the x-axis, not the conclusion.

| Profile | Composition | JSON size | gzip | Where it comes from |
|---|---|---|---|---|
| **L** identity | 10–40 cookies (0.2–1 KB each), `localStorage` under 100 KB across 1–3 origins | 32–128 KiB | 8–30 KiB | a login plus a few feature flags; the common case for a scraping or automation slot |
| **M** app state | cookies, `localStorage` 0.5–1 MiB across 3–5 origins, IndexedDB under 1 MiB | 1–3 MiB | 0.3–1 MiB | a SPA that caches its API responses in IndexedDB |
| **H** data-bearing | IndexedDB 10–100 MiB, `localStorage` at the per-origin quota | 10–100 MiB | 2–30 MiB | an offline-capable app; the profile that decides whether object storage is the design driver |

The browser quotas bound the top end: `localStorage` is 5 MiB per origin, IndexedDB is bounded by
disk. The bottom end is bounded by cookies alone.

**The model's working numbers** are L=32 KiB, M=256 KiB, H=2 MiB, so that the arithmetic brackets
the plausible centre rather than the extremes; E0 replaces them with measured percentiles.

## 3. Rate model

Three independent traffic sources, and they have different shapes:

| Source | Rate | Shape |
|---|---|---|
| **periodic checkpoint** | `N/T` snapshots/s | steady, all slots in step unless jittered |
| **hand-off** | `m` moves/s | bursty; 1 read + 1 write per move, on two different workers |
| **recovery** | one per lost node | a single burst of `slots-on-node × S` |

`N` is 168 at the measured ceiling and 336 as the doubled case. `T` is the checkpoint interval.
`m` is a control-plane policy, not a workload property: a rebalancer that moves one session per
second is already aggressive.

## 4. The tables

### 4.1 Write throughput and daily volume

One checkpoint per slot per `T`, no compression, all slots in step. GB are decimal (10⁹ bytes).

| `T` | snapshots/s (168 / 336) | MiB/s at S=256 KiB (168 / 336) | GB/day at S=256 KiB (168 / 336) | GB/day at S=2 MiB (168 / 336) | PUTs/day (168 / 336) |
|---|---|---|---|---|---|
| 600 s | 0.28 / 0.56 | 0.07 / 0.14 | 6.3 / 12.7 | 51 / 101 | 24 k / 48 k |
| 300 s | 0.56 / 1.12 | 0.14 / 0.28 | 12.7 / 25.4 | 101 / 203 | 48 k / 97 k |
| 120 s | 1.40 / 2.80 | 0.35 / 0.70 | 31.7 / 63.4 | 254 / 507 | 121 k / 242 k |
| 60 s | 2.80 / 5.60 | 0.70 / 1.40 | 63.4 / 126.8 | 507 / 1 015 | 242 k / 484 k |
| 15 s | 11.2 / 22.4 | 2.80 / 5.60 | 253.7 / 507.3 | 2 029 / 4 059 | 968 k / 1.94 M |

Read `S=32 KiB` by dividing the 256 KiB columns by eight.

The 60 s row is the base case: **63 GB/day and 242 k objects/day** at 168 slots with a 256 KiB
snapshot. That is 0.70 MiB/s, or 5.9 Mbit/s — inside any network the platform will run on, and
inside the object store's rate limit by two orders of magnitude. The 15 s row is where it stops
being free: 2.8 MiB/s and 254 GB/day for a cadence that costs 1.12 of the node's 2.7 free cores.

### 4.2 Hot set and retention

One current snapshot per slot, then ×`G` generations retained:

| S | 168 slots, 1 generation | 336 slots, 1 generation | 168 slots × 10 | 336 slots × 10 |
|---|---|---|---|---|
| 32 KiB | 5.3 MiB | 10.5 MiB | 53 MiB | 105 MiB |
| 256 KiB | 42 MiB | 84 MiB | 420 MiB | 840 MiB |
| 2 MiB | 336 MiB | 672 MiB | 3.3 GiB | 6.6 GiB |
| 8 MiB | 1.3 GiB | 2.7 GiB | 13 GiB | 27 GiB |

At 168 slots and 256 KiB the entire hot set is 42 MiB — 0.3 % of the node's memory and less than a
tenth of a second of `dd`. Any cache sized `--cache-size=102400` (the default, 100 GiB) holds it
without eviction, so **eviction is not part of this workload's steady state**; it only appears in
the deliberately-thrashed arm of the experiment.

### 4.3 Egress

Reads happen for three reasons, and their volumes differ by orders of magnitude:

| Read source | Volume | Note |
|---|---|---|
| restore on another worker | `m × S` per second | at m=1/s, S=256 KiB: 0.25 MiB/s, 22 GB/day |
| recovery of a lost node | `slots × S`, once | 42 MiB (168 × 256 KiB) to 336 MiB (168 × 2 MiB) |
| cache miss on a checkpoint read-back | ≤ steady-state write volume | only when the cache is smaller than the hot set |

So egress is a function of **how many sessions move**, not of how often they checkpoint. A design
that checkpoints every minute and never migrates moves 63 GB/day out to the object store and
almost nothing back. That asymmetry is what makes write-back staging attractive on paper and what
E4 tests, because write-back delays exactly the bytes a hand-off needs.

## 5. Capture cost, which is the real coupling to capacity

The node's ceiling is a CPU-request ceiling: 21 browsers × 8 contexts = 168 slots, about 2.7 cores
free at that point (note 18 §2). Reading state out of eight sibling contexts spends some of it, in
the browser and in the runner.

`cores = N × c / T / 1000`, with `c` the per-slot capture cost in milliseconds:

| `c` per slot | T=300 s | T=60 s | T=15 s |
|---|---|---|---|
| 20 ms (cookies + small `localStorage`) | 0.011 | 0.056 | 0.224 |
| 100 ms (a few origins, small IndexedDB) | 0.056 | **0.28** | 1.12 |
| 300 ms (IndexedDB with many records) | 0.168 | 0.84 | 3.36 |

At 336 slots, double. Against 2.7 free cores: the 60 s / 100 ms cell is 10 % of the margin, the
15 s / 300 ms cell is 124 % of it, which means the node stops serving 336 slots and starts serving
fewer. A checkpoint strategy that is free at 168 slots with light state can cost a third of the
capacity at 336 with heavy state. This is why the ladder in note 03 varies cadence at **fixed
session count** and reports `sessions/s` next to `checkpoint_ms`, and why a storage benchmark run
outside the browser cannot settle the cadence question.

## 6. Requirements the storage layer has to meet

These are the acceptance criteria the experiment measures. They come from the four-level model in
note 18, where a slot holds one session at a time and workers change.

| # | Requirement | Why | Failure mode if violated |
|---|---|---|---|
| R1 | one key per (slot, generation), monotonically versioned | a restore must pick a committed generation, never a mix | a session resumes half of checkpoint *n* and half of *n−1* |
| R2 | atomic commit of a snapshot | snapshots are rewritten every interval | a reader sees a truncated JSON and fails to parse |
| R3 | read-after-write across workers | the whole point of the layer | destination context starts empty, or the read errors |
| R4 | one writer per key at a time | two workers must never both own a slot | last-writer-wins corruption with no error anywhere |
| R5 | deletion that cannot race a live reader | superseded generations are reaped | a restore fails on a key the reaper just removed |
| R6 | detectable corruption | object storage can return a short object | a session starts with silently missing cookies |
| R7 | retention policy with a bound | 63 GB/day at base, 3.9 TB/day at the heavy corner | the volume fills and the cluster stops |

R4 is not a storage property. JuiceFS, S3 and a local directory all let two writers write the same
path; ownership has to be fenced by whatever records which worker owns the slot — the run's SQLite
store today, a Kubernetes `Lease` under multi-node. A consistent filesystem makes R1–R3 cheap and
leaves R4 exactly as hard as it was.

## 7. What this model leaves out, on purpose

- **Snapshot compression as a designed step.** `gzip` on JSON of cookies and `localStorage` should
  land near 4:1; measuring it is E0's second output, and the model above carries the uncompressed
  numbers so the result can only improve.
- **Whole-pod checkpointing.** CRIU or pod-level suspend captures the process, not the identity,
  and the swap/Suspend work already covers the parts of it that matter here.
- **Serve-path reads.** A restored session's page load fetches from the internet, not from this
  layer; the layer only holds the state that precedes it.
- **The control-plane writes a snapshot implies.** If the runner records each checkpoint as a
  Kubernetes object, it re-enters the kine ceiling from note 18 at 5 writes/session. Snapshots
  belong in the storage layer, with one row per session in the run's own SQLite, not in the API.

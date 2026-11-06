---
title: 'JuiceFS as the session snapshot store'
kind: index
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
tags: [session-lifecycle, storage, juicefs, state-hypothesis]
---

# JuiceFS as the session snapshot store

| | |
|---|---|
| Status | Design notes. Nothing here has been measured yet |
| Date | 2026-11-05 |
| Question | Can a slot's browser state be checkpointed into one persistent layer and restored on another worker, at 168 slots, and at 336? |
| Harness | `apps/session-lab` — it has no snapshot path today |
| Notes | [01 — the workload and its size model](01-snapshot-workload-and-size-model.md) · [02 — JuiceFS mechanics and the verdict it supports](02-juicefs-mechanics-and-verdict.md) · [03 — the isolated experiment plan](03-isolated-experiment-plan.md) · [04 — the architecture, as it would be built](04-architecture-and-metrics.md) |

Labels used throughout, the same four as `research/swap-capacity/`: **[M]** measured in this lab
with a named artefact, **[D]** documented upstream, **[I]** inferred from a measurement or from
arithmetic over one, **[U]** unknown — no evidence produced yet.

Every number in these notes is either arithmetic over a figure already recorded elsewhere in this
repository (named at the point of use) or a vendor statement about JuiceFS. The snapshot sizes are
**[U]** until E0 in note 03 runs, and the whole plan is built so that a wrong size assumption
changes a constant, not a conclusion.

---

## 1. What "session snapshot" means here, precisely

A session leases one browser context inside a shared Chromium (`internal/runtime/slots`, note
16.1). A browser context is an in-memory partition of one process: there is no per-slot directory
on disk to copy, so the state has to be read out through CDP, per slot, from the same Chromium
that serves seven other sessions.

The state that can be captured that way is the Playwright `storageState` shape — cookies,
`localStorage` per origin, and — in recent Playwright versions, or directly through CDP —
IndexedDB databases. What it leaves behind: `sessionStorage`, the HTTP cache, service workers and
Cache Storage, in-flight JavaScript, the DOM, the tab's history.

So a restored session reloads its page and re-authenticates. The capability the storage layer has
to support is **resume an identity**, at page-load cost, and the layer only ever holds kilobytes
or a few megabytes per slot. Whether that is sufficient for a product's notion of migration is a
question that comes before the storage question, and no JuiceFS result can answer it.

## 2. Three findings that decide how the experiment is built

**The working set is small enough to live in memory.** 168 slots × 256 KiB = 42 MiB; 336 slots at
a heavy 2 MiB per snapshot = 672 MiB. The node has 15.5 GiB. A cache that holds every slot's
current snapshot is not a tuning exercise, it is two lines of mount option. The storage layer is
then paying for durability, namespace and commit semantics; its throughput ceiling is not what
this workload will hit first. **[I]**, settled by E0 and E2.

**Capture cost competes with the resource that caps the node.** The 168-slot ceiling is a CPU
request ceiling: 21 browsers × 100m, with roughly 2.7 cores free at that point (note 18, from the
600-session run `20261031T024806Z`). Reading cookies and `localStorage` out of eight sibling
contexts costs browser CPU and runner CPU. At 100 ms per slot per minute, 168 slots consume
**0.28 of those 2.7 cores (10 %)**; on a 15 s cadence, **1.12 cores (41 %)**. Checkpoint frequency
is a capacity variable, and a storage benchmark that ignores the capture cost measures the cheap
half. **[I]**, settled by E1 and E7.

**JuiceFS writeback moves the commit point off the object store.** With `--writeback`, close or
fsync returns once the data is in the local staging directory (`/var/jfsCache/<UUID>/rawstaging/`)
and the upload continues asynchronously; other clients reading that file before the upload
completes get an I/O error or a timeout. **[D]** (`juicefs mount --writeback`,
`docs/community/guide/cache#client-write-cache`, `docs/community/command_reference#mount`). A
hand-off is exactly that read. So the snapshot subtree runs write-through, or the layer grows an
upload barrier JuiceFS does not currently expose per file.

## 3. The questions, and what settles each

| Question | Current answer | Settled by |
|---|---|---|
| Does the snapshot rate strain any layer? | **No** below ~5 snapshots/s; the hot set is tens of MiB | E0, E2 |
| Is local NVMe staging necessary? | As the JuiceFS cache directory, **yes**; as a second hand-rolled layer, **no** | E3, E6 |
| What checkpoint cadence? | Event-driven plus a 5-minute floor, if E1 holds | E1, E7 |
| Writeback or write-through for the snapshot subtree? | **Write-through** — writeback breaks hand-off | E4 |
| Which metadata engine? | Redis or Postgres; never SQLite on the disk that carries kine | E5 |
| JuiceFS, or objects plus a cache? | Unmeasured; the object-store arm is in the plan as the control | E1, E3 |
| Does cross-worker restore hold under node loss? | Unmeasured | E4, E5 |

## 4. The arithmetic, in one table

Per-slot snapshot `S`, `N` slots, one checkpoint every `T` seconds, all slots in step.

| Quantity | 168 slots, S=256 KiB | 336 slots, S=256 KiB | 336 slots, S=2 MiB |
|---|---|---|---|
| snapshots/s at T=60 s | 2.8 | 5.6 | 5.6 |
| write MiB/s at T=60 s | 0.70 | 1.40 | 11.2 (90 Mbit/s) |
| written per day at T=60 s | 63 GB | 127 GB | 1.0 TB |
| objects (PUTs) per day | 242 k | 484 k | 484 k |
| hot set, one generation per slot | 42 MiB | 84 MiB | 672 MiB |
| capture CPU at 100 ms, T=60 s | 0.28 cores | 0.56 cores | 0.56 cores |
| full-node restore burst | 42 MiB | 84 MiB | 672 MiB |

The full derivation, including the 15 s and 300 s rows, is in
[01](01-snapshot-workload-and-size-model.md) §4. The four numbers to carry into any discussion:
**63 GB/day**, **242 k objects/day**, **42 MiB hot set**, **0.28 cores**.

The cadence decision falls out of the last two rows rather than out of a storage benchmark: a
5-minute floor writes 13 GB/day and costs 0.06 cores, and a 60 s cadence writes 63 GB/day and
costs 0.28. Both fit. A 15 s cadence writes 254 GB/day and costs 1.12 of the 2.7 free cores, which
is a capacity decision disguised as a storage decision.

## 5. Where this could be wrong

- **The sizes.** No storageState has been captured on this platform yet. If a real page carries
  tens of MiB of IndexedDB, the write volume at a 60 s cadence lands near 5–10 TB/day and the
  object store becomes the design driver. E0 exists to find out before anything else is built.
- **The capture cost.** 100 ms is a guess from the number of CDP round trips, not a measurement.
  IndexedDB traversal is the risk: `Storage.indexedDB.requestData` is per object store with a
  page size, so a database with many records is many round trips.
- **The tenancy assumption.** Restores assume one writer per snapshot key at a time. JuiceFS has
  no lease or fencing primitive, so the ownership change has to be carried by whatever records
  worker ownership (the run's SQLite store, or a Kubernetes Lease). A storage layer that is
  consistent does not make a hand-off safe.

## 6. Provenance

JuiceFS statements were read on 2026-11-05 from the Community Edition docs: `guide/cache`,
`architecture`, `command_reference`, `fuse_mount_options`, `guide/block-device`,
`production_deployment_recommendations`, and `csi/guide/pv`. Version-specific defaults are quoted
from `command_reference` as it stood that day; a client upgrade can move them, so an experiment
must record the client version it ran.

Platform numbers are quoted from notes already in this repository: the 168-slot ceiling, the 6-core
node, and the 2.7 free cores come from
[note 18](../session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) and
[the CPU handoff](../session-lifecycle-elasticity/HANDOFF-cpu-oversubscription.md).

## 7. How to check these notes

```bash
# the slot model this note assumes
rg -n "contexts-per-browser|Slots per pod" apps/session-lab/README.md \
  research/session-lifecycle-elasticity/16.1-browser-contexts-and-slots.md

# the CDP surface a snapshot needs (a generic Do, no new transport)
rg -n "func \(c \*Conn\) Do|func \(s \*Session\) Do" apps/session-lab/internal/runtime/browser/

# the JuiceFS statements
#   https://juicefs.com/docs/community/guide/cache#client-write-cache
#   https://juicefs.com/docs/community/command_reference#mount
#   https://juicefs.com/docs/community/architecture
```

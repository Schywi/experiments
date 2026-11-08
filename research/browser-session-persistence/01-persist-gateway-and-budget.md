---
title: 'The persist gateway: one flag, one blob store, one ledger'
kind: design
state: hypothesis
date: 2026-11-06
branch: docs-site
tags: [session-lifecycle, persistence, storage, design, state-hypothesis]
---

# 01 — The persist gateway: one flag, one blob store, one ledger

| | |
|---|---|
| Status | Design agreed in discussion, nothing built |
| Date | 2026-11-06 |
| Fits | `apps/session-lab/`, `config/session-lab/`, `scripts/` |
| Capacity | 350 slots live, stated by the operator |
| Labels | `[O]` operator-stated, `[I]` arithmetic over an `[O]` figure, `[U]` unmeasured |

## 1. Decisions

| # | Decision | Value |
|---|---|---|
| D1 | Live-set capacity | 350 slots `[O]` |
| D2 | Whether a session persists | one boolean on the session, `is_persistent`, default `false` `[O]` |
| D3 | What is persisted | one opaque blob per context per generation; the gateway does not parse it `[O]` |
| D4 | Durable home | MinIO, one bucket, **10 GB hard budget** for the experiment `[O]` |
| D5 | Metadata, capacity ledger, eviction queue | one SQLite file on the node `[O]` |
| D6 | Write order | reserve bytes in SQLite, `PUT` to MinIO, mark committed `[O]` |
| D7 | Eviction | least-recently-used, run before rejecting a write `[O]` |
| D8 | Distributed machinery | JuiceFS, Litestream and Postgres are out of this design `[O]` |

The cluster is one node. SQLite is the metadata authority and there is no second writer to design
around; a single gateway process owns the write path, so the whole concurrency story is one mutex,
one SQLite writer and one `BEGIN IMMEDIATE`.

## 2. Topology

```
                         +----------------------------------------------+
                         |            one node (bare metal)             |
                         |                                              |
  session request ------>|  runner / admission                          |
   is_persistent: bool   |    . sets is_persistent at admission         |
   (default false)       |      false ------------------+               |
                         +--------------+---------------+---------------+
                                        | true                          |
                                        v                               |
   +----------------------------------------------------------------+   |
   |                      persistGateway                            |   |
   |                     (single process)                           |   |
   |                                                                |   |
   |  capture --> compress --> reserve --> PUT --> commit           |   |
   |  (opaque     (gzip)      (bytes in    to      (state=          |   |
   |   blob from              SQLite,      MinIO    committed,      |   |
   |   the context)           state=                actual bytes)   |   |
   |                          reserved)                             |   |
   |  restore --> GET --> decompress --> hand to a fresh context    |   |
   |  evict   --> LRU by last_access --> DELETE object --> row      |   |
   +-------+----------------------------------------+---------------+   |
           |                                        |                   |
           v                                        v                   |
   +-----------------------+            +---------------------------+   |
   |  MinIO                |            |  persist.db (SQLite)      |   |
   |  bucket: sessions     |            |  sessions                 |   |
   |  budget: 10 GB        |            |   workspace, session_id,  |   |
   |  one object per       |            |   is_persistent, gen,     |   |
   |  (session, generation)|            |   state, bytes_reserved,  |   |
   |                       |            |   bytes_actual, sha256,   |   |
   |  s3://sessions/       |            |   created_ns, last_access |   |
   |    ws/sess/g-3.gz     |            |  counters (one row)       |   |
   |    ws/sess/g-2.gz     |            |   retained_bytes,         |   |
   |    ws/sess/g-1.gz     |            |   persisted_bytes,        |   |
   +-----------------------+            |   evicted_bytes, ingress, |   |
                                        |   egress                  |   |
                                        +---------------------------+   |
                                                        |               |
   is_persistent: false ------------------------------- | ---> nothing  |
   session ends, context disposed, no blob, no row      |      written  |
                                                        |               |
   live restore cache: /var/lib/persist/cache/<sha256>  +---------------+
     TTL 1 h, LRU, rebuilt from MinIO on miss
```

## 3. Write path

```
1  capture blob from the context                     5-50 ms   [U]
2  gzip                                              1-5 ms    [U]
3  BEGIN IMMEDIATE; check retained+reserved+len<=budget
   INSERT state='reserved'                                      <- capacity decision
4  PUT s3://sessions/<ws>/<sess>/g-<n>.gz                       <- the slow, fallible step
5  UPDATE state='committed', bytes_actual=<n>                   <- commit point
6  if retained_bytes over budget: evict LRU until under
```

Two crashes matter and both are recoverable:

- Between 3 and 4 a `reserved` row exists with no object. The sweep reclaims reservations older
  than 60 s and returns the bytes to the ledger.
- Between 4 and 5 an object exists with no committed row. The sweep deletes objects with no
  `committed` row.

The commit point is step 5. A reader asks SQLite which generation is committed and then fetches
that key; a blob without a row is never read.

## 4. The flag

`is_persistent` is set at admission, not at capture time, so the gateway has two paths and no
third. Go field `IsPersistent bool` (`json:"is_persistent"`), column
`is_persistent INTEGER NOT NULL DEFAULT 0`. "Ephemeral" is `!is_persistent` and gets no column of
its own; two booleans produce rows where both are true.

A session with `is_persistent = false` takes the existing path unchanged: context created, work
performed, `Target.disposeBrowserContext`, nothing written. Forced-false runs are the control arm
that prices the feature.

Revision policy for the first experiment: one blob, written at session close, one generation per
session. `persist_mode` (`on_close` / `checkpoint`) is a later column, and the eviction queue does
not need it.

## 5. The ledger

| Rule | Why |
|---|---|
| Reserve inside `BEGIN IMMEDIATE` before the `PUT` | 350 concurrent captures each seeing 9.9 GB used is how the budget is exceeded |
| Count `reserved + committed` against the budget | an in-flight write occupies space it has not uploaded yet |
| Evict, then accept; refuse only when one blob exceeds the budget | the budget is a bound on retained bytes, not a reject-all valve |
| Pin live sessions and in-flight restores | a session whose context is alive, or whose blob is being restored, is not evictable |
| Sweep reservations and orphans on a timer | the two crash windows in §3 |

Eviction order is `state='committed' AND NOT pinned ORDER BY last_access_ns ASC`.

## 6. Workload arithmetic

300,000 sessions a day at a 20 % persistence rate is **60,000 persisted sessions a day, 0.69/s**
`[I]`. Against 350 slots that is an average session of about 100 s at full utilisation `[I]`.

`bytes/day = 60,000 x blob x revisions` and `at rest = 60,000 x blob x generations x retention_days`.

| Blob, gzipped | 1 revision/day | 6 revisions/day | At rest, 3 gens x 7 d | At rest, 3 gens x 30 d | 350-slot live set |
|---|---|---|---|---|---|
| 25 KiB | 1.4 GiB/d | 8.6 GiB/d | 30 GiB | 129 GiB | 8.5 MiB |
| 250 KiB | 14 GiB/d | 86 GiB/d | 300 GiB | 1.3 TiB | 85 MiB |
| 1 MiB | 59 GiB/d | 352 GiB/d | 1.2 TiB | 5.2 TiB | 350 MiB |
| 5 MiB | 293 GiB/d | 1.7 TiB/d | 6.0 TiB | 25.8 TiB | 1.75 GiB |

Retention at a 10 GB budget, with one generation per session: 40,960 sessions at 250 KiB, 10,240 at
1 MiB, 2,048 at 5 MiB `[I]`. Eviction is the retention policy; no TTL rule is needed for the
experiment.

The 5 MiB row is the one to avoid: it is what the blob becomes when the HTTP cache and Cache
Storage are captured along with cookies, `localStorage` and IndexedDB.

## 7. Metrics

| Metric | Meaning |
|---|---|
| `ingress_bytes_total` | bytes uploaded to MinIO |
| `egress_bytes_total` | bytes read from MinIO during restores |
| `persisted_bytes_total` | cumulative bytes committed, monotonic |
| `retained_bytes` | bytes currently retained, the budgeted quantity |
| `evicted_bytes_total` | bytes deleted to make room |
| `restore_total{result}` | `ok` / `miss` / `corrupt` / `stale` |
| `reserve_rejected_total` | writes refused for capacity |
| `reservation_leaks` | reserved rows the sweep reclaimed |
| `capture_seconds`, `restore_seconds` | histograms, p50/p95 |

`persisted_bytes_total` is cumulative and `retained_bytes` is what occupies the disk; a run that
writes 20 GB and evicts 15 GB reports both. The run's report carries one assertion: every sample of
`retained_bytes` is at or below the budget, not only the last one.

## 8. Benchmark, three steps

| Step | Run | Proves |
|---|---|---|
| 1 Persist | launch context, perform actions, `is_persistent=true`, close, terminate | ingress, capture and commit latency |
| 2 Restore | fresh context, `GET`, inject, verify cookies / `localStorage` / IndexedDB / app progress | egress per restore, restore success ratio |
| 3 Pressure | write past the 10 GB budget at the 20 % rate, then keep writing | retained bytes stay under budget, evictions happen, writes continue, an older surviving session still restores |

The same workload runs with the flag forced true and forced false; the delta in sessions/s is the
price of the feature.

## 9. Restore surface

A restored context holds cookies, `localStorage` and IndexedDB. It reloads its page. The running
process, the JavaScript heap, open sockets, `sessionStorage` across a navigation, the DOM and tab
history are outside the blob, so "resume" means the session's identity and application state
survive, and its page costs one load.

## 10. Open

- Blob contents: whether the HTTP cache and Cache Storage are inside it. This single choice moves
  the at-rest figure between 1.3 TiB and 25.8 TiB at 30-day retention.
- Real blob sizes per page class, measured rather than assumed.
- The test strategy for all of the above: an isolated round trip and a budget-pressure run.

## 11. How to check this note

```bash
# the boolean and the lifecycle it plugs into
rg -n "ContextsPerBrowser|Target.disposeBrowserContext" apps/session-lab/internal/

# the metadata store this ledger would sit beside
rg -n "CREATE TABLE" apps/session-lab/internal/platform/store/store.go

# the run script the pressure arm would reuse
sed -n 1,40p scripts/session-lab-experiment.sh
```

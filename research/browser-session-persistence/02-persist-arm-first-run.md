---
title: 'The first persist arm: 19 blobs of empty state, and four defects it found'
kind: experiment
state: benchmarked
date: 2026-11-08
branch: persist-probe
commits: [a25ad36]
tags: [session-lifecycle, persistence, storage, state-benchmarked]
---

# Note 02 — The first persist arm: 19 blobs of empty state, and four defects it found

| | |
|---|---|
| Status | Recorded |
| Date | 2026-11-08 |
| Command | `scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s --persist-rate 0.2` |
| Run id | `20261108T200728Z` — report `docs/session-lab-runs/20261108T200728Z.json`, storage `…-persist.json` (both gitignored) |
| Code | `apps/session-lab` at `a25ad36` on `persist-probe` |
| Related | [01 — the persist gateway, the flag and the budget](01-persist-gateway-and-budget.md) |

Numbers labelled `[M]` were printed by this run, `[I]` is arithmetic over an `[M]` or `[O]`
figure, `[U]` is unmeasured.

## 1. What this run is, and what it is not

The first execution of the capture arm end to end: 50 sessions at 1 session/s on
`sandbox-demo/browser-pool`, 20 s each, eight browser contexts per Chromium, 20 % of
sessions selected to persist. Every session succeeded and the script's teardown left no
claims behind.

It prices the mechanism — the CDP walk, the gzip, the file write, the ledger. It says
nothing about payload, because the workload performs no actions: each session opens
`https://example.com/`, holds it for 20 s and closes. There is also no restore path in
`cmd/persist-lab` yet, so this run writes blobs and never reads one back.

## 2. The platform it ran on `[M]`

| Quantity | Value |
|---|---|
| Pool | `sandbox-demo/browser-pool`, desired 2, ready 2, template `browser-tpl` |
| Node | `fedora`, allocatable 6000m CPU / 15923 MiB / 500 pods |
| Template | `browser` requests 10m / 128 MiB, limits 0m / 768 MiB |
| Preflight ceiling | ~20 browsers, 20 by memory (15923 MiB / 768 MiB each), 488 by pod cap |
| Workload | 20 offered contexts at 8 per browser ≈ 3 browsers, 160 slots |
| Instances | 4 warm claims: 17, 16, 14 and 3 sessions |

## 3. What the run reported `[M]`

| Quantity | Measured |
|---|---|
| sessions planned / stored | 50 / 50, all `success` |
| arrival window | 47.9 s, peak in flight 27, peak waiting 27 |
| offered concurrency | 20.0 at 1.000 sessions/s; measured mean 14.8 |
| queue wait (claim → ready) | p50 0 ms, p95 29 ms, p99 95 ms, max 100 ms |
| acquire (arrival → ready) | p50 1 ms, p95 79 ms, p99 126 ms, all warm |
| hold | p50 20001 ms |
| reset | p50 33 ms, p95 56 ms |
| per-session task time | p50 31 ms, p95 89 ms (n=50); script p50 2 ms; heap p50 0.6 MiB |
| node CPU | peak 1.56 / 6 cores, mean 1.56 (n=35 samples) |
| node memory | minimum available 7095 MiB of 15924 MiB (44.6 %), swap used 2455 MiB |
| resources per session | **0 sessions with samples** — `cmd/persist-lab` supplies no `Sampler` |
| storage | 19 blobs, 1292 bytes, mean 68 B, refused 0, failed 0, budget 0 (unbounded) |
| blob directory | 76 KiB on disk for 1292 bytes of blobs |

## 4. What is inside a blob

```
$ zcat docs/session-lab-runs/persist-blobs/000001-g1.json.gz
{"version":1,"origin":"https://example.com"}
```

40 bytes of JSON, 68 bytes gzipped — gzip's header and trailer cost more than this
payload. Every session logged `0 cookies, 0 items` because `example.com` sets no cookie
and no page in this workload writes `localStorage`, and the runner performs no actions on
the page. The capture path is proven to run, and the sizes it produced describe an empty
page. A blob from a real session carries cookies and per-origin `localStorage`, so
`mean_bytes 68` says nothing about production sizes; note 01 §6 ranges them from 25 KiB
to 5 MiB and E0 exists to replace that range with a measurement.

## 5. The rate the flag asked for is not the rate the run delivered

`--persist-rate 0.2` produced **19 blobs out of 50 sessions, 38 %** `[M]`. The selector is
`sessionID % 100 < round(rate*100)` and this run's ids are 1…50, so ids 1–19 pass and
20–50 do not `[I]`. The bias is a property of the id space rather than of the workload: at
300 000 ids it disappears, at 50 ids it is 1.9×. Any persistence percentage quoted from a
small run is wrong until either the selector keys on something else or the run reports the
achieved fraction beside the requested one.

## 6. On disk, block size dominates the payload

76 KiB of directory for 1292 bytes of blobs `[M]`: 19 files at 68 bytes each, each
occupying a 4 KiB block. Directories in this size range measure inode overhead, so the
directory size is not a storage result and `du -sh` on `persist-blobs` cannot be quoted as
one.

## 7. Capture cost, as far as one empty page can show it

Per-session capture times `[M]`: 24, 10, 11, 12, 11, 8, 3, 9, 12, 5, 11, 6, 12, 76, 3, 6,
3, 23, 12 ms — p50 around 10 ms, one 76 ms outlier. Note 01 §3 carried `5–50 ms [U]` for
capture and this sits inside that band for the cheapest possible page. Capture CPU cannot
be attributed from this run: the runner recorded zero resource samples because
`persist-lab` passes no `Sampler`, so note 01 §5's 0.28-cores-at-60 s figure remains `[U]`.

## 8. Four defects this run exposes

| # | Defect | Where |
|---|---|---|
| 1 | `reserve` refuses when the budget would be crossed but returns nothing, so `Capture` writes the file anyway. A budget bounds the counter, not the disk. | `internal/persist/store.go:113-118` |
| 2 | The generation is a literal: `%06d-g%d.json.gz` with `1`. A session captured twice overwrites its own blob while `bytes` counts both. | `internal/persist/store.go:112` |
| 3 | The capture sits at the top of `closeIsolated`, which `openIsolated` also calls on its three error paths, so a context that failed to open is still offered to the capture path. | `internal/runtime/session/isolate.go:64,68,73`, with the capture block at `:100` |
| 4 | The selector quotes a rate it does not deliver on small id ranges (§5). | `internal/persist/store.go:84-92` |

Defect 1 is why the pressure arm cannot run yet: a run at a 1 MiB budget would report
refusals while the directory grew past it, and the budget invariant is the only thing that
arm exists to prove. The test that should have caught it, `TestBudgetIsReservedNotOverrun`
(`store_test.go:82`), calls `reserve` directly three times and never calls `Capture`.

All four are fixed on `persist-probe` after this run, and this section keeps the state the
run was taken at (`a25ad36`). `reserve` became `commit`, which reports whether the bytes
were booked and `put` refuses to write when they were not; the generation comes from a
per-session counter; the capture moved out of `closeIsolated` to the normal-close site in
`shared.go`, so a context that failed to open and a hold that was interrupted both capture
nothing; and the selector hashes the session id, which holds 20 % of a 50-session run at
20 % `[I]`. The budget and generation fixes carry tests that call `put`, so the path that
writes is the path under test. The hook move has no test: the `session` package has no fake
DevTools browser to fail a context open against, so it is verified by the call-site
inspection and the existing suite.

One follow-up changed the shape of the arm, so the file references above no longer exist:
the local directory writer was deleted and `internal/persist/store.go` became
`internal/persist/persist.go` holding only the interfaces and the selector. MinIO is the
only store, its endpoint and credentials are read from the cluster, and the budget,
generation and eviction tests now run against the `S3` writer through an in-memory
`objectStore`.

## 9. The store is a bounded queue: send, count, evict the oldest

Three steps, three numbers.

| Step | What happens |
|---|---|
| Send | a session that persists writes one gzipped blob to MinIO at `runs/<run-id>/<session>-g<n>.json.gz` |
| Count | `ingest_bytes` adds every byte written and never decreases, so it is the total this arm dealt in over the run |
| Evict | when the next blob would cross `--persist-budget`, the writer deletes the oldest blobs until it fits, then writes |

`retained_bytes` is what the bucket holds now, `evicted_bytes` is what was deleted to keep it
there, and the day-scale figure is `ingest_bytes / run_hours × 24`. Eviction is driven by the
writer's own ordering rather than by a bucket lifecycle rule: two deletion policies on one
keyspace means `retained_bytes` stops describing the store, and that counter is what asserts
the budget.

The store itself is `config/session-lab/templates/minio.yaml` with `minio.enabled=true`: one
replica, one PVC, credentials generated on first installation and preserved across upgrades by
reading the existing Secret back. The writer creates its bucket on first use, so no init Job
is declared.

## 10. The metrics the next runs must carry

The run above reports one line of storage numbers. A queue needs five groups.

**Volume.** `ingest_bytes` — every byte written, cumulative, the total the arm dealt in and the
number to extrapolate to a day (`ingest_bytes / run_hours × 24`); `retained_bytes` — what the
bucket holds now; `evicted_bytes` and `evicted_objects` — what was deleted to stay inside the
budget, so `evicted/ingest` is the churn ratio; and the per-blob size histogram at p50/p95/p99,
split by page class. The first three are already in `<run-id>-persist.json`.

**Latency — write and read paths separately.** `persist_capture_seconds`,
`persist_encode_seconds`, `persist_put_seconds`, `persist_commit_seconds`; on the read
side `restore_get_seconds`, `restore_decode_seconds`, `restore_inject_seconds`,
`restore_load_seconds`, and `restore_seconds` as the whole path — the time from "resume"
to a usable page. `persist_evict_seconds` per sweep. Slowness on the write side is
acceptable in a way the read side is not, which is why they are separate.

**Outcomes — counters by result.** `persist_write_total{ok,refused,failed:<stage>}`;
`restore_total{ok,miss_evicted,corrupt,stale}`, where `miss_evicted` is an expected queue
outcome rather than an error; `persist_sha256_mismatch_total`; and
`restore_waiting_for_slot_total`, which is the coupling between the two paths.

**The price.** `sessions_per_s` at `--persist-rate` 0, 0.2 and 1.0 — the delta is the
feature's cost — plus capture CPU in cores, which needs the `Sampler` wired into
`persist-lab` before it can be reported at all.

## 11. The runs that follow

| Arm | Flags | Question |
|---|---|---|
| A control | `--persist-rate 0` | baseline sessions/s and platform cost |
| B write only | `--persist-rate 0.2`, unbounded budget | ingress GB, PUT latency, sessions/s delta |
| C write and restore | rate 0.2, each persisted blob restored into a fresh context | egress, `restore_seconds` p95, success ratio |
| D pressure | rate 0.5, `--persist-budget 67108864` | evictions happen, `retained_bytes` never crosses, misses are counted, survivors still restore |
| E at capacity | 350 slots, hold ≈ 100 s, rate ≈ 3.5/s for 10 minutes | the same at the recorded concurrency, where capture CPU competes with serving |

Arm C does not exist in the code yet: `cmd/persist-lab` has no restore path, so nothing
today answers whether a session can boot from a stored blob at load. It comes before the
MinIO work, because a MinIO-backed run without it measures write throughput only.

## 12. How to check this note

```bash
# the run's own numbers
cat docs/session-lab-runs/20261108T200728Z-persist.json
python3 scripts/session-lab-report-from-db.py docs/session-lab-runs/20261108T200728Z.db \
  --no-raw -o /tmp/20261108T200728Z-recovered.json

# what a blob actually held
zcat docs/session-lab-runs/persist-blobs/000001-g1.json.gz

# the four defects
sed -n '84,120p' apps/session-lab/internal/persist/store.go
sed -n '94,106p' apps/session-lab/internal/runtime/session/isolate.go
```

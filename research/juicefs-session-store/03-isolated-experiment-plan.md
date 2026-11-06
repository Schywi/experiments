---
title: 'The isolated experiment: a JuiceFS lab for slot snapshots'
kind: experiment
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
tags: [session-lifecycle, storage, juicefs, experiment-design, state-hypothesis]
---

# 03 — The isolated experiment: a JuiceFS lab for slot snapshots

| | |
|---|---|
| Status | Plan, nothing run |
| Date | 2026-11-05 |
| Shape | `config/vm-juicefs-lab/`, following `config/vm-swap-lab/` |
| Model behind it | [01](01-snapshot-workload-and-size-model.md) |
| Verdict it tests | [02 §7](02-juicefs-mechanics-and-verdict.md) |
| Labels | every gate below is a **[U]** engineering threshold chosen before the run; none is a measured property of the platform, and each is replaced by the measured value in the note that reports it |

---

## 1. Why a disposable lab, and what it is allowed to touch

The question needs a **second worker** and an **object store that is not the live node's disk**. It
needs `/dev/dri` for none of it, so the workspace rule applies directly: a VM is an acceptable
environment when the work does not require the GPU, provided no GPU is attached and no host driver
binding is touched. That is the same reasoning that produced `config/vm-swap-lab/`, and the same
scripts apply.

Three isolation constraints, each from a measurement already on record:

1. **The storage node's disk must not be the disk that carries kine.** Note 18 measured 1.5–2.9 s
   commits on the live node's SQLite under load. An object store or metadata engine sharing that
   device would make every latency in the results a measurement of the control plane.
2. **The link between workers and storage must be shaped.** A virtio-net segment between guests on
   one host moves data at memory speed; every throughput and egress number taken over it would be
   optimistic by an order of magnitude. The lab pins `tc qdisc … netem rate` on each guest's
   interface, with 1 GbE and 10 GbE as the two settings.
3. **The live cluster is out of scope for phase 1.** No claims, no pool, no kubelet. The first
   phase mounts and writes from a plain Go process.

## 2. Lab layout

```
config/vm-juicefs-lab/
├── up.sh                 # three guests on a private bridge: store, worker-a, worker-b
├── verify-store.sh       # minio + redis alive, tc shaping applied, time skew < 50 ms
├── install-juicefs.sh    # client version pinned, format + mount with the arm's options
├── gen-corpus.sh         # seeded snapshot corpus from the size model (frozen)
├── run-arms.sh           # phase 1: every arm, same corpus, same concurrency, repeated
├── install-k3s.sh        # phase 2: k3s on both workers, agent-sandbox, one template, one pool
├── run-migration.sh      # phase 2: session-lab with --checkpoint and --migrate
├── compare.sh            # the tables, one per experiment, from the run artefacts
└── down.sh [--purge]     # guests deleted; --purge also drops the base image and keys
```

| Guest | vCPU / RAM | Disk | Runs |
|---|---|---|---|
| `store` | 2 / 3 GiB | qcow2 on the host's SSD, one file | MinIO (single drive, erasure coding off) and Redis |
| `worker-a` | 4 / 6 GiB | qcow2 on a separate directory | JuiceFS client, the load tool; phase 2 adds k3s + a browser pool |
| `worker-b` | 4 / 6 GiB | same image | the second client; phase 2 adds the destination pool |

The cache directory sits on the guest's own disk in phase 1, with an explicit arm that moves it to
a tmpfs-sized, artificially small cache. Phase 2 on the live host would put it on NVMe; a guest
cannot reproduce that device, so the lab measures the layer and the live node measures the
platform.

**Two phases, because the two questions are independent:**

- **P1 — the storage layer.** A Go tool writes and reads the corpus at the modelled rates from both
  workers. No Kubernetes, no browser, no control plane. Everything about throughput, per-op
  latency, hand-off visibility, cache behaviour and metadata load is settled here.
- **P2 — the platform.** k3s on both workers, `session-lab` with a checkpoint path, real Chromium
  contexts, real migration. This phase adds the capture cost and the slot ceiling interaction, and
  nothing else. If P1 says the layer is too slow at these rates, P2 does not run.

## 3. The arms

One variable per arm; format-time options (block size, compression) need their own filesystem, so
each such arm gets its own bucket and metadata database.

| Arm | Layer | Variable | Fixed |
|---|---|---|---|
| `jfs-default` | JuiceFS write-through, Redis metadata, block 4 MiB, compress none | — the baseline | cache on guest disk, `--trash-days 0` |
| `jfs-writeback` | JuiceFS `--writeback` | commit point | same filesystem as `jfs-default` |
| `jfs-smallblock` | JuiceFS format `--block-size 64` | block size | separate filesystem, write-through |
| `jfs-trash` | JuiceFS with `--trash-days 1` | retention | separate filesystem, write-through |
| `jfs-sqlmeta` | metadata engine `sqlite3://` | metadata engine | separate filesystem, write-through |
| `jfs-thrash` | `--cache-size 64` MiB | cache size vs hot set | same filesystem as `jfs-default` |
| `obj-direct` | S3 client + a local content-addressed cache | everything (the control) | same MinIO bucket family, same corpus |
| `local-async` | local directory + background upload | everything (the floor) | same corpus, upload delay not counted in the write |

The last two are the arms that make the result interpretable. `obj-direct` answers "what does the
metadata engine and the FUSE hop cost"; `local-async` answers "what is the cheapest thing that
could possibly work". A JuiceFS win over `local-async` is not expected and not required; a JuiceFS
loss to `obj-direct` by more than the gate is a finding that decides the design.

## 4. The corpus

`gen-corpus.sh` produces a frozen, seeded corpus — the same discipline as `benchmark/baseline/`:
do not edit it, add a dated directory.

```
corpus/2026-11-XX/
  L/  1000 files, 32 KiB, cookies + two origins of localStorage
  M/  1000 files, 256 KiB, cookies + five origins + a small IndexedDB export
  H/   200 files, 2 MiB, the same shape with a large IndexedDB export
  H8/  100 files, 8 MiB, the tail case
```

Each file is realistic JSON (cookie arrays with the field shape CDP returns), so `gzip` and any
compression setting behave the way they will in production. Sizes are drawn from a log-normal
around the profile's mean, so percentiles are meaningful; the seed is recorded in the directory
name.

E0 (§5) replaces the assumptions in this corpus with measured ones and generates a new dated
corpus. Runs that predate that are labelled as model-driven, not workload-driven.

## 5. The experiment matrix

Each row states the question, the run, and the number that decides it. Gates are the values at
which the design changes; they exist so that a result cannot be read as "acceptable" after the
fact.

| # | Question | Run | Measured | Gate |
|---|---|---|---|---|
| **E0** | How big is a real slot snapshot? | P1, capture from 3 page classes × 30 slots each on the live cluster (read-only; no state is written anywhere) | bytes p50/p95/max per class, gzip ratio, cookies/`localStorage`/IndexedDB split, capture wall time | p95 ≤ 1 MiB keeps the small-object design; p95 > 8 MiB re-runs the model in note 01 and re-plans |
| **E1** | What does one checkpoint cost, end to end? | P1 + P2, 60 s cadence, 168 slots | `checkpoint_ms` p50/p95 split into extract / serialise / write / close; PUT and metadata counts per checkpoint from MinIO's access log | p95 ≤ 50 ms; capture CPU ≤ 5 % of the node's free cores |
| **E2** | Where does the write path saturate? | P1, writer ladder 1 → 8 → 42 → 168 → 336 writers, each at T = 300/60/15 s | snapshot/s, MiB/s, write p50/p95/p99, error rate, object-store PUT rate | 336 writers at T=15 (22.4/s, 5.6 MiB/s) with p95 ≤ 50 ms and zero errors |
| **E3** | What does a cold read cost on another worker? | P1, worker-b reads every file worker-a wrote, cache cold | GET latency p50/p95, bytes per read, prefetch amplification, effect of block size | cold p95 ≤ 100 ms at 256 KiB over the 1 GbE shape; warm p95 ≤ 5 ms |
| **E4** | Is the hand-off visible? | P1, 100 write→read hand-offs per arm, read issued at 0/10/100/1 000 ms after close | read errors, truncated snapshots (length mismatch), visibility lag | zero errors and zero truncations in `jfs-default`; **`jfs-writeback` is expected to fail this and that failure is the result** |
| **E5** | What does losing a worker cost? | P1, kill worker-a mid-run; worker-b restores all 168 slots | restore wall time, egress bytes, peak GET rate, any missing key | 168 restores ≤ 60 s, egress ≤ 42 MiB at 256 KiB, zero missing keys |
| **E6** | Does the cache have to be NVMe, and how big? | P1, `jfs-thrash` with the cache at 10 % and 100 % of the hot set | cache hit ratio, refault count, write p95 under thrash | at 10 % the miss ratio tracks the size ratio and p95 stays ≤ 100 ms; superlinear degradation makes cache sizing a first-class lever |
| **E7** | Does checkpointing move the slot ceiling? | P2, `session-lab` at offered load 1, 2 and 4 sessions/s, checkpoint off / 300 s / 60 s / 15 s, 168 and 336 slots | sessions/s, hold p95, node CPU peak, throttled sessions, `checkpoint_ms` | no more than a 5 % drop in sessions/s at the 60 s cadence; any drop beyond that is reported as the cadence's capacity cost |

E0 is the only row that touches the live cluster, and it writes nothing there: it reads state out
of existing contexts and prints the sizes. If the user would rather not run it against the live
node at all, it runs in P2 of the lab and the plan keeps its order apart from one substitution.

## 6. What the harness needs

`apps/session-lab` has no snapshot path. The additions, kept small enough to land as one change:

| Where | Addition | Why |
|---|---|---|
| `internal/runtime/browser/state.go` | `Snapshot(ctx) (State, error)` and `Restore(ctx, State)`; the walk over `Storage.getCookies`, `DOMStorage.*`, `Storage.indexedDB.*` | capture is per slot, through the CDP connection the runtime already has |
| `internal/runtime/slotstore/` | one interface: `Put(ctx, slot, generation, []byte) (Key, error)`, `Get(ctx, key) ([]byte, error)`, `Reap(ctx, before)`. Two implementations: `fs` (the JuiceFS mount) and `s3` | the arm becomes a configuration value instead of a branch; every arm runs the same code path above the interface |
| `internal/platform/model` | `RunConfig`: `CheckpointEvery`, `SnapshotRoot`, `SnapshotStore` (`fs`/`s3`), `SnapshotJitter`, `MigrateEvery`, `MigrateTo` | no figure from a document is an input to the runner; these are workload parameters |
| `internal/runtime/session/run.go` | a per-slot checkpoint loop, generation counter, and a migrate path that disposes the context on A and restores on B | the hand-off has to be the same code a production runtime would use |
| `internal/infra/measure` | `checkpoint_ms`, `snapshot_bytes`, `snapshot_write_ms`, `snapshot_read_ms`, `snapshot_failures`; the client's own staging counters from `<mount>/.stats` (`juicefs_staging_block_bytes`) | per-checkpoint, not per-run, so the distribution is visible |
| `internal/runtime/report` | a `storage` block: bytes and objects written and read, cache hit ratio where the layer exposes it, `checkpoint_s`, `restore_s`, first hand-off failure | the report contract already carries latency blocks; this is one more |
| `internal/platform/store` | `snapshots(run_id, slot, generation, bytes, sha256, put_ns, closed_ns, key)` | the run database is the only place a checkpoint's provenance can be reconstructed later |
| `cmd/snapshot-bench` | the phase-1 load tool, sharing the corpus generator and the store interface | P1 must not need Kubernetes or a browser |

Naming: `Resume` already means suspend-and-resume of a pod (`session.Run` line 495 and following).
The new path is `Snapshot`/`Restore`, and the history note in 16.2 will need a glyph for a restore
that is not a resume.

## 7. Traps that would silently invalidate a result

1. **An unshaped link.** Every number becomes a measurement of shared memory. Verify with
   `verify-store.sh`: an `iperf3` between guests must report the configured rate within 10 %.
2. **A shared disk.** MinIO and Redis on the device that holds the client's cache, or on the disk
   the host uses for kine, produces latency that belongs to neither component.
3. **Object-store erasure coding.** MinIO in single-drive mode writes one file; the distributed
   mode adds a parity computation per PUT that no result here should include.
4. **Clock skew between guests.** A hand-off's visibility lag is a difference of two clocks. NTP
   inside the guests, verified under 50 ms, or lag is measured on one worker against the object
   store's own timestamps.
5. **In-step checkpoints.** 168 slots firing at the same instant is a different (worse) workload
   than 168 firing at random offsets. `SnapshotJitter` exists for this; both settings are arms.
6. **`--trash-days` left at its default.** Deleting a superseded generation does not free the
   bucket, so the "storage used" curve measures the trash window.
7. **Mixing write-back and write-through in one arm.** The two change the commit point, and a run
   that toggles between them has no valid hand-off measurement.
8. **A warm page cache pretending to be a warm block cache.** Drop caches between cold-read runs,
   and record whether it was dropped.
9. **Comparing arms at different concurrency.** Same writers, same corpus, same duration, same
   order; the swap lab's `run-ab.sh` fixed its environment variables precisely because a drifting
   `RATE` invalidates an A/B.
10. **Reporting a document's figure as a result.** The runner rule holds here: node capacity,
    object-store limits and cache sizes are read at run time, never typed into a summary.

## 8. What would change the recommendation

| Observation | Consequence |
|---|---|
| E0 p95 > 8 MiB | the design changes before the storage question does: chunked snapshots, per-origin files, or an incremental delta; note 01's tables get re-run at 8 MiB |
| E4 shows `jfs-default` also failing hand-off | the problem is JuiceFS's metadata propagation, and object-store-direct becomes the default design |
| E1 p95 > 200 ms | capture, not storage, dominates; the cadence drops to event-driven and this layer stops being the interesting one |
| E2 or E3 fail at 336 writers | 336 slots is out of reach with this layer, and the doubled-capacity case needs a different store |
| E6 shows the cache must be full-hot-set-sized | NVMe staging is a requirement, and a node without NVMe is out of the capacity plan |
| E7 shows a > 5 % `sessions/s` drop | checkpoint cadence is priced into capacity, and the base case is 300 s |

## 9. Order, effort, artefacts

1. `E0` — capture sizes on real pages. Half a day including the CDP walk. Output: a dated corpus
   and one table.
2. `P1` lab — `up.sh`, `verify-store.sh`, `install-juicefs.sh`, `cmd/snapshot-bench`, corpus.
   About a day, plus a day of runs for E1–E6 across the arms.
3. `P2` — k3s on two guests, `session-lab` with the snapshot path, `run-migration.sh`. Two days,
   most of it in the harness and in the second pool's readiness, matching what note 19 cost the GPU
   A/B.
4. `compare.sh` — one table per experiment, no prose. The write-up cites the tables.

Artefacts: every run writes `<run-id>.json` and `<run-id>.db` under the lab's `runs/`, copied back
before `down.sh` (the swap lab lost two JSON artefacts to `down.sh` deleting `runs/`; the copy step
in `run-arms.sh` exists because of that). The lab's `README.md` states where each table came from
and which client version produced it.

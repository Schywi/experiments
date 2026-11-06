---
title: 'The architecture: JuiceFS snapshot plane on the sandbox platform'
kind: design
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
tags: [session-lifecycle, storage, juicefs, architecture, state-hypothesis]
---

# 04 — The architecture: JuiceFS snapshot plane on the sandbox platform

| | |
|---|---|
| Status | Design, nothing deployed |
| Date | 2026-11-05 |
| Fits | `config/session-lab/`, `apps/session-lab/`, `config/sandbox/` |
| Sized by | [01 — size and rate model](01-snapshot-workload-and-size-model.md) |
| Decided against | [02 — mechanisms and verdict](02-juicefs-mechanics-and-verdict.md) |
| Measured by | [03 — experiment plan](03-isolated-experiment-plan.md) E1, E4, E7 |

One shared POSIX namespace for session state. JuiceFS client in the runner pod, Redis as metadata
engine, MinIO as object store, both on the node's existing SSD. Snapshot subtree write-through,
event-driven checkpoints with a 5-minute floor, four generations retained, `--trash-days 0`. Slot
ownership is fenced by a Kubernetes `Lease`, not by the filesystem. The existing control plane —
kine/SQLite, agent-sandbox, the pool autoscaler, the browser template — is untouched.

The alternative was one object per (slot, generation) through an S3 client with a local
content-addressed cache, which removes two services and one FUSE hop. It loses the shared
namespace that the platform will want for run databases, model weights and profile seeds, and that
namespace is the reason to pay for the metadata engine. E1/E3 keep the S3 client as a control arm;
if it wins on hand-off latency by more than 2×, the snapshot subtree moves to it and JuiceFS keeps
the artefacts.

## 1. Topology

```
                        +--------------------------------------+
  browser / kubectl --> |  cilium LB  192.168.0.247 / console  |
                        +----------------+---------------------+
                                         |
      +----------------------------------+-----------------------------+
      |                                  |                             |
+-----v----------+           +-----------v-----------+     +-----------v-------+
| sandbox-console|           | session-lab runner    |     | grafana           |
| ui + runs db   |           | slots + snapshot I/O  |     | 6 panels          |
+-----+----------+           +--+----------+------+--+     +-----------^-------+
      |                         |          |      |                      |
      | read runs               | claim    | CDP  | 3 write / 4 read     | scrape
      v                         v          v      |                      |
+----------------+     +-----------------+  +-----v-------------+  +-----+----------+
| run sqlite pvc |     | agent-sandbox   |  | browser pod  x21  |  | victoria-      |
| local-path     |     | ctrl + pool     |  | chromium + bridge |  | metrics        |
+----------------+     | autoscaler      |  | 8 ctx = 8 slots   |  +-----^----------+
                       +-----------------+  +-------------------+        |
                                                                         |
  ~~~~~~~~~~~~~~~~~~~~~~~~~ snapshot plane (new) ~~~~~~~~~~~~~~~~~~~~~~~ |
                                                                         |
      +------------------------+   3  /mnt/session-store  +--------------+---+
      | slotstore              |-------------------------->| juicefs client   |
      |  Put / Get / Reap      |<--------------------------| cache 2 GiB      |
      +-----------+------------+   4  read on restore     +------+--------+--+
                  |                                              |        |
                  | 5 lease + gen (fencing, not the FS)           | 6 meta | 7 data
                  v                                              v        v
      +------------------------+                        +--------+  +-----+------+
      | kubernetes Lease       |                        | redis  |  | minio      |
      |  slot-0007 owner A     |                        | aof    |  | 5 GiB quota|
      +------------------------+                        +--------+  +------------+
```

Authority per fact:

| Fact | Authority | Read by |
|---|---|---|
| which worker owns slot 7, at which generation | `Lease` `slot-0007` in `sandbox-demo` | runner, before any write or restore |
| which bytes are committed for (slot, generation) | the `gen-N.meta.json` object in JuiceFS, renamed last | runner, on restore |
| namespace and block map | Redis | every JuiceFS client; nobody else |
| the bytes | MinIO bucket `jfs-session` | JuiceFS client only |
| per-session record of what happened | run SQLite (`<run-id>.db`) | console, report |

## 2. Checkpoint, the write path

```
 runner                 browser pod A            /mnt/session-store          minio+redis
   |                         |                          |                        |
   |-- 1 CDP /cdp ---------->|  Storage.getCookies      |                        |
   |     per slot context    |  DOMStorage.* (origin)   |                        |
   |                         |  Storage.indexedDB.*     |                        |
   |<-- 2 state JSON --------|  32 KiB - 256 KiB raw    |                        |
   |   5-30 ms p95           |                          |                        |
   |                                                    |                        |
   |-- 3 gzip (~4:1) + write tmp/gen-N.json.gz ------->| buffer, 8-64 KiB       |
   |-- 4 rename tmp -> gen-N.json.gz ----------------->| atomic in namespace    |
   |                                                    |-- 5 close: PUT ------>|
   |                                                    |-- 6 txn file+block -->|
   |<-- 7 close returns, 2-10 ms local -----------------|                        |
   |-- 8 put gen-N.meta.json (sha256, bytes) [commit] ->|                        |
```

`checkpoint_ms` budget at p95: **50 ms**, composed of extract 30 + gzip 3 + close 10 + 7 slack.
At 168 slots on a 60 s cadence that costs 0.28 of the node's 2.7 free cores; event-driven with a
300 s floor costs 0.06 (note 01 §5).

The session is never blocked on the commit: steps 1–2 run in the slot's own goroutine, 3–8 run in
a per-slot writer goroutine. A checkpoint that fails is retried once and then recorded, not
retried forever — the next event supersedes it.

## 3. Restore, the resume path

```
 runner              lease            worker A            worker B          store
   |                   |                 |                   |                |
   |-- need slot 7 ----|                 |                   |                |
   |<- owner=A,gen=42--|                 |                   |                |
   |-- fence: lease 7=A,gen=43 --------->|                   |                |
   |-- dispose ctx 7 on A -------------->| session ends      |                |
   |-- create ctx on B ------------------------------------->|                |
   |-- read slots/0007/gen-42 -----------------  cache hit 1-5 ms ------------>|
   |                                     miss: GET 20-100 ms over a 1 GbE link->|
   |-- verify sha256 and byte count ----------------------------------------->|
   |-- Restore cookies / localStorage / IndexedDB ----------->|                |
   |-- navigate + waitForLoad ------------------------------->| page reload    |
   |-- lease 7=B,gen=43; next event writes gen-43 --------------------------->|
```

What resumes: cookies, `localStorage`, IndexedDB. What does not: `sessionStorage`, the HTTP cache,
service workers and Cache Storage, in-flight JavaScript, the DOM, tab history. A restored session
reloads its page and re-authenticates; the storage layer moves kilobytes, not a process.

The fence in line 3 is what makes the hand-off safe: a restore of generation 42 is refused if the
lease has moved past 42, and a late write from A carries generation 43 only after A re-reads the
lease. JuiceFS provides no lease, and a filesystem cannot.

## 4. Namespace layout

```
/mnt/session-store/
  slots/0007/
    gen-42.json.gz          8-64 KiB, one object, one PUT
    gen-42.meta.json        {sha256, bytes, closed_ns, owner, gen}
    gen-41.json.gz          kept: G = 4 generations
  leases/0007.json          audit copy of the Lease, written by the runner
  runs/<run-id>.json        report artefact (existing report, relocated)
  reports/                  console artefacts

  commit point = the rename that creates gen-N.meta.json.
  a gen-N.json.gz without its meta is invisible to every reader.
```

## 5. Components and what they cost

| Component | Form | Requests / limits | Disk | Why |
|---|---|---|---|---|
| `juicefs-csi-driver` | DaemonSet + mount pod, namespace `kube-system` | 100m / 128Mi | — | mounts the filesystem into pods; `mountPropagation: HostToContainer` |
| metadata engine | `redis:7-alpine`, 1 replica, AOF everysec, `maxmemory 256mb`, `noeviction` | 100m / 256Mi | 256 MiB | namespace and block map; the single authority |
| object store | `minio` single drive, 1 replica, bucket quota 5 GiB, lifecycle expiry 24 h | 200m / 512Mi | 5 GiB | the bytes |
| `slotstore` | new package inside the runner | — | — | `Put` / `Get` / `Reap` over the mount |
| Lease | Kubernetes object per active slot | — | etcd/kine rows | fencing, one write per hand-off |

StorageClass, 1 PVC, `ReadWriteMany` (JuiceFS serves it; RWO would also do on one node):

```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: session-store
  namespace: sandbox-demo
spec:
  accessModes: [ReadWriteMany]
  storageClassName: juicefs-session
  resources: {requests: {storage: 20Gi}}
```

## 6. Format and mount — the flags that decide behaviour

```
juicefs format --compress zstd --block-size 4096 --trash-days 0 \
  redis://redis.sandbox-demo:6379/1 jfs-session

juicefs mount -d --cache-dir /var/jfsCache --cache-size 2048 \
  --free-space-ratio 0.05 redis://redis.sandbox-demo:6379/1 /mnt/session-store

# absent on purpose: --writeback
#   with it, close returns before the upload and the receiving worker reads EIO.
# absent on purpose: --open-cache
#   it breaks close-to-open, which is the only cross-worker guarantee in use.
```

`--trash-days 0` matters at this cadence: every superseded generation is deleted, and the default
retention keeps those bytes in the bucket for a day. `--compress zstd` is applied by the client at
write time; the gzip in step 3 is the application-level copy that also bounds what leaves the
browser.

## 7. Disk budget

Every size below is a design requirement, independent of the node that carries it:

| Consumer | Size | Note |
|---|---|---|
| MinIO bucket `jfs-session` | 5 GiB quota | lifecycle expiry 24 h |
| Redis AOF | 256 MiB | `appendfsync everysec` |
| JuiceFS cache dir | 2 GiB | hot set is 10.5 MiB compressed; this is slack |
| snapshots, 4 generations × 168 slots | 42 MiB | 64 KiB × 4 × 168 |
| **total new** | **~7.3 GiB** | leaves 15 GiB for kine, containerd, registry |

A node that carries this layer needs 7.3 GiB of free space on the volume that holds MinIO and the
client cache, and that volume must not be the one kine's SQLite writes to. On a node that already
runs the container store, the registry and the cluster datastore, check free space against that
7.3 GiB before mounting.

## 8. Metrics contract

Six panels, each answering one question a reviewer will ask.

| Panel | Series | Type | Claim it backs |
|---|---|---|---|
| Checkpoint cost, split | `session_lab_checkpoint_seconds` + `_extract_` + `_write_` + `_close_` | histogram, p50/p95/p99 | p95 ≤ 50 ms, and which phase pays |
| Snapshots and bytes | `session_lab_snapshots_total`, `session_lab_snapshot_bytes_total` | counter | 0.28 snap/s, 0.7 MiB/s at 60 s |
| Restore latency | `session_lab_restore_seconds` | histogram | page-load cost of a resume |
| Hand-off outcome | `session_lab_handoff_total{result}` | counter, `ok\|stale\|error\|refused` | zero errors in 200 moves |
| Object store and metadata | `minio_bucket_usage_total_bytes`, `s3_requests_total{api="putobject"}`, `s3_ttfb_seconds`, `redis_commands_processed_total` | gauges/counters | one PUT per snapshot; metadata is not the bottleneck |
| Cache and staging | `juicefs_blockcache_hits_total`, `juicefs_blockcache_miss_bytes`, `juicefs_staging_block_bytes` | counters | cache holds the hot set; **staging stays 0** |

Capacity row, reported next to the six: `session_lab_sessions_per_second` for checkpoint off,
event-only, and 60 s, at fixed offered load. That is the number that turns the cadence from a
storage setting into a capacity cost.

A report line a reviewer can read without the dashboard:

```
storage: 168 slots, checkpoint p95 47 ms (extract 29 / write 8 / close 10),
         9.1 KiB mean snapshot gzip, 0.30 snap/s, 2.7 KiB/s,
         200 hand-offs, 0 stale, 0 error, restore p95 141 ms,
         cache hit 99.6 %, staging 0 B, bucket 41 MiB, 4 generations
```

## 9. Failure analysis, ranked by likelihood

| # | Trigger | Symptom | Mechanism | Mitigation | Signal |
|---|---|---|---|---|---|
| 1 | `--writeback` lands in the mount args, or the cache fills | hand-off read returns EIO, restored session empty | bytes sit in `/var/jfsCache/<UUID>/rawstaging/`; no per-file upload barrier exists | mount args are one chart value with a startup assertion; alert when staging > 0 for 60 s | `juicefs_staging_block_bytes`, `session_lab_handoff_total{result="error"}` |
| 2 | Redis restart, or memory pressure with eviction allowed | every mount stalls, checkpoints fail, browsers keep serving | metadata is the single authority; a restart drops the last second of AOF, `allkeys-lru` would drop live keys | AOF everysec, `maxmemory-policy noeviction`, `--backup-meta 3600` off-pod, client retry with 3 s backoff | `redis_up`, `redis_memory_used_bytes`, mount-pod `up` |
| 3 | rebalance moves slot 7 while A is still writing | a snapshot that mixes generations, no error surfaced | JuiceFS has no lease or fencing; last writer wins | `Lease` + generation, reader rejects `gen < lease.gen`, writer re-reads the lease before rename | `session_lab_handoff_total{result="stale"}` |
| 4 | CSI mount pod evicted, or the node's FUSE mount dies | pods see a stale directory or EIO; `mountPropagation` stops when the mount pod is gone | the mount pod is the owner of the mount on that node | PDB `maxUnavailable: 0`, a 30 s canary write from the runner, restart-on-canary-failure | `session_lab_canary_age_seconds` |

Partial-failure case worth naming: Redis up but MinIO down. Metadata commits, uploads fail, `close`
returns an error and the checkpoint is recorded as failed; reads of older generations still hit the
local cache. The layer degrades one direction at a time, which is why the `checkpoint_failures`
counter is on the panel rather than in a log line.

## 10. Tradeoff register

- **Two more stateful services on one node** — Redis and MinIO share the disk with kine's SQLite,
  whose 1.5–2.9 s commits are already the control-plane ceiling (note 18 §4) — accepted because the
  measured snapshot traffic is 0.7 MiB/s and the bucket is quota'd — revisit when any kine commit
  exceeds 100 ms with the layer mounted.
- **Write-through on every checkpoint** — each snapshot pays one PUT, 2–10 ms locally and tens of
  ms against remote object storage — accepted because a hand-off read is exactly the read that
  write-back breaks — revisit if p95 `checkpoint_ms` exceeds 50 ms at the base cadence.
- **Identity-only resume** — a page reload and a re-auth per restore, no HTTP cache, no service
  worker — accepted because it keeps the storage layer at kilobytes instead of gigabytes —
  revisit if the product needs the live DOM, which moves the design to pod suspend and makes this
  layer irrelevant.
- **Fencing outside the filesystem** — one extra Lease write per hand-off, and a generation check
  in every reader — accepted because no POSIX layer offers conditional writes — revisit at
  multi-node, where a pointer object with a conditional PUT replaces the Lease.
- **No durability domain** — node loss takes Redis, MinIO and every snapshot with it — accepted
  because a lost snapshot costs a page load, not a session — revisit when a second node exists to
  replicate to.

## 11. The scale-later path

| Today | With a second worker | With a remote object store |
|---|---|---|
| Redis on the node, AOF to `sda3` | Redis stays, `--backup-meta` to the second node | Postgres or TiKV as the metadata engine |
| MinIO single drive, 5 GiB | MinIO or GCS; bucket per environment | GCS/S3, lifecycle rules replace the reaper |
| host FUSE mount via CSI on one node | mount pod per node, `ReadWriteMany` PVC | same |
| Lease fencing per slot | `Lease` stays for ownership; pointer object with `If-Match` for commit | same |
| 168 slots, 42 MiB of state | 336 slots, 84 MiB | unchanged arithmetic |

The numbers that scale are the ones in note 01 §4; nothing in this design is sized by the node.

Decision: build `slotstore` (Put/Get/Reap over a write-through JuiceFS mount, gzip, `gen-N` data
plus `gen-N.meta` commit, generation-fenced readers) into `apps/session-lab`, add Redis + MinIO +
one PVC to `config/session-lab/`, fence slot ownership with a `Lease`, and ship the six
panels above.

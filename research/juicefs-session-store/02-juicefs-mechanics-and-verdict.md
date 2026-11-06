---
title: 'JuiceFS for slot snapshots: mechanics, comparison, verdict'
kind: analysis
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
tags: [session-lifecycle, storage, juicefs, state-hypothesis]
---

# 02 — JuiceFS for slot snapshots: mechanics, comparison, verdict

| | |
|---|---|
| Status | Analysis of documented behaviour; no measurement yet |
| Date | 2026-11-05 |
| Reads with | [01 — size and rate model](01-snapshot-workload-and-size-model.md), [03 — the experiment plan](03-isolated-experiment-plan.md) |
| Vendor docs | Community Edition: `architecture`, `guide/cache`, `command_reference`, `fuse_mount_options`, `guide/block-device`, `production_deployment_recommendations`, `csi/guide/pv` |

---

## 1. The three parts, and where they land on this platform

JuiceFS is a client plus two external services: a **metadata engine** (Redis, TiKV, MySQL,
MariaDB, PostgreSQL or SQLite) holding the namespace and the block map, and **object storage**
holding the data. The client talks to both; FUSE is one of several access paths. **[D]**
(`docs/community/architecture`).

For a single-node bare-metal cluster, that shape is worth naming plainly: the metadata engine and
the object store are two more stateful services to run, back up and keep alive, so that a
directory can be shared. On a single node a local directory already is shared; the value appears
when there are two workers, or when the same namespace has to serve artefacts other than
snapshots.

## 2. The write path, per mode

| Step | `--writeback` off (default) | `--writeback` on |
|---|---|---|
| `write()` | into the client's memory buffer (300 MiB default) | same |
| `close()` / `fsync()` | flush → upload the object → metadata commit | write into the local staging directory `/var/jfsCache/<UUID>/rawstaging/` and return |
| Upload | completed before close returns | asynchronous, in the background |
| Visibility to another node | after close, per close-to-open consistency | **only after the upload finishes**; before that, a read from another node returns a timeout or an I/O error |
| Data at risk on client or node death | none committed | everything still staged |

**[D]** `guide/cache#client-write-cache` and `command_reference#mount`; the docs state that with
write-back enabled a read on another node can fail while staging is behind, and recommend the mode
for temporary bulk writes of many small files.

A snapshot write is a sub-block file: it is uploaded at close as one object whose size is the file
size, alongside a handful of metadata transactions (create, write/setattr). The default block size
is 4 MiB and is fixed at format time; the chunk is 64 MB. **[D]** `command_reference`. So the
per-checkpoint object-storage bill at 256 KiB is one PUT of 256 KiB, and at 2 MiB it is still one
PUT — the 4 MiB block never forces a write amplification for a small file.

The docs are explicit that this class of I/O is the expensive one for this filesystem: "the
performance of small files or large amounts of random reads/writes is significantly worse than
sequential reads/writes", with the shared-block-device feature (client 5.2+) as the mitigation.
**[D]** `guide/block-device`. That statement is about throughput on a loaded filesystem, and with
2.8–5.6 snapshots/s it will not be the limiting factor here; it is quoted because it sets the
expectation for the E2 ladder.

## 3. The read path

| Case | What happens |
|---|---|
| block present in the local cache | read from the cache; the docs quote microseconds to a few GiB/s for repeated reads |
| cache miss | `GetObject` (or a ranged get) for the block, then a prefetch of the following ones |
| cache directory broken, write-back off | the client errors and falls back to reading object storage directly |
| cache directory broken, write-back on | the client hangs with the cache |

**[D]** `guide/cache#client-read-cache`. Default cache: `/var/jfsCache` (or `$HOME/.juicefs/cache`),
`--cache-size=102400` MiB, `--free-space-ratio=0.1`, shared between the read cache and the write
staging. `--prefetch` defaults to 1 block. **[D]** `command_reference#mount`.

At 42 MiB of hot set against a 100 GiB default cache, every snapshot read is a cache hit after its
first touch. The interesting read is therefore **the first one on a node that has never seen the
file** — the hand-off — and that one is a cold `GetObject`.

## 4. Consistency

Default is close-to-open: after a client modifies and closes a file, another client opening it
sees the new state. The client keeps the committed metadata in memory and in the kernel for a
short interval, and `--open-cache` trades that guarantee for a TTL. **[D]**
`guide/cache#consistency`, `#client-memory-metadata-cache`.

Mapping to the requirements in note 01 §6:

| Requirement | JuiceFS | Caveat |
|---|---|---|
| R1 versioned key | directories and files, or one object per generation | the generation has to be in the path or in the file; the filesystem does not mint versions |
| R2 atomic commit | rename is atomic; a single sub-block file commits at close | a partially written file is visible with its growing size while open; write to a temp name and rename |
| R3 read-after-write across workers | holds at close, in write-through mode | **fails in write-back mode until the upload completes**, and no per-file upload barrier is documented |
| R4 one writer per key | not provided | no lease, no fencing; must come from the runner or a Kubernetes `Lease` |
| R5 safe deletion | POSIX unlink semantics | with `--trash-days` (default 1) the object bytes stay in the bucket, so deletion frees the namespace and not the bill |
| R6 corruption detection | no per-file checksum surfaced to the reader | a short read looks like a short file; a length field in the snapshot is the cheap fix |
| R7 bounded retention | trash-days, quota, background expiry | object storage can be much larger than the logical filesystem, by design |

The trash behaviour deserves its own line because it interacts with a checkpoint loop: every
superseded generation that is deleted still occupies object storage for the retention window, and
the docs recommend `--trash-days 0` when an application rewrites or deletes files frequently.
**[D]** `production_deployment_recommendations`. At the base case of 63 GB/day, the default trash
window roughly doubles the bucket's growth, and at the 3.9 TB/day corner it is the difference
between a plan and an incident.

## 5. Access from Kubernetes

The CSI driver runs a **mount pod** per volume and the application pod mounts the result with
`mountPropagation: HostToContainer`. **[D]** `csi/guide/pv`. On this platform that means either a
sidecar-ish mount pod next to `apps/session-lab`, or a node-level mount with a hostPath, which
sidesteps FUSE-in-pod entirely but requires the runner to be scheduled on a node where the mount
exists.

The sandbox pods themselves do not need the mount: capture happens over CDP, from the runner, and
the payload travels runner→storage. That keeps the browser image unchanged, which matters because
that image is the object of other experiments.

## 6. The comparison the decision needs

All four candidates satisfy R1–R3 for a small immutable object with one writer. They differ in
what has to exist, and in what a checkpoint costs.

| | Commit point | Services to run | Checkpoint cost, p50 | Cross-worker read | Retention / GC | Code to write |
|---|---|---|---|---|---|---|
| **JuiceFS, write-through** | object store, at close | metadata engine + object store + CSI mount | extract + one PUT (ms) + metadata txns | cold `GetObject`, cache after | trash-days, quota, `juicefs gc` | a mount, a directory layout, a reaper |
| **JuiceFS, write-back** | local staging, at close | same | extract + local write (µs–ms) | **after upload only** | same, plus staging accounting | same, plus an upload-wait loop |
| **S3/MinIO client + local cache** | the PUT, atomically | object store only | extract + one PUT | one `GetObject` | bucket lifecycle rules | ~200 lines: PUT/GET, listing, TTL, checksum |
| **Local NVMe dir + async copy** | the local write | nothing extra | extract + local write | copy of the file, or a pull from the source worker | a cron or the reaper | the sync logic and its failure handling |
| **RWX PVC / NFS** | the NFS server | the NFS server and its network path | extract + RPC | RPC read | filesystem only | almost none |

Two of these are worth calling out.

The last row is the single-node option: on one host, a directory behind a shared mount covers this
use case with no extra services. It stops being adequate
the moment two workers both need it and the storage tier is remote, which is the direction the GCE
path already points.

The third row is the control arm the experiment needs. Without it, a JuiceFS result at 2.8
snapshots/s says "this works", and there is no way to tell whether the metadata engine plus FUSE
cost anything relative to a PUT and a `stat`. The plan in note 03 runs both.

## 7. Verdict

**The storage layer's job here is a small, immutable, versioned object with an atomic commit, a
read-after-write guarantee across nodes, and a bounded retention.** At 168 slots and a 256 KiB
snapshot the whole hot set is 42 MiB and the base write rate is 0.70 MiB/s. Object storage already
is that system; JuiceFS adds POSIX semantics, one namespace, and a cache with a consistency model,
at the price of a metadata engine, a FUSE hop, and a write-back mode that has to be disabled
exactly where it would help most.

Whether that price is worth paying is measurable, and the measurement decides it:

- **Adopt** JuiceFS if the hand-off p95 is within 2× the object-store arm, the node has no device
  shared between the metadata engine and kine, and the same mount absorbs the platform's other
  artefacts (run databases, model weights, profile seeds). The single-namespace argument is real
  and this repository has four flavours of local storage that would be replaced by it.
- **Do not adopt** it for snapshots alone if the object-store arm matches it on hand-off latency
  and the metadata engine has nowhere to live except the disk that carries kine. A checkpoint loop
  that puts a Redis-and-MinIO stack on the same NVMe as SQLite gives the control plane from note
  18 a neighbour it did not have.

## 8. Local NVMe staging: the answer, split three ways

1. **The JuiceFS cache directory belongs on NVMe.** Every cold read and every write-back staging
   write lands there. Put it on the device that holds neither kine's SQLite file nor the container
   runtime's layers.
2. **A second, hand-written staging layer does not.** It would duplicate the cache, add a second
   eviction policy and a second consistency rule, and the two would disagree during a hand-off.
   One owner for the local bytes.
3. **Write-back stays out of the snapshot path.** It is the right tool for scratch data (profile
   seeds, logs, extracted archives) and the wrong one for a file another worker is about to read,
   for the reason in §2. If the cadence needs to be cheap enough to justify write-back, the
   cadence is too high — see the capture-cost table in note 01 §5.

## 9. What only a measurement can settle

| Claim in this note | Test |
|---|---|
| a checkpoint costs one PUT plus a few metadata transactions | E1: per-checkpoint PUT/GET counts from the object store's own access log |
| close latency is dominated by the PUT | E1: `checkpoint_ms` decomposed into extract, write and close |
| the hand-off fails on write-back before upload completes | E4: write on A, read on B within the upload window, both modes |
| the 4 MiB block does not amplify a 256 KiB file | E2: bytes out of the object store per snapshot |
| the cache holds the whole hot set | E6: refault and miss counters under a deliberately undersized cache |
| the layer does not move the slot ceiling | E7: `sessions/s` with checkpoints off and on, at fixed offered load |

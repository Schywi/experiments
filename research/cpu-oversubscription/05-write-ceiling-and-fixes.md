# 05 — Raising the write ceiling: what stalls, and what to change

| | |
|---|---|
| Written | 2026-11-01 |
| Purpose | turn the diagnosis in [04](04-datastore-capacity-brief.md) into changes that raise sessions/s |
| Verified here | `journalctl -u k3s`, apiserver `/metrics`, `/sys/fs/btrfs/*/commit_stats`, `/proc/diskstats`, `/proc/pressure/*`, `lsblk`, `kubectl` |
| Not verified | `state.db` and WAL size, SQLite pragmas, `btrfs filesystem usage`, `dmesg` (all need root; `/var/lib/rancher/k3s/server/db` is mode 0700) |
| Issues | `experiments-9kl` writes per session, `experiments-c4h` ceiling ladder, `experiments-0fo` kine metrics |

---

## 1. What the numbers say

Read in one session on the live node, no session run active, warm pool at its static 20 replicas.

### The datastore engine is fast

| Signal | Value |
|---|---|
| `AfterAllOldVal` (reads) | 6 301 calls in 3.04 s — **0.48 ms mean** |
| `InsertLastInsertID` (writes) | 5 989 calls in 386.73 s — mean 64.6 ms, **p50 < 1 ms**, p90 ≈ 45 ms, p99 ≈ 1.5–2 s |
| Concentration | 79 inserts (1.3 %) hold ≈ 80 % of all insert time |
| Abandoned writes | 17 `context canceled`, **mean 9.0 s**, none under 4.1 s |

A median of under a millisecond on a single SQLite writer means the writer is idle almost all the time. Throughput is not the constraint.

### The filesystem commit is

| Signal | Value |
|---|---|
| btrfs `commit_stats` | 772 commits, **mean 617 ms**, **max 18 211 ms**, last 179 ms |
| commit interval | one per 30 s (3 commits in 90 s), the default |
| device | 79 % busy, 524 write IOPS, 13.4 MB/s, 5.44 ms mean write service, queue 2.85 |
| device extras | 402 flushes/20 s at 6.45 ms; **1 881 discards/20 s** |
| `io.pressure full` | 16 % baseline, **42 % during a storm** |
| `cpu.pressure full` | **0.00 %** at avg10, avg60 and avg300 in every sample |
| device model | **Kingston SA400S37120G** — 120 GB consumer SATA SSD, DRAM-less |

`max_commit_ms = 18 211` against 82 slow inserts at ≥16 s is the same event seen from two sides: SQLite's fsync waits for whichever btrfs commit is in flight, and that commit can take 18 seconds.

### Engine or filesystem: the separating measurement

The journal timestamps every slow statement's start, so the 2 527 slow inserts were correlated against the 16 compaction windows:

| Slow inserts overlapping a compaction | 107 / 2 527 = **4.2 %** |
|---|---|
| Share of slow-insert time they carry | **8.9 %** |

Compaction owns the single worst event — `DELETE FROM kine … WHERE kp.id > 2099851 AND kp.id <= 2100851`, **39.68 s** — and almost none of the population. 95.8 % of slow writes start outside any compaction.

### What ends a run is not slowness

```
E1031 peer_endpoint_lease.go:156 unable to update peer endpoint leases: database is locked
E1031 controller.go:168 unable to sync kubernetes service: database is locked
level=fatal msg="Transaction commit failed: sql: transaction has already been committed or rolled back"
```

**9 k3s server starts and 15 fatal/lock events in 24 h**, two fatals in the first 15 minutes of this session. k3s exits; the control plane is down for ~35 s; every watch re-lists; every controller rewrites. That restart storm is itself write load, and it provokes the next stall.

### Where the writes come from

| Source | Rate | Evidence |
|---|---|---|
| kine inserts, zero sessions, warm pool 20 | **14.3/s** | 5 989 / 420 s |
| of which Cilium L2 announcement leases | **0.91/s** = 78 900/day | 11 leases, `renewTime` sampled at **12.0 s**, duration 15 |
| that share of *slow lease* writes | **1 424 of 1 645 = 87 %** | journal, 24 h |
| the device's write IOPS that are kine's | **2.7 %** (14.3 of 524) | arithmetic |
| browser pods created with zero sessions | **20 in 21 s** (23:53:32–23:53:53) | `creationTimestamp`; 0 `SandboxClaim`s |
| the stall storm around it | 526 slow inserts in 23:45–00:00 | journal |

So the writer is idle, the filesystem stalls, and 97 % of the writes in each commit window are not kine's. The pod churn is what fills the device.

---

## 2. The write budget, stated as a number to spend

```
sessions/s = (writes/s the writer survives without a >1 s stall) / (writes per session)
```

Measured today, at zero sessions:

| Term | Value |
|---|---|
| Writes/s offered by control-plane churn alone | 14.3 |
| Slow statements at that offer | 823/hour |
| Control-plane exits at that offer | 2 in 15 min |
| Writes per session (floor, from the run records) | 5 |

The storm-free band today is *below* the 14.3 writes/s the cluster produces with nobody using it. Two ways to widen it, and they are independent:

1. **Make the same writes cheaper** — shorten the commit each one waits for (§3, L1–L4).
2. **Ask for fewer writes per session** — a multiplier on whatever the writer can take (§3, L5).

Both are needed. Neither alone moves the number much, because the ceiling after either is ~2.2 sessions/s of CPU and slots (§4).

---

## 3. The fix ladder

Ordered by expected effect per unit of risk. L2 is one sysctl; L1a is a disk you have to buy.

### L1 — Take `state.db` out of the shared commit domain

btrfs commits are per **filesystem**, not per subvolume or per file. `state.db` lives on `/dev/sda3` alongside containerd's layers, kubelet's volumes and every browser profile, so each kine write waits for a commit made long by work that has nothing to do with it. That is the structural cause of every number in §1.

**L1a — a separate filesystem.** This is the fix that removes the class. On this host it needs hardware: `sda` is the only usable device and `sdb` is a 1 TB **spinning** Seagate ST1000DM010 holding NTFS, which is a worse datastore than the SSD it would replace. A second SSD (any NVMe with a DRAM cache) mounted at `/var/lib/rancher/k3s/server/db` is the change to buy; nothing else in this document removes the shared-commit problem.

**L1b — a `nodatacow` subvolume, available today.** Keeps the same device but stops paying COW write amplification and zstd work for a file that is rewritten in place 14 times a second:

```bash
sudo systemctl stop k3s
sudo sqlite3 /var/lib/rancher/k3s/server/db/state.db \
  ".backup '/var/backups/state-$(date +%F).db'"
sudo btrfs subvolume create /dbvol
# /etc/fstab, added:
# UUID=$(blkid -s UUID -o value /dev/sda3) /var/lib/rancher/k3s/server/db \
#   btrfs subvol=/dbvol,nodatacow,compress=no,nodatasum 0 0
sudo mount /var/lib/rancher/k3s/server/db
# copy the backup back in so the new inode is created nocow
sudo sqlite3 /var/backups/state-$(date +%F).db \
  ".backup '/var/lib/rancher/k3s/server/db/state.db'"
sudo systemctl start k3s
```

Cost: no checksums or snapshots for the datastore. `state.db` is already protected by SQLite's own WAL and by the `.backup` above, so put that `.backup` on a timer before starting.

Expected: removes the db's own COW and compression work from each commit. It does **not** remove the container churn from the same commit — only L1a does that.

### L2 — Bound the dirty window so each commit is small

Measured: `vm.dirty_ratio = 20` of 15.9 GiB lets **3.18 GiB** go dirty before writeback starts, and `dirty_background_ratio = 10` starts it at **1.59 GiB**. btrfs forces the dirty set out at commit; a 617 ms mean and an 18 s worst case follow from a dirty set that can be gigabytes.

```bash
sudo sysctl -w vm.dirty_background_bytes=134217728 \
               vm.dirty_bytes=268435456 \
               vm.dirty_expire_centisecs=1000
# persist in /etc/sysctl.d/90-datastore-writeback.conf
```

Note that setting the `_bytes` knobs disables the corresponding `_ratio` knobs, which is the intent.

Measure it without a reboot — `max_commit_ms` is a high-water mark since mount, so compare *deltas* over a fixed window instead:

```bash
U=$(ls /sys/fs/btrfs/)
read -r c0 t0 < <(awk '/^commits/{c=$2} /^total_commit_ms/{t=$2} END{print c, t}' /sys/fs/btrfs/$U/commit_stats)
sleep 600
read -r c1 t1 < <(awk '/^commits/{c=$2} /^total_commit_ms/{t=$2} END{print c, t}' /sys/fs/btrfs/$U/commit_stats)
echo "commits=$((c1-c0)) mean_commit_ms=$(( (t1-t0) / (c1-c0) ))"
```

Expected: mean commit time scales with the dirty set, so 3.18 GiB → 256 MiB should take the mean well under 617 ms. This is the highest expected effect per unit of risk in the document: one sysctl, reverted with one more.

### L3 — Let the writer wait instead of failing

`database is locked` is SQLite's busy handler giving up. Every occurrence becomes an apiserver error, and the ones that land inside a transaction become the fatal that restarts k3s.

k3s accepts a SQLite data source name (`k3s server --help`: *"Specify etcd, NATS, MySQL, Postgres, or SQLite (default) data source name"*), and kine uses `mattn/go-sqlite3`, whose DSN takes `_busy_timeout` and `_synchronous`. Raising the timeout converts a hard failure into a wait — more writes accepted, at a higher p99. `_synchronous=NORMAL` removes one fsync per commit; a process crash stays safe, a power loss can lose the last transactions.

Before changing the live node, read the current values (needs root, which is why this session could not):

```bash
sudo sqlite3 /var/lib/rancher/k3s/server/db/state.db \
  'PRAGMA journal_mode; PRAGMA synchronous; PRAGMA busy_timeout; PRAGMA page_size; PRAGMA wal_autocheckpoint;'
```

Then validate a candidate DSN somewhere disposable — a VM is fine here, no `/dev/dri` involved — before it goes in `/etc/rancher/k3s/config.yaml` as `datastore-endpoint`.

Durability call: this host is a workstation running experiments, and the datastore holds claims, leases and events rather than user data. `NORMAL` plus a nightly `.backup` is a defensible trade. Say so in the config comment so the next person does not have to rediscover it.

### L4 — Stop making the filesystem do work nobody asked for

| Change | Removes | Where |
|---|---|---|
| Warm pool follows demand instead of a static 20 | 20 pod creations per scale event, with nothing to serve them | `SandboxWarmPool.spec.replicas` or the autoscaler's floor |
| Claim reuse across sessions | claim create + delete = 2 of 5 session writes, and no pod churn | `runtime/session`, `cmd/pool-autoscaler` |
| `discard=async` → `nodiscard` + `fstrim.timer` | **94 discards/s** measured during churn | `/etc/fstab`, `systemctl enable --now fstrim.timer` |
| Stop per-pod Events for the sandbox namespace | 184 slow event writes/24 h | `--kubelet-arg=event-qps=1`, controller |

The first two are the same change seen from two sides: a session that adopts a warm browser touches no layer, no discard and no claim.

### L5 — Cut what each session costs in writes

Five is the current floor: claim create, status, event, claim delete, cilium endpoint. Three are removable without touching the datastore:

- **claim reuse** — no create, no delete (also L4).
- **one `Patch` for status** instead of create-then-update.
- **no per-pod Event** for the sandbox namespace.

5 → 3 writes per session is a **1.67× multiplier on whatever rate the writer tolerates**, on any backend, at no infrastructure cost. It is the cheapest sessions/s in this document.

### L6 — The infra write rate that is independent of sessions

Eleven `cilium-l2announce-*` leases renew every 12 s forever: **0.91 writes/s, 78 900/day**, 87 % of all slow lease writes, and the reason `cilium-operator` has restarted 117 times. Each one is a LeaderElection lease per LoadBalancer service, emitted by Cilium's L2 announcer so it can own the ARP replies for that IP.

Consolidate the eleven LoadBalancer services behind one address (an ingress, or a shared L2 policy), or scope `CiliumL2AnnouncementPolicy` to the services that actually need L2 rather than ingress-level access. Each service removed deletes 0.08 writes/s permanently and removes one lease that can be lost during a stall.

### L7 — Change engine, if L1–L6 are not enough

| Option | Effect | Cost |
|---|---|---|
| External etcd on a second host | removes kine's 14.3 writes/s, the SQLite lock and the compaction scan from this SSD — but not the container churn, which still commits `sda3` | a machine, ~1–2 GiB there, cluster re-init |
| kine over Postgres/MySQL on a second host | same lock removal, plus a network round trip per commit; compaction still scans SQL, on a server not also serving Chromium layers | a database to run |
| Embedded etcd (`--cluster-init`) on this disk | **expect worse**: etcd fsyncs per raft entry, where kine's single writer already batches to a p50 of 1 ms | nothing, and it costs a GiB here |

---

## 4. Expected sessions/s, and the cap nobody escapes

Derived from the budget in §2, not from vendor claims. Each row is the increment expected *on top of the row above*.

| Step | Removes | Expected sessions/s | Confidence |
|---|---|---|---|
| today | — | **fails at ~2, and fails at 0 with churn** | measured |
| L2 dirty window | the length of every commit | 2.0 → 2.5 | medium, one sysctl |
| L5 + L4 writes per session | 5 → 3 writes/session, no pod churn | 2.5 → 3.0 | high, arithmetic |
| L6 L2 announcements | 0.91 writes/s, 87 % of slow lease writes, operator restarts | 3.0 → 3.2 | high, config only |
| L3 busy timeout | the `database is locked` class | 3.2 → 3.5, at a higher p99 | medium |
| L1b `nodatacow` | COW and zstd work in the commit path | 3.5 → 3.7 | low |
| L1a separate SSD | the shared commit domain | 3.7 → 5+ | high, needs hardware |
| L7 external etcd | kine entirely | as L1a, without owning a disk | high, needs a host |

Then this stops, and not because of the datastore:

```
CPU    2.0 core-seconds per session (note 18) over ~2.7 free cores  = 1.35 sessions/s, heavy pages
slots  168 contexts / 75 s mean hold                                = 2.24 sessions/s
memory ~38 browsers × 8 contexts at 360 MiB each                    = ~304 concurrent
pods   110 − 44 system ÷ 1 browser                                  = 66 browsers
```

**A datastore fix buys stability at 2.2 sessions/s, not a rate beyond it.** Past that the wall is cores and slots, and the next piece of work is either more cores or lighter pages — which is what the CPU study was for. Anyone reading the table above as "5 sessions/s is available" is reading the wrong column.

---

## 5. A 24 h soak, and what breaks first

Only after §6 steps 1–5. Abort thresholds, checked every 60 s:

| Order | Bottleneck | Signal | Abort if |
|---|---|---|---|
| 1 | k3s exits | `journalctl -u k3s \| grep -c level=fatal` | ≥ 1 per hour |
| 2 | leases missed | `leader election lost`, controller restartCount delta | any |
| 3 | compaction on a grown log | `name=Compact` count and duration | any > 5 s |
| 4 | device saturation | `io.pressure full`, device busy | full > 50 % sustained 5 min |
| 5 | disk | `df -h /` | < 10 GB free |

Row 3 is the one that kills long runs, and the arithmetic is unforgiving: a 1000-revision compaction window at 10 writes/s closes every 100 s, so a 24 h soak at 2 sessions/s sees **864 compactions against the 76 of the last 24 h** — 18 % of which exceed 1 s at a p50 of 2.34 s. That is ~156 multi-second write-lock windows per day, up from 14.

```bash
while true; do
  sleep 60
  f=$(journalctl -u k3s --since "-60 s" | grep -c 'level=fatal')
  [ "$f" -gt 0 ] && { echo "ABORT: control plane restarted"; break; }
  printf '%s slow=%s compact_max=%s io_full=%s\n' "$(date -Is)" \
    "$(journalctl -u k3s --since "-60 s" | grep -c 'Slow SQL')" \
    "$(journalctl -u k3s --since "-60 s" | grep 'name=Compact' | sed -E 's/.*duration=([0-9.]+)s.*/\1/' | sort -gr | head -1)" \
    "$(awk '/^full/{print $2}' /proc/pressure/io)"
done
```

---

## 6. What to measure next, in this order

1. **Root reads, five minutes.** `sudo sqlite3 … 'PRAGMA …'` from §3 L3, plus `sudo btrfs filesystem usage /` and `sudo ls -l /var/lib/rancher/k3s/server/db/` for `state.db` and `-wal` size. This session could not, and it is the input to L3 and L1b.
2. **Baseline the commit tail** at the current warm pool of 20, 10 minutes, `commit_stats` and `/proc/pressure/io` each second. Deliverable: mean and max commit ms under known load.
3. **The fsync control.** 300 × 4 KiB `oflag=dsync` writes to a file on the same subvolume, not kine's, timestamped. Same tail as kine → filesystem; sub-millisecond → SQLite lock, and L3/L7 move up the list.
4. **L2, alone.** Apply the three sysctls, repeat step 2, compare mean commit ms over a fixed 600 s window. One variable.
5. **L5 + L4, alone.** Claim reuse and the demand-following warm pool, then repeat step 2 and count pod creations per 10 minutes. This is the test that confirms or kills "pod churn drives the storms".
6. **The ladder**, `experiments-c4h`: 0.5, 1, 2, 4 sessions/s, 10 minutes each, kine metrics live, `RIOT_RATE` and `--boot-concurrency` as in the folder README.
7. **The 24 h soak**, at the highest rung that produced no fatals.

---

## 7. Corrections to 04 and note 18

1. The 39.7 s worst event is kine's compaction `DELETE`, not a session commit — `04` reads as though it were an insert.
2. The failure is fatal, not slow: 9 k3s starts in 24 h. "kine refuses writes" understates it by a control-plane restart.
3. 87 % of slow lease writes are Cilium's L2 announcer — 1 424 of 1 645 — not the sandbox controller or the scheduler.
4. Free disk ranks last, not first. The metadata and data chunk counters show no starvation: 4.33 of 8 GiB metadata used, 9.9 GB free inside allocated data chunks. The docs' 81 % figure is also now 79–80 %.
5. The "10.4 GiB available, CPU requests 24 %" snapshot was a calm minute. During a storm this session saw 243 MiB free, load 27, 1.87 GiB of zram in use, and `io.pressure full` at 42 %. The stable fact is `cpu.pressure full = 0.00 %`, which is the whole argument for §4.
6. The swap in `02` is `/dev/zram0`, compressed RAM, not a swapfile on `/dev/sda3`. It costs RAM and CPU, not disk IO, so "fsync contention with swap" is not a mechanism on this host.

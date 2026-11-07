# 03 — Host 16GB zstd zram Upgrade, Benchmark Matrix, and VM Swap Thrashing Analysis

| | |
|---|---|
| Status | Measured & Implemented |
| Date | 2026-11-07 |
| Component | Platform memory, zram swap, sandbox templates |
| Related | [01 attribution across classes](01-attribution-across-classes.md), [vm-swap-lab README](../../config/vm-swap-lab/README.md) |

---

## 1. What Changed

1. **Host zram Reconfigured (16 GiB, `zstd`)**:
   - `/etc/systemd/zram-generator.conf` updated to:
     ```ini
     [zram0]
     zram-size = 16384
     compression-algorithm = zstd
     ```
   - Automated via [scripts/reconfigure-zram.sh](../../scripts/reconfigure-zram.sh), which executes `swapoff /dev/zram0` and writes `echo 1 > /sys/block/zram0/reset` to avoid kernel `-EBUSY` when altering `comp_algorithm`.
   - Verified via `zramctl`: ALGORITHM `zstd`, DISKSIZE `16G`.

2. **Sandbox Chart Updated to `zram-5to1`**:
   - [config/sandbox/values.yaml](../../config/sandbox/values.yaml): Template `browser-tpl` and `browser-tpl-gpu` updated from `spill: zram-2to1` to `spill: zram-5to1`.
   - Applied to the cluster: `helm upgrade sandbox config/sandbox -n agent-sandbox-system` (Revision 17).
   - Live `SandboxTemplate` now carries `session-lab.io/spill: "zram-5to1"`.

3. **Cgroup Allowance Doubled**:
   - Under Kubelet `LimitedSwap`, container swap is calculated as `(request / node allocatable) * node swap`.
   - Doubling host zram from 8 GiB to 16 GiB automatically scales the container swap ceiling from ~66 MiB to ~132 MiB.
   - Per-pod allowances can be manually scaled via `scripts/swap-allowance.sh --bytes <bytes> --tier zram-5to1`.

---

## 2. Harder Benchmark Suite (Melting-Point Commands)

| Test Target | Lever | Command |
| :--- | :--- | :--- |
| **Control Plane API Stress** | High Arrival Rate (`--rate 35`) | `SESSION_LAB_POOL=browser-pool-gpu ./scripts/session-lab-experiment.sh --layer session --sessions 1500 --rate 35 --hold 120s --boot-concurrency 10 --fixed-arrival --sample-seconds 1` |
| **Deep Queue Saturation** | Massive Session Pool (`--sessions 3500`) | `SESSION_LAB_POOL=browser-pool-gpu ./scripts/session-lab-experiment.sh --layer session --sessions 3500 --rate 10 --hold 180s --boot-concurrency 10 --fixed-arrival --sample-seconds 1` |
| **Lifecycle / Cleanup Churn** | Mixed Turnaround (`--mix 30s:50,2m:30,5m:20`) | `SESSION_LAB_POOL=browser-pool-gpu ./scripts/session-lab-experiment.sh --layer session --sessions 2000 --rate 15 --mix 30s:50,2m:30,5m:20 --boot-concurrency 10 --fixed-arrival --sample-seconds 1` |
| **Scheduler Contention** | Unbounded Boot (`--boot-concurrency 0`) | `SESSION_LAB_POOL=browser-pool-gpu ./scripts/session-lab-experiment.sh --layer session --sessions 1200 --rate 15 --hold 120s --boot-concurrency 0 --fixed-arrival --sample-seconds 1` |
| **All-in-One Melter** | Rate 35, 3k sessions, mix, boot 0 | `SESSION_LAB_POOL=browser-pool-gpu ./scripts/session-lab-experiment.sh --layer session --sessions 3000 --rate 35 --mix 30s:50,2m:30,5m:20 --boot-concurrency 0 --fixed-arrival --sample-seconds 1` |

---

## 3. Why the VM Swap Ladder (Rungs 5, 6, 7, 8) Overwhelmed the Server

The VM swap experiments in `config/vm-swap-lab/capacity.sh` caused severe server distress and lockups that the bare-metal session runs did not produce. Three core mechanisms explain the contrast:

### 1. Dedicated Pods vs Context Multiplexing (`--layer infra` vs `--layer session`)
- In `config/vm-swap-lab/capacity.sh`, each session ran on `--layer infra`: **1 session = 1 full Kubernetes Pod = 1 full Chromium browser instance** with forwarder sidecars and cgroups.
- In `session-lab-experiment.sh --layer session`, sessions are multiplexed into **8 contexts per Chromium instance**. 1,200 sessions need only ~150 browser processes, whereas 8 concurrent sessions in the VM spawned 8 full browser processes on a constrained node.

### 2. Physical Host Headroom vs 3 GiB Hard Boundary
- The VM swap lab ran in a virtual machine restricted to **4 vCPUs and 3–4 GiB RAM**.
- The control plane (k3s, kubelet, containerd) consumes ~800 MiB base memory.
- A single browser instance consumes ~250–350 MiB resident memory.
- At Rung 6–8 (6 to 8 full browser pods), memory demand hits 6–8 × 350 MiB = ~2.1–2.8 GiB. Combined with the 0.8 GiB platform overhead, physical RAM is **100 % exhausted**.

### 3. Lack of `memory.swap.min` and Kernel Thrashing
- Linux cgroups v2 does **not** support `memory.swap.min`. The kernel cannot protect the platform control slice from eviction.
- Under heavy anonymous memory pressure, kswapd swaps out `kube-apiserver`, `etcd`/SQLite, and `containerd` pages into swap/zram.
- Every API call and context switch incurs page faults and direct reclaim stalls.

### 4. Rung Results: Why 5:1 (zstd) Broke at 7/8
- **Rung 5**: Held by all arms (2:1, 3:1, 5:1).
- **Rung 6**: The constraint was zram device space. At 2:1 (`lz4`), the fixed 512 MiB device ran out of space, incurring 39.4 s queue p95 stalls. At 5:1 (`zstd`), pages compressed 5.3:1 into memory without space exhaustion, yielding **740 ms** p95 latency.
- **Rung 7**: The bottleneck shifted from **memory space to CPU throughput**. Decompressing `zstd` under constant page faulting saturated all CPU cores. `kube-apiserver` starved of CPU cycles and timed out (`API Delete timeout`, 7 failed). `lz4` held 31/31 sessions because its decompression CPU overhead is negligible.
- **Rung 8**: Two separate failures collided:
  1. **Harness Watch Bug**: Client watch issued `ResourceVersionMatch: NotOlderThan` without `SendInitialEvents` (`watch.go:116`), triggering `resourceVersionMatch: Forbidden` when claims stacked up.
  2. **Total Node Freeze**: Memory dropped to `MemAvailable 21 MiB`, and `kube-apiserver` dropped connections with `TLS handshake timeout` and `connection refused`.

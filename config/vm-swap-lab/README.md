# vm-swap-lab — a throwaway k3s node for the swap A/B

One QEMU VM, one k3s node, one Chromium sandbox pool. Nothing here touches the
host's cluster: no `/etc/rancher/k3s`, no kubelet restart on the live node, no
sudo. `down.sh` deletes the VM and the disk it ran on.

Why a VM at all: `memorySwap.swapBehavior` is a kubelet setting, and the host's
sandbox pods run with `restartPolicy: Never` — flipping the live node's kubelet
restarts containerd and kills them. Inside a VM the same change costs one
`systemctl restart k3s` and nothing else.

## What it does

| Step | Script | Result |
|---|---|---|
| Boot | `up.sh` | Ubuntu 24.04 + k3s + **swap backing chosen by `SWAP_MODE`** (`none` / `zram` with `ZRAM_ALGO`, `ZRAM_MB` / `disk` with `SWAP_MB`), kubelet drop-in at `NoSwap` |
| Verify | `verify-swap.sh` | backing mode, device, achieved zram ratio; `/configz` shows the running kubelet settings; pod slices show `memory.swap.max` |
| Install | `install-sandbox.sh` | agent-sandbox v1.0.7 (digest-verified), `sandbox-demo`, one template, one warm pool of 4 |
| Run | `run-ab.sh` | run A with `NoSwap`, run B with `LimitedSwap`, identical workload, both reports copied back |
| Capacity | `capacity.sh` | ladder: replicas 4→6, one row per rung per arm, stops at the first rung that fails |
| Compare | `compare.sh` | the table below, from the two run reports |
| Teardown | `down.sh [--purge]` | VM killed, disk/seed/logs removed |

Swap sizing rule: **never `ram/2`**. A 1.5 GiB zram device on a 3 GiB node takes that
RAM away from the pods it is meant to help; the default is `ZRAM_MB=768` (25 %).

## Run it

```bash
cd experiments/config/vm-swap-lab
./up.sh                 # boots the VM, waits for the node
./verify-swap.sh        # NoSwap at this point: pods must show memory.swap.max=0
./install-sandbox.sh    # agent-sandbox + browser-tpl + browser-pool (4 replicas)
./run-ab.sh             # run A (NoSwap) then run B (LimitedSwap)
./compare.sh            # the comparison table
./down.sh               # afterwards; --purge also drops the base image and key
```

`run-ab.sh` takes `DURATION` (default `10m`), `RATE` (`0.05`), `HOLD` (`30s`),
`REPLICAS` (`4`) and `SAMPLE_SECONDS` (`5`) from the environment; both runs always
use the same values.

## What is measured, and where it comes from

Every row is session-lab's existing per-session measurement, read from each pod's
cgroup slice on the VM:

| Row | Source |
|---|---|
| Startup p50/p95 | `acquire_ms` — arrival → sandbox Ready |
| Queue wait p50/p95 | `queue_wait_ms` — claim created → Ready |
| Execution p50/p95 | `hold_ms` — the hold the session asked for and how long it took |
| Peak RAM | `memory.current` peak per session (`mem_peak_*_mib`) |
| Swap peak | `memory.swap.current` peak per session (`swap_peak_*_mib`) |
| Throttling | `cpu.stat` `nr_throttled`/`throttled_usec` > 0 |
| OOM kills | `memory.events` `oom_kill` |
| Sessions/min | recorded sessions ÷ run window |
| Node memory / swap | `/proc/meminfo` samples from the guest |

## The two settings, exactly

The kubelet config is the **same** in both runs —
`/var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-swap.conf`:

```yaml
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
failSwapOn: false
memorySwap:
  swapBehavior: LimitedSwap
```

`memorySwap` is a `KubeletConfiguration` field and has no kubelet flag, which is
why it is a drop-in file and not another `--kubelet-arg`. The template's `256Mi`
request / `768Mi` limit is what makes the pod Burstable; under `LimitedSwap` only
Burstable pods are considered at all.

What varies between runs is one cgroup value, set by `swap-limit.sh`:

| Run | pod slice + container scopes |
|---|---|
| A (`swap-off`) | `memory.swap.max = 0` |
| B (`swap-on`) | `memory.swap.max = $SWAP_ALLOWANCE` (default `max`, i.e. the 2 GiB zram) |

Both levels are written because a child cgroup cannot exceed its parent: a
`0` on the browser container blocks swap even when the pod slice says `max`.

### What Kubernetes actually grants, measured here

k3s v1.36.5, node with 3915 MiB allocatable and 2047 MiB zram, template request
256Mi / limit 768Mi (Burstable):

| kubelet setting | browser container `memory.swap.max` |
|---|---|
| `LimitedSwap` | **133.9 MiB** — `(256/3915) × 2047`, the documented formula, written at the *container* scope |
| `NoSwap` | **`max`** — kubelet writes no limit at all, so the cgroup default applies: unconstrained, not forbidden |

So `NoSwap` is not a no-swap baseline, and `LimitedSwap` (≈134 MiB here) is a
small, request-proportional allowance. That is why the experiment varies the
cgroup value itself rather than the kubelet flag:

| Run | what is set | what it means |
|---|---|---|
| A (`swap-off`) | `memory.swap.max = 0` on pod slice **and** container scopes | swap forbidden, whatever kubelet granted |
| B (`swap-on`) | `kubelet` (default): leave kubelet's 133.9 MiB | the native Kubernetes behaviour, unfaked |
| B (`SWAP_ALLOWANCE=max` / bytes) | explicit value on slice and scopes | a larger, chosen allowance |

Both levels are written because a child cgroup cannot exceed its parent: a `0`
on the browser container blocks swap even when the pod slice says `max`.

**Trap worth knowing:** these values are written when a pod is *created*.
A reading taken on pods that predate a kubelet restart shows the old state, which
is exactly how an early check here "measured" a 0-byte allowance. `verify-swap.sh`
is only meaningful against pods the current kubelet started — the pool is
refilled before each run for that reason.



## Making pressure deterministic instead of hoping for it

Per-pod swap is not triggered by a pod reaching its own `memory.max`; the kernel
reclaims anon pages when the **node** is short. So an arm only exercises swap if
the box is genuinely too small for the workload — and "raise the replica count
until something breaks" makes that a matter of luck and extra debugging rounds.

Shrink the box instead. Same template, same image, same replica count, same
mix — only `MEM_MB` changes, so the comparison stays a comparison and the
pressure becomes a property of the configuration:

| `MEM_MB` | allocatable | zram (`ram/2`) | 4 browsers ≈ 1.2 GiB + platform ≈ 0.8 GiB | expected |
|---|---|---|---|---|
| 4096 | 3915 MiB | 2047 MiB | 2.0 GiB | comfortable; **measured** `MemAvailable` floor 502 MiB — tight, but sandboxes never swapped |
| 2048 | ~1900 MiB | ~1024 MiB | 2.0 GiB | **over**: ~100–300 MiB short → arm A must fail, arm B has ~550 MiB of swap to lean on |
| 1536 | ~1400 MiB | ~768 MiB | 2.0 GiB | clearly over: expect OOM kills in arm A, swap-in latency in arm B |

The 4 GiB row is not a guess: with 4 replicas the last run recorded
`MemAvailable` bottoming at 502 MiB of 3916 (12.8 %), so that box is already at
the edge while the sandbox cgroups were forbidden to swap. Read arm B there
first — if `swap_peak_max_mib > 0` the effect is visible without rebuilding.

Rebuilding at a smaller size (throws the cluster away, ~5 minutes):

```bash
cd experiments/config/vm-swap-lab
./down.sh                                   # VM + disk gone, base image kept
MEM_MB=2048 ./up.sh                         # zram becomes ram/2 = 1024 MiB
./install-sandbox.sh
./run-ab.sh                                 # both arms on the small box
```

What "it worked" looks like, per arm: `resources.oom_kills`, `outcomes` with
`acquisition_failed`, and the `first_boundary` line in the report. Arm A is
*supposed* to break first; an arm A that survives as comfortably as arm B means
the box is still too big, so drop `MEM_MB` another step rather than adding
replicas.



- One node: both runs happen on the same machine, sequentially, so the pools and
  the node are identical between runs and only the kubelet setting differs.
- The VM is capped at 4 GiB and 4 replicas. It measures *whether swap changes
  these timings*, not the maximum density of the host.
- Without `/dev/kvm` the VM runs under emulation and every latency is inflated;
  `up.sh` prints a warning in that case, and the numbers should not be compared
  to anything measured on real hardware.
- `swap_peak_*_mib` is `omitempty` in the report: `0` in run A is the expected
  result (no swap available), not a missing measurement. `verify-swap.sh` is what
  distinguishes the two.

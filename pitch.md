# The pitch

One host. 60 vCPU-seconds of work in flight per second, 168 browser contexts
live at once, 42 sessions confirmed running at overload — and the reason the
number is 42 is a database commit, not a browser.

This file is the short version of `ROADMAP.md` and `research/`: what the thing
is, how much it holds, and which wall stops it. Every figure carries its label
(`[M]` measured, `[I]` inferred, `[U]` unknown) and the note it comes from.

## 1. What it is

```text
              Fedora 44 · Intel i5-9400F (6 threads) · 15 GiB RAM + 8 GiB zram
                        AMD RX 570 8 GB (also drives the desktop) · node 192.168.0.28
 ┌──────────────────────────────────────────────────────────────────────────────────────┐
 │  native k3s v1.36 · Cilium 1.20 only: eBPF, kube-proxy off, ingress + LB-IPAM .240-.250│
 └──────────────────────────────────────────────────────────────────────────────────────┘
      │                  │                     │                    │              │
 ┌────────────┐   ┌─────────────┐      ┌──────────────┐    ┌────────────┐  ┌───────────┐
 │agent-      │   │ session-lab │      │sandbox-      │    │observability│  │ platform  │
 │sandbox     │   │             │      │console       │    │             │  │           │
 │v1.0.7      │   │ runner      │      │.250          │    │Victoria     │  │Argo CD .247│
 │4 CRDs +    │   │ pool        │      │run + wall    │    │Metrics .246 │  │Kargo .249 │
 │controller  │   │ autoscaler  │      │views         │    │Grafana .245 │  │Neo4j .242 │
 │router +    │   │ slots       │      │tap = 1       │    │Hubble .241  │  │assistant  │
 │warm pool   │   │ CDP client  │      │screenshot    │    │             │  │.243       │
 │Chromium+CDP│   │ store, KPIs │      │traffic, cmp  │    │             │  │15 tools   │
 └────────────┘   └─────────────┘      └──────────────┘    └────────────┘  └───────────┘
```

A session is not a pod. One Chromium carries up to eight browser contexts; a
**slot** is `(instance, browserContextId)` and one slot serves one session. That
is what turns one node into 168 concurrent sessions instead of 21 pods.

## 2. How much it holds

```text
 1 pod ──► 1 Chromium ──► 8 contexts ──► 8 slots ──► 8 sessions
   │           │
   │           └── 559 MiB RSS  [M]                      1 GPU render node, shared
   └── 154 MiB p50 peak / 168 MiB working-set per session [M]
       template: requests 100m CPU, limits 768Mi, no CPU quota [M]

 the three walls, as measured
 ┌─────────────────────────────┬───────────────────────────┬──────────────────────────────┐
 │ scheduler / API server      │ warm pool                 │ browser contexts             │
 │ 60 offered → 42 ran         │ 2 → 22 → 2 replicas       │ 168 contexts                 │
 │ 17 refused, queue p95 134 s │ 84.9 s to scale out       │ ~8 per Chromium              │
 │ memory 20.9 % free at peak  │ ceil 120 pods configured  │ 559 MiB per browser          │
 └─────────────────────────────┴───────────────────────────┴──────────────────────────────┘
```

**Throughput is concurrency ÷ hold, not a session count.** At the measured
concurrency and the 30 s hold the runs used:

| Basis | Concurrency | Hold | Sessions/s | Sessions/hour | Sessions/day |
|---|---|---|---|---|---|
| Measured overload point **[M]** | 42 | 30 s | 1.40 | 5 040 | **≈ 121 000** **[I]** |
| Slot ceiling **[M]** 168 contexts | 168 | 30 s | 5.60 | 20 160 | **≈ 484 000** **[I]** |

The daily column is arithmetic on measured inputs `[I]`, not a soak result: no
run here has produced a 24-hour artefact. The defensible claim is "**42
concurrent, 168 slots, 5 000 sessions/hour measured at overload, and the daily
figure is that rate held**".

## 3. Where the CPU goes, including the GPU

Acceleration is measured, and it pays in one place only: **per captured frame.**

```text
 30 concurrent sessions, sleep-hold workload (what session-lab runs)
   CPU-only Chromium   1.58 cores of 6   ┐
   RADV-accelerated    1.53 cores of 6   ┴─► no change. The hold never asks for a frame.

 forced render loop, 40 Page.captureScreenshot per session
   CPU-only Chromium   2.13 cores/browser   screenshot p50 244 ms
   RADV-accelerated    1.57 cores/browser   screenshot p50 279 ms
                       −26 % CPU            +14 % latency        [M] note 19 §1

 LLM inference on the same card, Vulkan/RADV, Qwen2.5-1.5B, 64-token answer
   CPU  ≈4 410 ms      GPU ≈806 ms      ≈6× faster, host RSS ≈4.4× lower  [M] e3 RESULTS
```

So the pitch is precise rather than generous: **the GPU buys CPU per frame and
per token, not sessions per node.** A screenshot-heavy or VLM workload gets a
quarter of its browser CPU back; a page-holding workload gets nothing, because
there is nothing to offload. `gpu_compositing` stays `disabled_software` in both
arms — headless Chromium has no display surface to composite **[M]**.

## 4. What the cheap boxes do for it

| Lever | Measured effect | Note |
|---|---|---|
| Lifecycle policy | reuse resets a session in **35 ms**; destroy pays **374 ms + cold start** | 09 |
| Lifecycle + autoscaling together | queue wait p50/p95 **3.2 s / 134 s → 1.4 s / 54 s**, and the 40-replica ceiling is reached | 09 |
| CPU requests, not limits | **248 of 360** sessions were CFS-throttled by a 1000m quota, which made hold latency an artefact of the quota | 12 |
| zram swap | **5 → 6** concurrent sessions, **+20 %**, at 3 vCPU / 4 GiB; **~70 %** better tail latency at equal load; lz4 holds the highest rung, zstd is 30–50× better on p95 at moderate overload | swap study §5b, §5d.1 |
| kine DSN | insert mean **64.6 → 2.06 ms**, **823 → 0** Slow SQL/hour, **9 → 0** fatals/24 h | cpu-oversubscription §8 |

The last row is the one that matters most for a pitch about scale: the platform's
ceiling is **the SQLite commit under the API server**, not Chromium. kine takes
1–3 s per commit under write pressure, and that surfaces as an API `Get`/`Delete`
timeout in the runner — which is what "the 17 refusals" actually were.

## 5. What it is not

- **Not highly available.** One host, one node, no second control plane. If it
  reboots, everything is gone and rebuilt.
- **Not public, not TLS, not authenticated.** Anonymous read-only on a LAN is a
  lab posture, not a security model.
- **Not GPU-compute.** Vulkan/RADV on an unsupported Polaris card, no ROCm, no
  CUDA, no VRAM enforcement — and the card is also this workstation's display.
- **Not measured over a day.** Every number above is a run of minutes. The daily
  column is arithmetic and is labelled as such.

## 6. What would make it 10×

1. **Move the control plane off single-node SQLite.** kine is the ceiling; etcd or
   a real datastore buys more than any browser tuning.
2. **Put the GPU to work on the workloads that use it.** Capture-heavy sessions
   gain 26 % CPU per browser; the current workload does not ask.
3. **Share the card properly.** One device, one accelerated browser at a time
   today; Mesa-level sharing across containers is untested **[U]**.
4. **Store the slot id.** The wall binds slots to sessions *by time* above one
   context per browser; a stored id makes the measurement exact.

## 7. Re-deriving every number

```bash
# the overload run behind "42 of 60"
grep -n "42 success\|queue wait" research/session-lifecycle-elasticity/07-overload-and-suspend.md

# the slot ceiling and the pod/context split
sed -n '1,60p' research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md

# GPU: the A/B and the render-loop counter-case
sed -n '25,40p' research/session-lifecycle-elasticity/19-gpu-pool-a-b.md

# swap: capacity and the ratio verdict
sed -n '120,180p' research/swap-capacity/README.md

# throughput arithmetic: concurrency ÷ hold
awk 'BEGIN{printf "42/30*3600/1000 = %.2f k sessions/hour\n", 42/30*3600/1000}'
```

Where this file and a note disagree, **the note wins** and this file is wrong.

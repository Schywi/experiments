---
title: 'The pitch'
kind: index
---

# The pitch

Registered data only. Every block below is quoted verbatim from a note in this
repository, with its source and line range underneath. Nothing here is computed,
summarised or interpreted — the narrative is written by the author in §11.

## 0. What we set out to do

> in resume we tried to scale Agentsandbox claim/sandbox pool past kubernetes
> cold start and allocation aiming for the maximum amount of served users
> possible

Cold boot **~40 s** · warm adoption **107 ms** · resume **p50 2853 ms** — [08.2 §6](research/session-lifecycle-elasticity/08.2-gpu-offload-feasibility.md), [07](research/session-lifecycle-elasticity/07-overload-and-suspend.md) lines 97–98.

What a claim bought at that point: agent-sandbox `v1.0.7` (pinned release, sha256-checked), a router + browser template + warm pool from `config/sandbox/`, and a Chromium-with-CDP image from `apps/browser-sandbox/` — [evolution/07](evolution/07-sandbox-console.md) lines 40–42.

## 1. What it is

```text
              Fedora 44 · Intel i5-9400F (6 threads) · 15 GiB RAM + 16 GiB zram
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

Source: [`ROADMAP.md`](ROADMAP.md) §0 and §3, [`README.md`](README.md).

## 2. Two narratives

The same host runs two stories, and they share the machine. Pick one:

<div class="tabs narrative">
<input type="radio" name="narrative" id="n-sre" checked>
<input type="radio" name="narrative" id="n-chromium">
<label for="n-sre">The SRE assistant</label>
<label for="n-chromium">The Elastic Chromium</label>

<section>

<p>An operations assistant over the live cluster: 15 tools, one evaluator call that reads the whole sentence and returns the task list, and every answer carrying its evidence — tool, arguments, time, source, a copyable command or PromQL, and a Grafana or Hubble deep link where the backend supports one.</p>

<p>Contract and reasoning: <a href="research/features/README.md">feature contracts</a> · <a href="research/assistant/README.md">assistant notes</a> · <a href="evolution/05-ai-ops-track-d.md">phase 5 narrative</a></p>

<div class="shots-wrap slides-3">
<button class="shots-nav prev" type="button" aria-label="Previous slide">‹</button>
<button class="shots-nav next" type="button" aria-label="Next slide">›</button>
<div class="shots shots--auto">
<div class="track">

<figure>
<img src="evolution/assets/sre-assistant-answer.png" alt="The assistant answering an operations question with the evidence panel open" loading="lazy">
<figcaption>An answer with its evidence panel — the pane the assistant returns alongside the text</figcaption>
</figure>

<figure>
<img src="research/features/assets/deep-links-kargo.png" alt="An answer about the Kargo promotion carrying evidence and deep links" loading="lazy">
<figcaption>An answer about the Kargo promotion, carrying its deep links</figcaption>
</figure>

<figure>
<img src="research/features/assets/assistant-requests-browser-screenshot.png" alt="The assistant asking the browser for a screenshot of a claim" loading="lazy">
<figcaption>The assistant asking a sandbox for a screenshot, and getting one back</figcaption>
</figure>

</div>
</div>
</div>

</section>

<section>

<p>A claim becomes a pod, a pod runs one Chromium, and one Chromium carries up to eight browser contexts — so a session is a slot, not a pod. The workload is driven by the runner, sized by an autoscaler that reads claim demand and node pressure together, and bounded by the control plane underneath it.</p>

<p>Design notes: <a href="research/session-lifecycle-elasticity/README.md">session lifecycle elasticity</a> · <a href="evolution/08-session-lifecycle-elasticity.md">phase 8</a> · <a href="research/session-lifecycle-elasticity/16.2-session-wall-ui.md">the console</a></p>

<div class="shots-wrap slides-6">
<button class="shots-nav prev" type="button" aria-label="Previous slide">‹</button>
<button class="shots-nav next" type="button" aria-label="Next slide">›</button>
<div class="shots shots--auto">
<div class="track">

<figure>
<img src="research/session-lifecycle-elasticity/assets/console-initial-about-blank.png" alt="The console on a fresh claim with the page at about:blank" loading="lazy">
<figcaption>A fresh claim in the console — the page is at about:blank</figcaption>
</figure>

<figure>
<img src="research/session-lifecycle-elasticity/assets/cdp-frame-from-claim.png" alt="A CDP frame booting from a claim" loading="lazy">
<figcaption>A frame booting from a claim, driven over CDP</figcaption>
</figure>

<figure>
<img src="research/features/assets/riot-claim-interaction.png" alt="A scripted agent driving a Riot page inside a claim" loading="lazy">
<figcaption>An agent driving a Riot page inside a claim</figcaption>
</figure>

<figure>
<img src="research/session-lifecycle-elasticity/assets/console-claim-leakage-pending-requests.png" alt="The console showing leaked claims as pending requests" loading="lazy">
<figcaption>Leaked claims visible as pending requests in the console</figcaption>
</figure>

<figure>
<img src="research/session-lifecycle-elasticity/assets/low-resource-run.png" alt="The same session route under the reduced resource template" loading="lazy">
<figcaption>The same route under the reduced resource template</figcaption>
</figure>

<figure>
<img src="research/session-lifecycle-elasticity/assets/keda-first-scale-out.png" alt="First KEDA scale-out test of the deployed autoscaler" loading="lazy">
<figcaption>The first scale-out test of the deployed autoscaler</figcaption>
</figure>

</div>
</div>
</div>

</section>
</div>

<script src="./static/carousel.js" defer></script>

Deep dives, one per mechanism, each behind a dropdown. Every claim inside carries the
file and line it can be checked against.

> [!note]- The two layers: a pod is not a session
> The 10-minute baseline used to mean one thing. It now means two, and they must not be compared
> ([16.1](research/session-lifecycle-elasticity/16.1-browser-contexts-and-slots.md) §1).
>
> ```text
>  claim ──► 1 pod ──► 1 Chromium ──► 8 browser contexts ──► 8 slots
>            infra layer            session layer
>            1 pod per session      8 contexts per pod
>                                                │
>                                     slot = (instance, browserContextId)
>                                                │
>                                     ceiling: 352 slots, 306 concurrent served
> ```
>
> A slot is the pair `(instance, browserContextId)`; one slot serves one session. The ceiling
> is the one in the [capacity ledger](research/session-lifecycle-elasticity/24-capacity-ledger.md):
> **44 browsers × 8 = 352 slots, peak concurrency 306, 2986 of 3000 sessions successful** on
> `20261107T235847Z` at `requests.cpu: 10m`. The 168 in
> [18](research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) line 62 is the
> same arithmetic at `100m` requests, where the scheduler bound at ~21 browsers; at 10m it does
> not bind. That run's constraint was the API server refusing claim creation, not slots.

> [!note]- The concurrency model: three classes, one queue, one scheduler
> Quoted from [15](research/session-lifecycle-elasticity/15-capacity-plane-and-demand-plane.md) §2:
>
> > | Class | Queue position | Triggers new capacity | Interruptible |
> > |---|---|---|---|
> > | `normal` | first | yes | no |
> > | `batch` | after normal | yes, only after the deficit survives one autoscaler interval | no |
> > | `bulk` | last, may starve | **never** | yes — suspend/resume, not kill |
>
> "No caps. Nothing is rejected by us; rejection stays whatever the platform does
> (`Insufficient cpu`), recorded as a boundary event." Two things are listed as deliberately
> absent: preemption between classes, and "Any second scheduler process. One scheduler owns the
> queue, as today" (line 198).

> [!note]- The autoscaler loop: demand and pressure, with hysteresis
> Two inputs are read every reconcile: claim demand and the node's own load
> ([`autoscale.go`](apps/session-lab/internal/infra/pool/autoscale.go) lines 120 and 132-137;
> [`main.go`](apps/session-lab/cmd/pool-autoscaler/main.go) lines 99-103 for CPU utilisation plus
> PSI `full avg10`).
>
> ```text
>  ClaimStats(ready+pending) ──┐
>                              ├──► demand = ceil(slots / perBrowser)
>  node CPU util + PSI avg10 ──┘
>                              │
>                              ▼
>        pressure > high  ──► Desired - 1        (relieve the node)
>        pressure < low   ──► demand             (grow toward what is waiting)
>        inside the band  ──► hold Desired       (no oscillation)
>                              │
>                              ▼
>        bound growth to Desired + BootConcurrency   (pending pods starve the scheduler)
>                              │
>                              ▼
>                       clamp to Min … Max ──► publish
> ```
>
> The band is written in the source as "Grow toward demand only when the node is clearly free,
> shrink one step when it is clearly saturated, and hold inside the band so the loop cannot
> oscillate at the reconcile rate" (lines 151-153); the boot limit as "pending pods hold requests
> that starve the scheduler" (lines 166-168).

> [!note]- The 17 refusals were the scheduler, on CPU requests
> Quoted from [07](research/session-lifecycle-elasticity/07-overload-and-suspend.md) lines 55-59:
>
> > **The scheduler refused first, on CPU requests.** Seventeen acquisitions failed with
> > `0/1 nodes are available: 1 Insufficient cpu`, and those claims never reached Ready. At 100m of
> > CPU requests per sandbox pod, the node's 6000m allocatable admits roughly sixty pods before the
> > scheduler says no, and the run offered exactly that offered load, so the refusal arrived while the
> > pool was still refilling. These are the 17 `acquisition_failed` rows, each carrying the scheduler's
> > message.
>
> The outcomes table beside it: `success` 42, `acquisition_failed` 17, `reset_failed` 1 (line 37).

## 3. GPU offload

> The GPU cell is the one that carried the largest run on this node: **44 `browser-tpl-gpu`
> browsers, 352 slots, 306 concurrent contexts, 2986 of 3000 sessions successful**
> ([ledger](research/session-lifecycle-elasticity/24-capacity-ledger.md)). Two numbers kept
> being collapsed into one; they are separate.
>
> **Density** — how many sessions a node holds — is not something the A/B below measured.
> **Raster cost** — CPU per browser while rendering — is, and there the answer depends on
> whether the workload asks for a frame. The runner's hold sleeps and samples cgroups, so
> headless Chromium renders nothing and node CPU was identical across arms: **1.53 vs 1.58
> cores out of 6** at 30 concurrent sessions.
>
> Give it frames and it moves: with 40 `Page.captureScreenshot` calls per session — which
> `session-lab` does not implement ([08 §9.2](research/session-lifecycle-elasticity/08.2-gpu-offload-feasibility.md)
> calls this the missing "drive phase") — the GPU cut CPU per browser **2.13 → 1.57 cores
> (−26 %)** while screenshot latency rose **244 → 279 ms (+14 %)**.

Source: [`19-gpu-pool-a-b.md`](research/session-lifecycle-elasticity/19-gpu-pool-a-b.md) §1, and
[`24-capacity-ledger.md`](research/session-lifecycle-elasticity/24-capacity-ledger.md) for the
density row.

## 4. Overload run

> | Quantity | Measured |
> |---|---|
> | planned / stored | 60 / 60 |
> | outcomes | `success` 42, `acquisition_failed` 17, `reset_failed` 1 |
> | offered concurrency | 60.0 |
> | queue wait (claim → Ready) | p50 3.2 s, p95 134 s, p99 136 s, max 136 s |
> | memory | 154 MiB p50 peak, 209 MiB max, 168 MiB working-set max |
> | node memory | MemAvailable floor 3322 MiB of 15924 MiB (20.9%), swap used 4418 MiB, 195 samples |

Source: [`07-overload-and-suspend.md`](research/session-lifecycle-elasticity/07-overload-and-suspend.md) lines 34–48.

## 5. Slots

> | Scheduler (CPU requests) | ~21 browsers × 8 = **168 slots** at `100m` requests — historical | `FailedScheduling … Insufficient cpu` at 23:59 with 21 instances |

> The measured ceiling is in the [ledger](research/session-lifecycle-elasticity/24-capacity-ledger.md):
> **44 browsers × 8 = 352 slots, peak concurrency 306, 2986 of 3000 sessions successful** at
> `requests.cpu: 10m`. The 168 above is the same arithmetic at 100m, where the scheduler bound
> at ~21 browsers; at 10m it does not bind, and the run's constraint was the API server.

Source: [`24-capacity-ledger.md`](research/session-lifecycle-elasticity/24-capacity-ledger.md),
[`18-capacity-elasticity-and-kine.md`](research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) line 62.

## 6. Capacity arithmetic, as registered

> ```
> sessions/s = concurrent slots / mean hold
> ```
>
> | Bound | Concurrency | sessions/s | sessions / 24 h |
> |---|---|---|---|
> | Slots at 100m requests (historical) | 168 | 2.24 | 193 500 |
> | Slots at 10m requests, measured ([ledger](research/session-lifecycle-elasticity/24-capacity-ledger.md)) | 352 | 4.69 **[I]** | 405 000 |
> | CPU, measured 2.0 core-seconds per session over ~2.7 free cores | 135 | 1.35 | 116 600 |
> | Control plane (what actually broke) | — | ~2 | ~170 000 theoretical, **0 observed** |
> | A defensible soak target | 75 | 1.0 | 86 400 |

Source: [`18-capacity-elasticity-and-kine.md`](research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md) §2, lines 74–83.

## 7. Lifecycle and autoscaling

> | queue wait p50 / p95 | 3.2 s / 134 s | **85 s / 209 s** | **1.4 s / 54 s** |
> | reset p50 / p95 | 35 ms / 625 ms | — | 39 ms / 531 ms |
> | release (delete) p50 / p95 | — | **374 ms / 7.07 s** | — |
> | replica trajectory | 2 → 22 → **40** (configured max), then back to 2 after the run |

Source: [`09-results-three-runs.md`](research/session-lifecycle-elasticity/09-results-three-runs.md) lines 42–44 and 80.

<figure>
<img src="research/session-lifecycle-elasticity/assets/warm-claim-vs-raw-sandbox.png" alt="A warm claim and a raw sandbox opened through the same session route" loading="lazy">
<figcaption>Warm claim against a raw sandbox — reset p50 35 ms versus delete p50 374 ms (p95 7.07 s) plus a cold start</figcaption>
</figure>

> | 17:16:18 | 22 | 22 | `scale-out complete: 22 ready after 84.9s` |

Source: [`10-autoscaler-deployed.md`](research/session-lifecycle-elasticity/10-autoscaler-deployed.md) line 56.

## 8. Swap and zram

> **On the host, at scale: yes.** The largest run on this node served **306 concurrent
> contexts with zram at 5:1 in use** — 16.46 MiB allowance per pod, 2919 of 2986 sessions
> swapping, 11.7 GiB of node swap occupied, memory floor 16.3 %, zero OOM kills, zero
> throttled sessions. The constraint it hit was the API server (14 claim refusals), not
> memory ([ledger](research/session-lifecycle-elasticity/24-capacity-ledger.md)).

> **In the VM lab, on the compression trade:** YES, by one concurrent session there —
> 5 → 6 (+20 %) at 3 vCPU / 4 GiB, and with zram it also cuts tail latency ~70 % at equal
> load (§5b). **[M] Swap buys exactly one more concurrent session: 5 → 6, +20 %**, i.e.
> 15 → 18 sessions/min at a 20 s hold, on that box.

> | `zram-2to1` | lz4 | 39 409 ms | **31/31 ok** | **6.38** | **7** |
> | `zram-5to1` | zstd | **740 ms** | 24 ok, 7 failed (API `Delete` timeout) | 7.25 | 6 |
> | **latency at rung 6** — same 27 sessions, same box | **zstd (5:1)**: 740 ms vs 22.7 s vs 39.4 s p95 | 30–50× better |

Source: [`swap-capacity/README.md`](research/swap-capacity/README.md) §5b and §5d.1 for the VM
arms, and [`24-capacity-ledger.md`](research/session-lifecycle-elasticity/24-capacity-ledger.md)
for the host row.

<figure>
<img src="research/swap-capacity/assets/zram-ratio-benchmark.png" alt="The zram compression ratio benchmark capture" loading="lazy">
<figcaption>The ratio run — the benchmark capture behind the table above</figcaption>
</figure>

## 9. Control plane

> | Status | §8 fix applied and measured on the live node 2026-11-01 (`_synchronous=NORMAL&_busy_timeout=30000`): insert mean 64.6 → 2.06 ms, 823 Slow SQL/hour → 0, 9 fatals/24 h → 0 |

> | `Slow SQL` (threshold 1 s) | 823 / hour | **0** | `journalctl -u k3s` |

Source: [`05-write-ceiling-and-fixes.md`](research/cpu-oversubscription/05-write-ceiling-and-fixes.md) lines 15 and 331.

## 10. Per-session footprint

> | Pod cgroup `memory.current` | 559 MiB | `03-browser-density.md:187` |

Source: [`13-client-go-apf-and-density-limits.md`](research/session-lifecycle-elasticity/13-client-go-apf-and-density-limits.md) line 92.

## 11. The narrative

Author writes here.

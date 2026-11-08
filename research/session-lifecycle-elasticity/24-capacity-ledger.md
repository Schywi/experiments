---
title: 'Capacity ledger: every measured ceiling on this node, one row each'
kind: index
date: 2026-11-08
branch: 'docs-site'
commits: [e5acd32, 3a04dd2]
tags: [session-lifecycle, capacity, gpu, swap, density]
---

# Capacity ledger: every measured ceiling on this node, one row each

Capacity numbers in this repository kept being copied from one note into another,
and the copies outlived the configuration that produced them. This note is the
single place a ceiling is stated. Other notes quote it and link here.

| | |
|---|---|
| Status | Living table; append a row per measured run |
| Date | 2026-11-08 |
| Phase | session lifecycle |
| Related | [note 18](18-capacity-elasticity-and-kine.md) (the 168 measurement), [note 19](19-gpu-pool-a-b.md) (GPU A/B), [note 22](22-slot-health-and-saturation.md) (slot health), [swap study](../swap-capacity/README.md) |

## The ledger

Every row is a real run of `session-lab` on the Fedora host; the numbers are read
from `docs/session-lab-runs/<run>.json`. `slots` is instances × contexts.

| Date | Run | `requests.cpu` | ctx | instances | slots | peak concurrency | Outcome | Binding constraint | GPU cell | zram tier |
|---|---|---|---|---|---|---|---|---|---|---|
| 2026-10-31 | `20261031T024806Z` | 100m | 8 | 21 | 168 | — | `FailedScheduling … Insufficient cpu` | scheduler (CPU request) | no | no |
| 2026-11-07 | `20261107T033326Z` | 10m | 8 | 38 | 304 | 236 | 2992/3000 | one dead browser, 8 sessions lost | yes | 2:1 |
| 2026-11-07 | `20261107T211551Z` | 10m | 8 | 43 | 344 | 336 | 1619/3000 | one dead browser, 1378 sessions lost | yes | 2:1 |
| 2026-11-07 | `20261107T235847Z` | 10m | 8 | 44 | 352 | **306** | 2986/3000 | API server refused claim creation, 14 sessions | yes | 5:1 |

The 100m row is why 168 appears in older notes: the scheduler bound at ~21 browsers
because each pod asked for a tenth of a core. The template now asks for 10m, and the
scheduler stopped binding there **[M]** — 44 browsers were placed in the last run.

## The last run, in full

`20261107T235847Z` — pool `browser-pool-gpu`, template `browser-tpl-gpu`
(privileged, `/dev/dri`, `browser-sandbox-vulkan:local`), 8 contexts, 35 arrivals/s,
retries 1, health interval 5 s, boot concurrency unset.

```
planned 3000   stored 3000
success 2986 (99.53%)   acquisition_failed 14 (0.47%)   reset 0   lost 0
instances 44   sessions/instance max 106, mean 67.9      leaked claims 0
peak concurrency 306 of 352 slots (86.9%)   mean 202.1   in flight at once 2969

queue wait (claim->ready)  p50 0 ms   p95 3 ms   p99 1063 ms   max 123056 ms
acquire (arrival->ready)   p50 925414 ms  p95 1494891 ms
cpu core-s  total 9597.94  p50 2.69  max 45.33
memory peak p50 29 MiB  max 494 MiB      working set max 477 MiB
swap peak   p50 17 MiB  max 132 MiB      allowance 17260544 B = 16.46 MiB
swapped 2919/2986   refaults 2966/2986   oom 0   throttled 0
node memory floor 2590 MiB of 15924 (16.3%)   swap used 11992 MiB
node cpu peak 1.69 / 6 cores, mean 1.58
task time p50 8 ms  p95 8024 ms
```

The 14 failures arrived inside a 46-49 s window, all at claim creation, with
`attempts = 0`: `dial tcp 127.0.0.1:6443 … connect: connection refused`. One
API-server hiccup during the burst, not a capacity wall and not a browser.

## What this settles

**The slot ceiling is at least 306 concurrent contexts on this node** **[M]**, on the
GPU cell, with zram at 5:1 in use and 44 browsers resident. 168 is a historical
number from a 100m-request configuration.

**The GPU cell is what carried it** **[M]**: `browser-tpl-gpu` served all 2986
successes across 44 browsers and 352 slots. A statement of the form "a GPU tier buys
no extra sessions per node" is wrong as written; what note 19 measured is narrower
and is restated there.

**zram was in use while the ceiling was reached** **[M]**: 16.46 MiB allowance per
pod, 2919 of 2986 sessions swapping, 11.7 GiB of node swap occupied, 16.3 % memory
headroom, no OOM kills. The constraint that stopped 14 sessions was the API server.

## What it does not settle

| Question | Why this run cannot answer it | What would |
|---|---|---|
| Per-cell tier attribution (`class`, `pool`, `tier`) | every instance row carries `swap_tier = ''`: `resolveTiers` is defined (`internal/runtime/session/pool.go:110`) and never called, so the table prints `tier -` for pods that do carry `session-lab.io/spill` | one call in preflight; then the same run |
| Whether the GPU *raster path* raises the ceiling | the workload's task time is p50 8 ms, so no frames were requested; note 19's A/B is the measurement for that, and its answer is about CPU per browser, not slots | a run with screenshot loops per session |
| How far past 352 slots the node goes | 44 instances were the pool's size, not the node's measured limit; nothing scaled beyond it | raise `maxReplicas` and repeat |
| Boot cost per instance | 39 of 44 instances recorded `boot peak 0.00`: the sampler resolved the pod in time for five | longer resolve window or a pod-start watch |

## How to append a row

```bash
# after a run, from its report
jq -r '[.run_id, (.config.contexts_per_browser|tostring), (.instances.total|tostring),
        ((.instances.total*.config.contexts_per_browser)|tostring),
        (.concurrency.peak|tostring)] | @tsv' docs/session-lab-runs/<run>.json
```

Add the row with the configuration that produced it, and a link to the run id. Do
not quote a ceiling anywhere else in `research/`; link here instead.

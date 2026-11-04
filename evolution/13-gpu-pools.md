---
title: 'Phase 13 — GPU pools: measured, and what the measurement forbids (2026-11-02)'
date: 2026-11-04
kind: narrative
phase: 13
tags: [gpu, session-lifecycle]
---

# Phase 13 — GPU pools: measured, and what the measurement forbids (2026-11-02)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../config/gpu/`](../config/gpu/), [`../config/gpu-exp/`](../config/gpu-exp/),
[`../apps/gpu-device-plugin/`](../apps/gpu-device-plugin/) · **notes:**
[`19`](../research/session-lifecycle-elasticity/19-gpu-pool-a-b.md) (measured),
[`20`](../research/session-lifecycle-elasticity/20-cpu-gpu-pool-split.md) (design),
[`08.2`](../research/session-lifecycle-elasticity/08.2-gpu-offload-feasibility.md) (settled)

Phase 4 proved the RX 570 could compute: Vulkan through RADV, ~6× CPU on
inference. Phase 13 asks the question that actually matters for a session platform
— **does a GPU in the pool sell more sessions per node?** — and answers it with a
live A/B rather than an argument.

## The answer

**No.** At 30 concurrent sessions the node burned **1.53 cores with GPU-accelerated
Chromium and 1.58 cores without**, out of 6. Identical within noise.

The reason is not the GPU. It is the workload: `session-lab`'s hold is a sleep that
samples cgroups, so headless Chromium never requests a frame and there is nothing
for the GPU to relieve. The A/B was real — `SystemInfo.getInfo` inside the pool
pods reported `rasterization=enabled, vulkan=enabled_on` on
`RADV POLARIS10` — and it changed nothing, which is the finding.

## The counter-case, which is the useful half

Under a **forced render loop** (40 `Page.captureScreenshot` calls per session) the
GPU cut CPU per browser **2.13 → 1.57 cores (−26 %)** while screenshot latency rose
**244 → 279 ms (+14 %)**.

So the honest rule this phase produces: **acceleration pays per captured frame, not
per session.** Any workload that captures — screenshot loops, VLM grounding, the
console's tap-to-screenshot — benefits; any workload that only holds a page does
not. `gpu_compositing` stays `disabled_software` in both arms because headless
Chromium has no display surface to composite.

## The device plugin was the obstacle, and it was avoidable

`amd.com/render` sat at `0/0` throughout: `apps/gpu-device-plugin/main.go:146-151`
registers once and then blocks on `select {}`, so the registration is lost on a
kubelet restart. Rather than repair the plugin, the experiment reached the card
through a `hostPath /dev/dri` mount and an in-cluster image built with Kaniko
(buildah cannot run inside the agent shell: read-only `/run/user/1000`).

That workaround is what let `08.2` be settled without KubeVirt — passthrough was
never needed, and its 200 lines of reasoning now live as a rejected path in
`08.2` §14.

## What this phase forbids

The capacity claim for a GPU pool, on this workload, for good. It also re-grounds
the design in [`20`](../research/session-lifecycle-elasticity/20-cpu-gpu-pool-split.md):
the CPU/GPU pool split survives for **containment of one exclusive device** and for
**routing frame-capturing workloads**, not for raising sustainable concurrency
(`20` §0).

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `4b6dbea` | 2026-10-06 | `feat(experiments-ckq): add Go GPU device plugin for amd.com/render` |
| `0b8a527` | 2026-10-06 | `feat(experiments-ckq): verify Vulkan on RX 570; document device-cgroup requirement` |
| `4c0ba7d` | 2026-10-25 | `chore(experiments-3k3): track the gpu-exp/llm-gpu manifest` |

The GPU assets predate this phase by weeks — that is the point. Phase 13 did not
build acceleration; it **measured what acceleration is worth to a session
platform**, and the A/B landed with the notes on 2026-11-02.

## What this phase leaves open

Three items, none of which are capacity claims:

1. **Make acceleration the default anyway** for capture-heavy work — a one-flag,
   one-device change in `apps/browser-sandbox/entrypoint.sh`, worth −26 % CPU per
   browser under a render loop.
2. **Repair the device plugin's registration** so `amd.com/render` survives a
   kubelet restart and `hostPath` stops being necessary.
3. **Measure per-slot GPU cost**, which the cgroup cannot attribute and the CDP
   `Performance` domain might.

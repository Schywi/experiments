---
title: 'GPU-enabled pool A/B: does acceleration raise session capacity'
kind: experiment
state: benchmarked
phase: 8
date: 2026-11-02
branch: 'research/session-lifecycle-elasticity'
tags: [session-lifecycle, gpu, state-benchmarked]
---

# Note 19 — GPU-enabled pool A/B: does acceleration raise session capacity

| | |
|---|---|
| Status | Research note, measured |
| Date | 2026-10-31 |
| Phase | 8 — session lifecycle elasticity |
| Branch | `research/session-lifecycle-elasticity` |
| Scope | Live A/B on the Fedora host cluster: the `browser-pool` warm pool with and without GPU-accelerated Chromium |
| Related | [08](08.2-gpu-offload-feasibility.md), `config/sandbox/`, `apps/browser-sandbox/`, `apps/gpu-device-plugin/` |

Labels used throughout: **measured** (a run recorded it), **configured** (a file says it),
**inferred** (arithmetic from measurements).

## 1. Verdict

**For a workload that never requests a frame, GPU acceleration changes nothing about
how many sessions a node can hold; it is nonetheless the cell the largest run has been
served on.** The runner's hold is a sleep that samples cgroups; it never requests a
frame, so headless Chromium renders nothing and the accelerator has nothing to relieve.
Measured node CPU was identical across arms: **1.53 vs 1.58 cores out of 6** at 30
concurrent sessions.

Two different numbers kept being collapsed into that verdict:

```
density   how many sessions a node holds      browser-tpl-gpu, 44 browsers, 352 slots,
                                              306 concurrent contexts, 2986/3000 successful
                                              (20261107T235847Z, note 24)
raster    CPU per browser while rendering     1.53 vs 1.58 cores at 30 sessions here;
                                              2.13 -> 1.57 cores with a screenshot loop
```

So "a GPU tier buys no extra sessions per node" is wrong as written — the GPU cell is
what carried 352 slots and 306 concurrent contexts. What the A/B settled is the raster
column, and its answer is that acceleration pays only when a session captures frames.

The one case where the GPU moved a number is a forced render loop (40
`Page.captureScreenshot` calls per session), which `session-lab` does not implement
([08 §9.2](08.2-gpu-offload-feasibility.md) calls this the missing "drive phase"). There the
GPU cut CPU per browser **2.13 → 1.57 cores (−26%)** while screenshot latency rose
**244 → 279 ms (+14%)**.

## 2. What was changed and how

The pool template (`config/sandbox/templates/sandboxtemplate.yaml`) points the pool at
`127.0.0.1:5000/browser-sandbox:local`, an image built
`FROM mcr.microsoft.com/playwright/python:v1.48.0-jammy`. That image ships **no Vulkan
ICD at all** (`/usr/share/vulkan/icd.d` absent), and its entrypoint hardcodes
`--disable-gpu` and `--disable-features=Vulkan,VulkanFromANGLE`
(`apps/browser-sandbox/entrypoint.sh:43-58`).

A GPU-capable variant was built in-cluster with Kaniko (buildah cannot run in the agent
shell: read-only `/run/user/1000`):

```
FROM 127.0.0.1:5000/browser-sandbox:local
RUN apt-get install mesa-vulkan-drivers
ENV VK_ICD_FILENAMES=/usr/share/vulkan/icd.d/radeon_icd.x86_64.json
sed -i --disable-gpu -> --use-gl=angle --use-angle=vulkan --use-vulkan=native --disable-vulkan-surface
sed -i --disable-features=Vulkan,VulkanFromANGLE -> --enable-features=Vulkan,VulkanFromANGLE
```

The live template was patched (not the repo) for the GPU arm: image
`browser-sandbox-vulkan:local`, `securityContext.privileged: true`, `hostPath /dev/dri`
mounted at `/dev/dri`. The device plugin cannot supply the device today: `amd.com/render`
is `0/0` because `apps/gpu-device-plugin/main.go:146-151` registers once and then blocks
on `select {}`, so the registration is lost on kubelet restart.

## 3. Evidence

**GPU genuinely engaged in the pool pods** (**measured**, `SystemInfo.getInfo` over the
in-pod bridge, `POST /cdp`):

```
device: ANGLE (AMD, Vulkan 1.3.255 (AMD Radeon RX 570 Series (RADV POLARIS10)), radv-23.2.1)
featureStatus: rasterization=enabled, vulkan=enabled_on, webgl=enabled_readback,
               gpu_compositing=disabled_software
```

`gpu_compositing` stays `disabled_software` in both arms: headless Chromium has no display
surface to composite.

**Idle ladder** — identical workload, 30 s hold, `--fixed-arrival`, one context per
browser, `--policy reuse`, pool 20:

| sessions | outcomes | node CPU peak | mem peak p50 | min MemAvailable |
|---|---|---|---|---|
| 12 (CPU) | 12/12 | 1.49 / 6 | 173 MiB | — |
| 12 (GPU) | 12/12 | 1.55 / 6 | — | 6249 MiB |
| 24 (CPU) | 24/24 | 1.51 / 6 | 165 MiB | — |
| 24 (GPU) | 24/24 | 1.56 / 6 | — | 4925 MiB |
| 30 (CPU) | 30/30 | 1.53 / 6 | — | 4379 MiB |
| 30 (GPU) | 30/30 | 1.58 / 6 | — | 3916 MiB |

First boundary in both arms arrived at 42 sessions as the k3s apiserver refusing
connections, not a GPU or memory event. GPU arm was marginally *worse* on memory: the
`privileged` container plus the resident RADV driver cost ~460 MiB at 30 sessions.

**Render loop** — the pool's own Chromium image, one pod, two arms, a 2D-canvas+WebGL
page and 40 screenshots, cgroup `cpu.stat` differenced (**measured**):

| arm | renderer | CPU per browser | screenshot latency |
|---|---|---|---|
| `--disable-gpu` (shipped) | ANGLE/SwiftShader | 2.13 cores | 244 ms |
| `--use-angle=vulkan` + RADV | ANGLE/RADV Polaris10 | 1.57 cores | 279 ms |

## 4. Why the ladder could not show a CPU effect

`session-lab`'s hold is a sleep (`apps/session-lab/internal/runtime/session/run.go`); the
only browser work a run prices is the entrypoint's initial page load. Headless Chromium
does not issue BeginFrame without a client asking for one, so animated pages
(WebGL aquarium, 2D canvas) left node CPU at 1.5–1.65 cores for 6–12 sessions. The
console's real load — `Page.captureScreenshot` on a cadence — is exactly the missing
driver ([08 §9.2](08.2-gpu-offload-feasibility.md)). No run in this A/B falsified the render
loop number; none validated it through `session-lab` either.

## 5. Cleanup

Template restored to `browser-sandbox:local`, no `privileged`, no `/dev/dri`; pool back
to `replicas: 2`; autoscaler back to 1. Leftover artefact: the image
`127.0.0.1:5000/browser-sandbox-vulkan:local` in the in-cluster registry (removal needs
host root). No tracked repository file changed.

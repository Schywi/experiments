# Phase 3 — CPU models inside the cluster, Track B (2026-10-06 → 2026-10-10)

**Branch:** `native-k3s-gpu-models` · anchors `a958eed` (Laya), `e71aecd`
(Kokoro), `3baa2bd` (1.5B LLM), `a914679` (models README) · **tracked doc:**
[`../apps/models/README.md`](../apps/models/README.md)

## The decision that shaped everything after

**Every model starts on CPU. GPU is a separate, later track (Track C) that only
accelerates what the card actually supports.**

The reasoning is in
[`../research/native-k3s-gpu-models-plan.md`](../research/native-k3s-gpu-models-plan.md)
§3, and it is a hardware statement, not a preference:

| Model | Runtime | Footprint on this host | GPU stance |
|---|---|---|---|
| **Laya** (decision engine) | `laya-serve` | ~2–3 GB, 193–464 ms/decision | `[fast]` path is CUDA/TileLang only — **no AMD route** |
| **Kokoro TTS** (82M) | kokoro-onnx | ~0.6 GB resident, better than realtime | GPU wrappers are CUDA-only |
| **Qwen2.5-1.5B-Instruct** | llama.cpp `llama-server`, Q4_K_M | ~1.5 GB RSS, ~10–20 tok/s | Vulkan exists in llama.cpp → the Track C candidate |

The binding constraint is **RAM**, not CPU: platform ≈ 2–3 GiB, models ≈ 7–8 GiB,
against ~9 GiB free with browsers closed. That is why the plan enables zram
swap for pods as a *cushion* (`fail-swap-on=false` + `memorySwap.swapBehavior`)
and says explicitly it is not a capacity plan.

## What was built

Each model is a Deployment + Service + `Containerfile` under `apps/models/`,
reached over ClusterIP, observable as flows in Hubble. No host networking, no
sidecar proxies, no privileged pods.

## What it cost — the four failures worth remembering

Shipping the models was mostly a container-contract exercise, and each failure
is a check to run before adding model number four:

1. **`runAsNonRoot` vs a root-owned base image.** Kokoro (`b92a5c7`) and the
   LLM (`3655324`) both failed to start because the image runs as root while
   the chart demanded non-root. The outcome was **not** uniform, and the
   difference is deliberate: Kokoro and Laya were fixed at the image level
   (they now run as a non-root uid), while the LLM's `fedora:44` image still has
   no `USER` directive — the live pod runs as `uid=0` — so its chart dropped
   `runAsNonRoot: true` instead. That is a known, accepted weakness of the
   model image, not a template to copy.
2. **The same, from the other side, for Laya.** Laya needed an explicit
   non-root uid pinned so the pod could start (`9e977c8`, `b616cc2`).
3. **Memory limits are load-bearing.** Laya OOMKilled at the default limit and
   needed 6 GiB (`160a211`) — on a 15 GiB host, that is the whole experiment's
   budget in one line of YAML.
4. **Incomplete `COPY` lines produce image-only bugs.** The assistant image
   shipped without `provenance.py` because the Containerfile copied a fixed
   file list (`87c4dd0`) — the failure appeared at runtime, not at build time.

## What it proved

A local model is an ordinary workload: it schedules, it is scraped, its traffic
is visible in Hubble, and it fails in mundane ways (uid, memory, image
contents). That is the precondition for Phase 5 — an assistant that explains
the cluster can only be built on models that live *in* the cluster.

## Current state

| Model | Desired replicas | Why |
|---|---|---|
| `models/llm` (Qwen2.5-1.5B) | 1 | serves the assistant |
| `models/kokoro` | 1 | TTS for the assistant's spoken answers |
| `models/laya` | **0** | benchmarked in Phase 5 (E2) and scaled down (`6148491`) |
| `assistant/assistant-laya` | **0** | isolated A/B variant, scaled down (`ebe3b17`) |

Laya is off, not removed: E2 measured it against Qwen for planner duty, and the
results live in
[`../benchmark/experiments/e2-laya-vs-qwen-e2e/`](../benchmark/experiments/e2-laya-vs-qwen-e2e/RESULTS.md).

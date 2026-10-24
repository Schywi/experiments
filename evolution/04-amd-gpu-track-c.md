# Phase 4 — AMD GPU, Track C (2026-10-06 → 2026-10-18)

**Branch:** `native-k3s-gpu-models` (plus `trackc` for the Vulkan smoke test) ·
anchors `5668f1a` (probe), `4b6dbea` (device plugin), `81c398d`/`5720602`
(Vulkan llama.cpp), tag **`e3-amd-gpu`** at `9b5554e`, `a07bced` + `ab05883`
(E3 measurements) · **tracked paths:** [`../apps/gpu-device-plugin/`](../apps/gpu-device-plugin/),
[`../config/gpu/`](../config/gpu/README.md), [`../benchmark/experiments/e3-amd-gpu/`](../benchmark/experiments/e3-amd-gpu/RESULTS.md)

## The question, asked as a protocol

The point of Track C was to answer a hardware question with evidence instead of
enthusiasm. The plan (§7) fixed the order and said *stop at the first NO*:

1. Does `/dev/dri/renderD128` exist on the host? **Yes** — `amdgpu` drives the card.
2. Does a **non-privileged** pod with `/dev/dri` + `supplementalGroups: [render]`
   see it? **Yes** — `vulkaninfo` lists the RX 570 / RADV.
3. Does a **Vulkan** workload actually run? **Yes** — a Vulkan llama.cpp build
   (`81c398d`, `5720602`) and the smoke pods in `config/gpu/`.

## The verdict the repository keeps stating

| Node | Backs | On RX 570 (Polaris, gfx803) |
|---|---|---|
| `/dev/dri/renderD128` | graphics, **Vulkan**, video (Mesa RADV / VA-API) | ✅ works, mounted into pods |
| `/dev/kfd` | AMD **compute** (ROCm) | ❌ card is outside ROCm's support matrix — never mounted |

So: **no ROCm, no CUDA, no `amd.com/gpu` compute path on this card.** The GPU is
mounted for exactly one class of work — Vulkan — and for validating the
kubelet → device-plugin → pod device path end to end.

## The adapter, and what it cannot do

`apps/gpu-device-plugin/` is a small Go program implementing the Device Plugin
gRPC API (`ListAndWatch`, `Allocate`) and advertising `amd.com/render`. It turns
the render node into a **schedulable, counted resource** with automatic
`/dev/dri` mounting, replacing a raw `hostPath`.

It does **not** deliver per-pod VRAM quotas, and the plan states why:

- **Admission/counting (cooperative): possible.** The card can be modelled as N
  slices (`amd.com/vram-gb`, 8 units on 8 GB) so the scheduler admits at most
  that much concurrently. Each slice still maps to the same render node.
- **Hard enforcement: impossible on this card.** The plugin controls *who is
  admitted and what is mounted*; it cannot cap *runtime memory use* inside a
  process. NVIDIA does that with MPS/CUDA accounting; ROCm offers partitioning
  on supported cards; raw `amdgpu`/Vulkan on gfx803 exposes no per-process VRAM
  limit. A slice is therefore **advisory**.
- **DRA** (the modern successor for attribute-rich requests) allocates devices
  too; it does not invent a limit the hardware lacks.

## What E3 measured

[`../benchmark/experiments/e3-amd-gpu/RESULTS.md`](../benchmark/experiments/e3-amd-gpu/RESULTS.md)
records the CPU-vs-GPU Qwen comparison, plus the follow-up single-pod contention
probe (`ab05883`). Baseline commit hashes are recorded next to the numbers
(`9b5554e`) because these are single-host measurements: they are valid for that
revision on that card and nothing else.

## Why this is the most transferable phase

The deliverable is not "the GPU works". It is a **method**: state the protocol,
stop at the first NO, and separate what the scheduler can promise from what the
hardware can enforce. That distinction — admission vs enforcement — is what
interviewers and reviewers actually press on, and it is written down rather than
implied.

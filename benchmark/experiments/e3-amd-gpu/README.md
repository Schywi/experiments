# E3 — AMD GPU vs CPU for Qwen inference (isolated)

- **Status:** in progress (gated on the Vulkan spike)
- **Tag:** (set on commit) · **Base commit:** (set on commit)
- **Scope:** new namespace `gpu-exp`. **Production untouched** — no prod model
  replacement, no automatic GPU scheduling changes. Reproducible + reversible.

## Verified GPU reality (before designing anything)

| Check | Result |
|---|---|
| GPU | AMD **Radeon RX 570** — Ellesmere/**Polaris gfx803**, PCI `01:00.0` |
| Kernel driver | `amdgpu` **loaded**; DRM nodes on the host (`/sys/class/drm/card1`, `renderD128`) |
| ROCm / `/dev/kfd` | **none** — gfx803 dropped from ROCm; no `kfd`, no `rocm-smi`/`rocminfo`. **No PyTorch/ROCm; no CUDA.** |
| Compute backend | **Vulkan (Mesa RADV)** via `/dev/dri/renderD128` only |
| Exposed as a schedulable resource | **No** — Go device plugin not deployed; node advertises no `amd.com/render`; no `gpu` ns |
| In-cluster GPU inference backend | **None** — `models/llm` is llama.cpp built **CPU** (`-DGGML_NATIVE=ON`, no Vulkan) |
| Agent shell sees GPU | **No** (`/dev/dri` absent in the sandbox) → **GPU work runs in a Pod** |

**Conclusion:** there is no compute stack on this card. The GPU path is
**llama.cpp + Vulkan**, and it is **unproven on gfx803**. So: spike first,
benchmark second.

## Plan

1. **Vulkan spike** (`gpu-exp`) — build llama.cpp with `-DGGML_VULKAN=ON`, run a
   tiny prompt with `/dev/dri` mounted (+ `supplementalGroups: [107]`) and
   explicit limits; capture `vulkaninfo` / RADV device info.
   **Gate: if this fails, the GPU benchmark is not possible.**
2. **CPU vs GPU benchmark** — same GGUF (Qwen2.5-1.5B Q4_K_M), same prompt set +
   generation settings. Metrics: TTFT, total generation, tokens/s, RSS, GPU
   utilization (no ROCm → limited counters), end-to-end latency.
   CPU arm = the existing `models/llm` config; GPU arm = the Vulkan build.
3. **Go device-plugin behavior** — deploy `apps/gpu-device-plugin` and observe
   its Go scheduling/goroutine behavior (ListAndWatch/Allocate), how it advertises
   `amd.com/render`, and how it interacts with the GPU pod.

## Files

- `cleanup.md` — pre-experiment scale-down of `assistant-laya` + `worm-lab`.
- `manifests/` — the isolated spike/benchmark manifests (added as we go).
- `baseline/` — recorded results.

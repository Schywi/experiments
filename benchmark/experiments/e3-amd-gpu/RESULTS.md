# E3 results — AMD RX 570 (Vulkan) vs CPU for Qwen2.5‑1.5B inference

**Verdict: the GPU works via Vulkan (RADV) and is ~6× faster at generation than
CPU — but only after the Go device plugin grants the render node (a `hostPath`
mount alone is denied by the device cgroup).**

## Fixed configuration (identical across arms)

| Item | Value |
|---|---|
| Model | `Qwen2.5-1.5B-Instruct-Q4_K_M.gguf` (same file) |
| Image | `127.0.0.1:5000/llama-vulkan:local` (llama.cpp, `GGML_VULKAN=ON`) |
| Context | `-c 1024` |
| Prompts (3) | "Say hi in three words." · "What is a Kubernetes pod?" · "Explain TCP in one sentence." |
| Generation | `max_tokens=64`, `temperature=0` |
| GPU arm | `-ngl 99` (all layers on GPU), requests `amd.com/render: 1` |
| CPU arm | `-ngl 0`, `--threads 4` |
| Namespace | `gpu-exp` (isolated) · pods run **sequentially**, not concurrently |

## Environment

- GPU: **AMD Radeon RX 570 Series (RADV POLARIS10)** — `vulkaninfo` reports
  `DEVICE_TYPE_DISCRETE_GPU`, driver `radv`; a second `llvmpipe` (software) device
  is also enumerated but unused.
- No ROCm/`/dev/kfd`; Vulkan is the only compute path on gfx803.

## Results

### Generation throughput (tokens/s) — the headline

| prompt | GPU tok/s | CPU tok/s | speedup |
|---|---|---|---|
| Say hi (4 tok) | 81.3 | 16.1 | 5.0× |
| Kubernetes pod (64 tok) | 96.1 | 14.8 | **6.5×** |
| TCP (31–41 tok) | 96.7 | 14.2 | 6.8× |

### Prefill / TTFT and total generation

| prompt | GPU prefill ms | CPU prefill ms | GPU gen ms | CPU gen ms |
|---|---|---|---|---|
| Say hi | 169.7 | 358.9 | 36.9 | 186.0 |
| Kubernetes pod | 149.9 | 157.5 | **655.8** | **4252.0** |
| TCP | 86.5 | 159.0 | 413.7 | 2109.8 |

End-to-end for the 64-token answer: **GPU ≈ 806 ms** (150 + 656) vs **CPU ≈ 4410 ms**
(158 + 4252) → **~5.5×**.

### Memory / utilisation

| | GPU arm | CPU arm |
|---|---|---|
| host RSS | **320 MiB** | 1398 MiB |
| host peak (VmHWM) | 1082 MiB | 1633 MiB |
| VRAM used | **~1.84 GiB** (`mem_info_vram_used=1,973,927,936`) | — |
| `gpu_busy_percent` | 0 (sampled between requests; see caveats) | — |

Host RSS is ~4.4× lower on GPU because the model lives in VRAM.

## Go device plugin (the thing that made it work)

- Advertises `amd.com/render: 1`; kubelet logs `registered amd.com/render`.
- `Allocate()` returns a `DeviceSpec` for `renderD128` (rw) + a `/dev/dri` mount →
  kubelet adds the char device to the container's **device cgroup**. Without it,
  `vulkaninfo`/RADV got `Operation not permitted` and llama.cpp fell back to CPU.
- Runtime footprint: **5 OS threads, ~6.9 MiB RSS, ~10.8 MiB peak, 1m CPU**.
  One long-lived `ListAndWatch` goroutine blocks on a **30 s ticker** (timer wait,
  not CPU) and re-sends; `Allocate` is a short RPC. No goroutine leak, no
  scheduler contention.

## Spike evidence (before the plugin)

`hostPath: /dev/dri` alone → `/dev/dri` visible but
`Could not open device /dev/dri/renderD128: Operation not permitted
(VK_ERROR_INCOMPATIBLE_DRIVER)`; llama.cpp ran on CPU (~6 tok/s) undetected.

## Caveats

- `gpu_busy_percent` read 0 at sample time (sysfs quirk / sampled between
  requests). Real GPU use is confirmed by the RADV device, ~1.84 GiB VRAM, and the
  6× speedup.
- CPU arm used 4 threads; this is a controlled A/B, not a production perf claim.
- One run; generation timings are llama.cpp's own `predicted_ms`. Re-run for p95.
- RX 570 VRAM is limited; a larger model would not fit. This is gfx803 / Vulkan
  only — no ROCm, no PyTorch/CUDA.

## Teardown (reversible)

```bash
kubectl -n gpu-exp delete pod --all
kubectl delete pvc -n gpu-exp gpu-models
kubectl delete namespace gpu-exp
helm uninstall gpu-device-plugin -n gpu
kubectl delete namespace gpu            # optional; removes the DaemonSet ns
```
The device plugin was deployed with `helm` (not Argo), so `helm uninstall` fully
reverts the GPU scheduling resource. Production models were never touched.

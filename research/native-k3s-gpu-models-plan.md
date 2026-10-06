# Native k3s + GPU + CPU models: plan and recorded decisions

> Tracked design note (committed to the repo). Machine-specific runbooks live in
> the gitignored `docs/`; this file records the decisions any future reader of
> this repository needs in order to understand *why* things are the way they are.

## 0. Why this exists (background)

The original lab (`start.sh`, `config/k3d/`) is a Docker-backed **k3d** cluster
with Cilium + Hubble + MetalLB + Argo CD. Two facts forced a pivot to **native
k3s on the Fedora host**:

1. **k3d had Cilium issues** on this machine — the k3d path had to fall back to
   iptables masquerading and host-legacy routing because the containerized
   kernel rejected Cilium 1.20's BPF SNAT program (see README's "Current known
   mismatch" and `config/k3d/`).
2. **The base Linux environment lacked `systemd` and eBPF** — both are required
   by k3s and by Cilium's eBPF datapath respectively. The Fedora 44 host has
   both (kernel 6.19, systemd boot, cgroup v2).

Running k3s **natively** restores full Cilium eBPF/kube-proxy-replacement and
puts the host's GPU device nodes within reach of pods.

## 1. System this was tested on

| Item | Value | Why it matters |
|---|---|---|
| OS | Fedora 44 Workstation, x86_64 | systemd + cgroup v2 + recent kernel |
| Kernel | 6.19.x | clears Cilium's 5.10+ / eBPF requirements |
| CPU | Intel i5-9400F, 6C/6T, **no iGPU** | **AVX2 only (no AVX-512)** — build llama.cpp accordingly |
| RAM | 15 GiB total, > 9 GiB free when browsers are closed | the binding constraint for CPU models |
| Swap | 8 GiB zram | see §4 — k8s swap is enabled for pods |
| GPU | AMD Radeon RX 570 (Ellesmere/Polaris **gfx803**, 8 GB) | old + ROCm-unsupported → see §2 |
| GPU driver | `amdgpu` (kernel) + Mesa **RADV** 26.x | Vulkan/graphics path |
| SELinux | Disabled | simplifies `hostPath` device mounts |
| Container tooling | `docker` absent; `podman` present (rootless quirks) | image builds use `buildah` + `k3s ctr images import` |

## 2. Why `/dev/dri/` — and why the GPU is mounted at all

The GPU is **old and unsupported for modern compute**: the RX 570 is Polaris
(`gfx803`), which was **removed from AMD's supported ROCm matrix**. There is no
ROCm/PyTorch-ROCm, no CUDA, and no `amd.com/gpu` compute path on this card.

The two device nodes have completely different meanings:

| Node | Backs | On RX 570 (gfx803) |
|---|---|---|
| `/dev/dri/renderD128` | graphics, **Vulkan**, video (Mesa RADV / VA-API) | ✅ works |
| `/dev/kfd` | AMD **compute** (ROCm) | ❌ unsupported — pointless to mount |

**Decision:** pods mount **only `/dev/dri`**, and only to enable
*Vulkan/graphics/video* workloads (e.g. a Vulkan inference path). We do **not**
mount `/dev/kfd`, and we do **not** pretend this gives ROCm/CUDA compute.

This is why the GPU is mounted: not for PyTorch/ROCm, but to run the *one*
class of GPU work the card still does (Vulkan), and to validate the
kubelet → device-plugin → pod device path end-to-end.

## 3. Model decisions (CPU first, GPU later)

Recorded policy: **every model starts on CPU. GPU is a separate, later track
(Track C) that only accelerates what the card actually supports (Vulkan).**

| Model | Runtime | CPU footprint | CPU perf (this host) | GPU stance |
|---|---|---|---|---|
| **Laya** (decision engine) | `pip install "laya[serve]"`, `laya-serve` | ~2–3 GB (all 3 checkpoints) | 193–464 ms/decision | **CPU-only.** Its `[fast]` GPU path is CUDA-only (TileLang); no AMD route. |
| **Kokoro TTS** (Kokoro-82M) | kokoro-onnx (onnxruntime) | ~310 MB fp32 / ~138 MB q8; ~0.6 GB resident | better than realtime (RTF ≈ 1.4×) | **CPU.** GPU wrappers are CUDA-only. |
| **LLM 1.5B** (Qwen2.5-1.5B-Instruct) | llama.cpp `llama-server`, GGUF **Q4_K_M** | ~0.94 GB weights, ~1.5 GB RSS | ~10–20 tok/s | CPU first. Vulkan path exists in llama.cpp and is the Track C upgrade candidate. |
| **LLM 3B** (Qwen2.5-3B-Instruct) | llama.cpp `llama-server`, Q4_K_M | ~1.9 GB weights, ~3.5 GB RSS | ~4–8 tok/s | CPU only if quality demands it; accept the speed hit. |

**Memory budget (the real constraint):** platform (k3s + Cilium + Argo CD +
Hubble) ≈ 2–3 GiB. Laya (2–3) + Kokoro (0.6) + 1.5B LLM (1.5) ≈ 7–8 GiB total —
fits in the > 9 GiB available, but **close the browser**. The 3B LLM pushes
toward ~10 GiB. RAM, not CPU or GPU, is the binding constraint.

## 4. Swap for pods (decision: enabled)

This machine runs with zram swap, and the models may exceed RAM transiently.
Kubernetes supports swap on **cgroup v2** (Fedora has it) via the kubelet's
`memorySwap.swapBehavior` (`NoSwap` default; `LimitedSwap` enables swap for
best-effort pods). NodeSwap is GA since k8s 1.30.

**Decision:** enable `memorySwap.swapBehavior=LimitedSwap` and
`fail-swap-on=false` so pods can use the host zram swap.

Caveats recorded so a future reader is not surprised:
- Swap-backed pods are **slow** (zram is still RAM-speed-ish, but eviction
  accounting adds jitter) — it is a safety cushion, not a capacity plan.
- Exact k3s wiring is a Track A implementation detail (k3s passes kubelet flags
  via `--kubelet-arg`; the structured `memorySwap` block may need a kubelet
  config file).

## 5. Track A — Platform (k3s + Cilium + pods + Argo CD + Hubble)

Independent of models and GPU. Native-k3s rework of the old k3d lab.

`/etc/rancher/k3s/config.yaml` (Cilium owns networking):
```yaml
write-kubeconfig-mode: "0644"
flannel-backend: none
disable-network-policy: true
disable-kube-proxy: true
cluster-cidr: 10.42.0.0/16
disable:
  - traefik
  - servicelb
```
Then install k3s and remove the auto-created `/var/lib/rancher/k3s/server/manifests/traefik.yaml`.

Cilium (reuse the repo's pinned **1.20.1**, but drop the k3d-only settings):
```yaml
kubeProxyReplacement: true
ipam:
  mode: kubernetes            # reuse 10.42.0.0/16
k8sServiceHost: 127.0.0.1
k8sServicePort: 6443
hubble:
  relay:
    enabled: true
  ui:
    enabled: true
```
Concrete change to make: `config/helm/cilium/values.yaml` currently carries the
k3d Docker-bridge settings (iptables masquerade, host-legacy routing) — remove
those for native. And fix the README's known Argo CD mismatch
(`install-ingress.sh`/`validate.sh` assert `LoadBalancer`; `values.yaml` says
`ClusterIP`). Argo CD stays headless/no-auth per AGENTS.md.

**Exit criteria:** node `Ready` → `cilium status` healthy → Argo CD `ClusterIP`
running → `hubble observe` shows flow → a `hello-world` pod schedules and
reaches the internet.

## 6. Track B — Models RUN inside the cluster (CPU)

Serve each model as a Deployment + Service; call via ClusterIP/port-forward;
observe flows in Hubble. Laya → Kokoro → LLM, one at a time.

Prerequisite (the recurring k3d blocker): a working image builder. Use
`buildah` (rootless, no daemon) + `sudo k3s ctr images import`, or install
`docker`. Bake weights into images or load them from a PVC on first run.

**Exit criteria:** each model answers its API from inside a pod, via a Service,
with Hubble showing the flows between them.

## 7. Track C — GPU: YES/NO test, then the Go adapter

Protocol, in order, stop at the first NO:
1. `/dev/dri/renderD128` exists on the host (expected: yes, `amdgpu` drives the card).
2. A **non-privileged** pod with `/dev/dri` + `supplementalGroups: [render]`
   sees the device (`vulkaninfo --summary` lists the RX 570 / RADV).
3. A **Vulkan** workload actually runs (laya.cpp `--vulkan`, or llama.cpp Vulkan).

Expected result: 2 and 3 = **YES for graphics/Vulkan/video**; **ROCm/CUDA
compute = NO** (driver-stack reality, not a k3s deficiency).

### The Go device plugin (adapter)

Yes — a small Go program implementing the Kubernetes Device Plugin gRPC API
(`ListAndWatch` + `Allocate`, registered at
`/var/lib/kubelet/device-plugins/*.sock`) can advertise the render node as a
schedulable resource (`amd.com/render`) and auto-mount `/dev/dri/renderD128`
into requesting pods. This makes the GPU a first-class, counted resource
instead of a raw `hostPath`.

**What it can NOT do — the VRAM question:**

> "Can we say pod X uses 1 GB of VRAM?"

- **Counting/admission (cooperative): YES.** A device plugin advertises a *count
  of devices*, not bytes — but you can model the card as N "1 GB slices"
  (e.g. `amd.com/vram-gb` with 8 units on an 8 GB card). The scheduler then
  admits at most 8 GB worth of concurrent requests. Each slice maps to the same
  render node in `Allocate`.
- **Hard enforcement (guarantee the pod can't exceed 1 GB): NO, not on this
  card.** The plugin can control *who is admitted and what is mounted*, but it
  cannot cap *runtime memory use* inside the process. Enforcement needs a
  runtime/stack that implements a memory limit: NVIDIA does it with MPS/CUDA
  memory accounting; ROCm offers VRAM partitioning on *supported* cards; raw
  `amdgpu`/Vulkan on gfx803 has **no per-process VRAM quota** exposed to the
  kernel. So a slice is **advisory** — a pod can still allocate more than its
  slice unless the workload self-limits.
- **Dynamic Resource Allocation (DRA)** is the modern successor for
  attribute-rich requests, but it also advertises/allocates devices, and does
  not invent a memory limit the hardware lacks.

**Conclusion:** the Go adapter gives us *scheduling + counting + automatic
mounting* of the GPU. It does **not** give true per-pod VRAM quotas on this
card — that would require the workload to self-limit (pass it a
`--max-vram` style flag) or a compute stack with real memory enforcement that
gfx803 doesn't support.

## 8. Sequencing (simplification)

1. **Track A** alone — a working observable cluster is the substrate.
2. **Track B** — CPU models, one at a time, watching the RAM budget.
3. **Track C** — the YES/NO probe, then the Go device plugin only if Vulkan works.

This keeps every track independently verifiable and prevents the GPU question
from stalling the models.

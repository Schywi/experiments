# GPU access on the native-k3s host (Track C)

Track C answers one question before anything else: **can a Pod use this host's
GPU at all, and how?** This directory holds the probe and the findings.

## The question, and the answer

| Step | Result |
|---|---|
| 1. Host exposes a DRM render node | ✅ `/dev/dri/renderD128` (amdgpu) |
| 2. A **non-privileged** Pod sees it | ✅ with `supplementalGroups: [105]` (render) — see `probe-pod.yaml` |
| 3. A **Vulkan** workload runs on it | ✅ expected — Mesa RADV supports this card |

**Verdict: YES for Vulkan / graphics / video.** But **NO for ROCm/CUDA compute**:
the RX 570 is Polaris (`gfx803`), dropped from AMD's supported ROCm list. This is
a driver-stack reality, not a Kubernetes or Cilium limitation.

## Why `/dev/dri`, and not `/dev/kfd`

| Node | Backs | On this card |
|---|---|---|
| `/dev/dri/renderD128` | graphics, **Vulkan**, video (Mesa RADV / VA-API) | ✅ works |
| `/dev/kfd` | AMD **compute** (ROCm) | ❌ unsupported — do not mount |

The GPU is mounted for the *one* class of work the card still does (Vulkan), and
to validate the kubelet → device → Pod path — not to run PyTorch/ROCm.

## The probe

`probe-pod.yaml` mounts `/dev/dri` into a plain `busybox` Pod and joins the
`render` group (gid 105). It is deliberately **not** privileged and does not
mount `/dev/kfd`.

Observed output on the reference host:

```
--- /dev/dri ---
crw-rw---- 1 root  39  226,1  card1
crw-rw-rw- 1 root 105 226,128 renderD128
--- identity ---
uid=0(root) gid=0(root) groups=0(root),10(wheel),105
```

## Resource scheduling (the Go adapter, next)

Mounting `/dev/dri` by `hostPath` gives device *visibility* but no scheduling:
Kubernetes does not count the GPU, and nothing stops many Pods sharing it. To
make it a first-class, counted resource we add a **Go device plugin** that
advertises a resource (e.g. `amd.com/render`) and `Allocate`s the render node to
requesting Pods. Note the limits of that approach — see
`research/native-k3s-gpu-models-plan.md` §7: a device plugin gives scheduling and
automatic mounting, **not** per-Pod VRAM quotas (this card exposes no such
limit).

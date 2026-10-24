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

## Result (verified live on the reference host)

`vulkaninfo --summary` inside a Pod reports the real GPU:

```
GPU0:
    deviceName = AMD Radeon RX 570 Series (RADV POLARIS10)
    driverName = radv          driverInfo = Mesa 26.2.3
    vendorID   = 0x1002        deviceID   = 0x67df
    deviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU
```

`config/gpu/vulkan-test-pod.yaml` is the no-build version (public `fedora:44`
image, `dnf install` at start, `ndots:1` to dodge the host search-domain bug).

## The catch: `/dev/dri` needs the **device cgroup** opened

Mounting `/dev/dri` by `hostPath` only makes the node *visible* (`ls`/`stat`
work). The container's **device cgroup** still denies `open()` on the render
node, so every Vulkan driver fails with `EPERM`:

```
WARNING: radv: Could not open device /dev/dri/renderD128: Operation not permitted
```

Two ways to grant access:

- `privileged: true` -- what the smoke test uses. Broad; not recommended.
- a **device plugin** (`apps/gpu-device-plugin`) -- grants the device in the
  cgroup on `Allocate`, so the workload stays non-privileged. That is exactly
  why the plugin exists.

**Visibility = `hostPath`. Access = device cgroup = `privileged` OR device plugin.**

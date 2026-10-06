# GPU device plugin (Track C)

A small Go program that implements the kubelet **Device Plugin API** and
advertises this host's DRM render node(s) as a schedulable resource
(default `amd.com/render`).

This is the answer to "k3s has no adapter for this GPU" — the card has no
ROCm/CUDA, but it *does* expose `/dev/dri/renderD128`, and a device plugin can
turn that into a counted, schedulable resource with automatic mounting.

```text
apps/gpu-device-plugin/
├── main.go        # ListAndWatch + Allocate against /dev/dri/renderD*
├── Containerfile  # static Go build -> alpine
└── chart/         # DaemonSet + ServiceAccount
```

## What it does, and what it does not

- ✅ advertises `amd.com/render` (one per `renderD*` node) so Pods can
  `resources.limits: {amd.com/render: 1}`;
- ✅ `Allocate` mounts the granted render node into the Pod (no `hostPath`+
  `privileged` needed at the workload);
- ❌ does **not** enforce per-Pod VRAM limits — the hardware exposes none, so a
  "slice" count is advisory only. See
  `research/native-k3s-gpu-models-plan.md` §7.

## Build and import (on the host)

```bash
buildah bud -t gpu-device-plugin:local apps/gpu-device-plugin
buildah push gpu-device-plugin:local oci-archive:/tmp/gpu-device-plugin.tar
sudo k3s ctr images import /tmp/gpu-device-plugin.tar
```

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
helm upgrade --install gpu-device-plugin apps/gpu-device-plugin/chart -n gpu
```

## Verify

```bash
# the node now advertises the resource
kubectl get node fedora -o jsonpath='{.status.allocatable}' | tr ',' '\n' | grep amd.com

# a Pod can request it and gets the render node mounted
kubectl -n gpu run gpu-request --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"c","image":"busybox:1.36",
    "command":["ls","-l","/dev/dri"],"resources":{"limits":{"amd.com/render":1}}}]}}'
kubectl -n gpu logs gpu-request
```

## Notes

- Run as a DaemonSet so every (future) node advertises its own render nodes.
- The plugin needs no Kubernetes API access — it speaks gRPC to kubelet over
  the socket in `devicePluginDir`. The ServiceAccount has no token mounted.
- Not yet built or run: it needs `buildah` on the host (absent in the agent
  sandbox) and a Go toolchain for `go mod tidy` (the Containerfile does this at
  build time).

# Wasmtime runtime preparation for the existing k3d node

This directory prepares the already-running `k3d-cilium-lab-server-0` node for
Wasm workloads without creating a new cluster or applying Kubernetes resources.
It uses the patched `containerd-shim-wasmtime-v1` from runwasi `v0.6.1`, a
`wasmtime` containerd handler, and a matching Kubernetes `RuntimeClass`.

## Deliberate boundary

`RuntimeClass` only selects a container runtime. It does not install one. The
shim binary and its containerd handler must be present inside the k3d server
container before a Pod using `runtimeClassName: wasmtime` can start.

`install-existing-node.sh` is the only mutating step, and it is intentionally
not wired into `Tiltfile`, bootstrap, Argo CD, or a DaemonSet. It changes the
running node's containerd configuration and requires an explicit node restart.
Run it only after Cilium is healthy:

```bash
config/k3d/wasmtime/install-existing-node.sh --restart-node
```

The script downloads the pinned amd64/musl shim release, verifies its SHA-256,
places it alongside the node's existing `containerd-shim-runc-v2`, and appends
the handler to k3s's `config.toml.tmpl`. The running k3d node is intentionally
reused. This mutation does not survive a future k3d recreation; a custom,
pinned k3s node image is the reproducible future replacement.

## Deferred Kubernetes apply

After the node restart and Cilium recovery, apply `runtimeclass.yaml`, then
`smoke-pod.yaml` to validate handler selection before deploying the Worm
controller or worker Pods. No Kubernetes commands are part of this preparation
step.

## Static checks

```bash
bash -n config/k3d/wasmtime/install-existing-node.sh
shellcheck config/k3d/wasmtime/install-existing-node.sh
kubeconform -strict -ignore-missing-schemas \
  config/k3d/wasmtime/runtimeclass.yaml \
  config/k3d/wasmtime/smoke-pod.yaml
```

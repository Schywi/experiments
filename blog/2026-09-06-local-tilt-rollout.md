---
title: "Local Tilt rollout for the bounded Worm experiment"
date: 2026-09-06
tags:
  - tilt
  - k3d
  - kubernetes
  - cilium
  - hubble
  - argocd
  - webassembly
  - wasmtime
  - runwasi
  - vector
  - gitops
---

# Local Tilt rollout for the bounded Worm experiment

The local development path should not depend on GitHub Actions or a remote
image registry. Tilt owns the fast experiment loop: it builds artifacts on the
developer machine, loads them into the existing k3d cluster, renders the
experiment workloads, and watches their readiness. Argo CD remains the
platform and stable-GitOps reconciler; it must not compete with Tilt over the
same Deployment.

```text
local source change
  -> Tilt build/custom build
  -> local image store
  -> k3d image import
  -> containerd image store on the existing node
  -> Tilt-rendered Helm workload
  -> Cilium/Hubble observes per-worm traffic
```

The existing `config/k3d/import-images.sh` already uses this local import
pattern for platform images. It is the first implementation choice because the
running `cilium-lab` cluster was created without a k3d-managed registry. A
local registry is a useful later optimization, but normally needs k3d registry
configuration at cluster creation or a deliberate containerd registry change
and node restart.

## Ownership boundary

| Owner | Resources |
|---|---|
| Tilt | Local Go, Elixir, and Lua/Wasm artifacts; worker/controller/regression/Vector development workloads; Pod readiness and log watching |
| Argo CD | Cilium, Argo CD itself, and later immutable/pinned experiment releases from Git |
| k3d image import | Local distribution into the existing node's containerd image store |
| Cilium | Service traffic enforcement and Hubble flow visibility |

Tilt and Argo CD must not reconcile the same Helm release or Deployment. For
active development, Tilt owns the experiment. After an image is stable and
pinned, its Argo CD Application can become the owner and the corresponding
Tilt resource must be disabled.

## Wasmtime runtime position

The Wasmtime runtime is a node prerequisite, not an application chart. The
runwasi shim binary and `io.containerd.wasmtime.v1` handler must exist inside
the k3d server before the Kubernetes `RuntimeClass` can select them.

```text
Cilium healthy
  -> manual Tilt resource: wasmtime-runtime
  -> node shim/configuration and explicit node restart
  -> RuntimeClass smoke workload
  -> Vector, controller, regression
  -> Lua/Wasm worker Pods
```

The Tilt resource must be manual (`auto_init=False`). It watches the files
under `config/k3d/wasmtime/` but never restarts the node automatically. A node
restart interrupts every local workload and is an operator decision.

## Hubble ingress and Argo CD boundary

Hubble is intentionally exposed only on the host loopback interface:

```text
127.0.0.1:8080
  -> k3d server NodePort 30080
  -> Cilium Ingress, host localhost, path /
  -> kube-system/hubble-ui ClusterIP
```

The ingress manifest is `config/helm/cilium/hubble-ui-ingress.yaml`. It routes
only the `localhost` host to Hubble UI.

Argo CD must **not** be added to this ingress. Its Helm values require
`server.service.type: ClusterIP` and `server.ingress.enabled: false`; it has no
authentication in this private development cluster. Do not add an Argo CD
Ingress, LoadBalancer, NodePort, or another host-wide port binding. Operate it
through its internal Service and Kubernetes/Argo status, not a public browser
endpoint.

## Delegable implementation plan

Each work package should be a separate `bd` issue before work starts.

1. **Tilt experiment orchestration**
   - Input: current root `Tiltfile`, existing k3d image-import pattern.
   - Deliverable: manual `wasmtime-runtime` resource plus Tilt resources for
     Vector, controller, regression, worker, and an aggregate `worm-lab`.
   - Acceptance: no resource applies a workload owned by an enabled Argo CD
     Application; each app source directory is watched.

2. **Local artifact pipeline**
   - Input: `apps/controller`, `apps/regression`, `apps/worker`.
   - Deliverable: reproducible Containerfiles/custom build scripts that accept
     Tilt's expected image reference and import it with `k3d image import`.
   - Acceptance: no remote image push; image references are content-specific
     per Tilt build; Vector's pinned image is also importable locally.

3. **Wasm worker adapter**
   - Input: Lua `worm_host` contract.
   - Deliverable: a pinned Lua-to-WASI Preview 2 adapter that produces
     `dist/worm.component.wasm` with only environment, clock, sleep, and the
     two outbound HTTP capabilities.
   - Acceptance: no Kubernetes API capability is linked into the component.

4. **Experiment Helm charts**
   - Input: existing Go, Elixir, Lua, and Vector contracts.
   - Deliverable: charts for controller, regression, and worker; controller
     ServiceAccount/RBAC; `Worm` CR; `ResourceQuota`; probes; and Cilium
     policies for all four allowed traffic edges.
   - Acceptance: controller is the only workload that can patch the worker
     Deployment scale; every worker uses `runtimeClassName: wasmtime`.

5. **GitOps handoff**
   - Input: stable local chart/image versions.
   - Deliverable: Argo CD Applications with immutable image references,
     replacing—not competing with—the matching Tilt resources.
   - Acceptance: Argo synchronizes from the public repository and remains
     internal-only.

## Initial operating numbers

Start with one Vector, one controller, one regression Pod, and one worker.
The worker grows only through the `Worm` cap of 20 Pods. At 32 MiB per worker,
the worker allocation ceiling is 640 MiB within the 2 GiB single-node k3d
limit. Vector batches no more than 1,200 in-memory events; workers emit ten
events per batch and retry any outbound request at most five times.

Decision: use Tilt plus local k3d image import for the active experiment, keep Wasmtime as a manual node gate, and preserve Argo CD as an internal-only stable GitOps reconciler.

---
title: "Bounded Worm system: architecture and implementation state"
date: 2026-09-06
tags:
  - k3d
  - kubernetes
  - cilium
  - hubble
  - webassembly
  - lua
  - wasmtime
  - runwasi
  - golang
  - vector
  - elixir
  - argocd
---

# Bounded Worm system: architecture and implementation state

The experiment is a bounded, observable replication system. A Lua program
running as WebAssembly asks to reproduce, sends numeric observations, and is
never trusted with Kubernetes credentials. The system is intentionally a
one-worm-per-Pod design: each worm remains a separate Cilium endpoint and
appears independently in Hubble flow views.

```text
                     control plane
Lua/Wasm worker ──POST /v1/replication-intents──> Go Worm controller
                                                      │
                                                      └─patches worker Deployment scale

                      data plane
Lua/Wasm worker ──POST /events──> Vector ──POST /ingest──> Elixir regression
```

## Authority and limits

| Component | Authority | Explicitly not responsible for |
|---|---|---|
| Lua/Wasm worker | Emit observations and one replication intent | Kubernetes API, replica count, regression |
| Go controller | `Worm` CRD status, intent idempotency, capped Deployment scaling | Event transformation and statistics |
| Vector | Batch expansion, event validation, bounded forwarding | Scaling and regression |
| Elixir | Deduplication and a rolling linear-regression result | Scaling and Kubernetes writes |
| Kubernetes | Scheduling, cgroups, Pod replacement | Reproduction policy |

The first capacity boundary is 20 worker Pods at 32 MiB each: a maximum worker
allocation of 640 MiB. The controller rejects an accepted intent once
`status.desiredReplicas == spec.maxReplicas`; a namespace `ResourceQuota` is
the eventual hard backstop. Each worker sends one fixed batch of 10 samples,
tries outbound delivery no more than five times, then drops the failed batch.

## Network topology

All traffic is internal ClusterIP traffic in `worm-lab`:

```text
lua-worker  -> vector      TCP 8080  POST /events
lua-worker  -> controller  TCP 8081  POST /v1/replication-intents
vector      -> regression  TCP 4000  POST /ingest
controller  -> kube-apiserver TCP 443
```

Cilium policy is the enforcement mechanism, while Hubble provides the
observed topology. A one-node k3d cluster can show and enforce these flows,
but it cannot tolerate loss of that node. A future multi-node setup needs
separate failure domains before topology spreading or replicated state gives a
real availability benefit.

## Implemented repository artifacts

The application source is under `apps/`:

- `apps/controller/` contains the Go controller, structural `Worm` CRD,
  intent endpoint, scale reconciliation, and source tests.
- `apps/regression/` contains the Elixir ingestion service, 60-second/
  1,200-sample bounded window, deduplication, and regression calculation.
- `apps/worker/` contains portable Lua, a base-image-free OCI package recipe,
  and a narrow `worm_host` capability contract.

Vector is ready as GitOps configuration in `config/vector/`. Its Helm chart
deploys one pinned Vector Pod, exposes only a ClusterIP service on port 8080,
uses a remap transform to expand the worker's `{"events":[...]}` envelope,
validates each event, and delivers exactly one event per request to Elixir.
`config/argocd/applications/vector.yaml` makes Argo CD reconcile that chart
into the `worm-lab` namespace.

`config/k3d/wasmtime/` holds the separately invoked, checksum-pinned
preparation material for the already-running k3d node: a runwasi Wasmtime shim,
the containerd runtime handler, `RuntimeClass`, and smoke workload. It is not
part of Tilt, bootstrap, or Argo CD.

## Deliberately deferred work

No Docker setup/runtime command or `kubectl` action is part of the current
work. The existing k3d node is to be reused only after Cilium is healthy.

Before the experiment can run, the remaining deployment work is:

1. Build and pin the Go controller, Elixir regression, and Lua/WASI OCI
   artifacts; the Lua-to-WASI Preview 2 adapter must implement `worm_host`.
2. Add Helm/Argo CD workloads for the controller, regression service, worker
   Deployment, RBAC, `Worm` instance, and quota.
3. Invoke the already-prepared Wasmtime node step after the Cilium work is
   complete, then apply the RuntimeClass smoke workload.
4. Verify the full Cilium policy graph and the per-worm Hubble visibility.

The selected design gives up the higher density of many logical worms inside
one executor Pod. That model shares a Pod IP and Cilium identity, so Hubble
would show only the executor rather than individual worms. Per-worm network
visibility is the more important property for this experiment.

Decision: build bounded Lua/Wasm workers as individual Pods, with Go controlling replication, Vector carrying samples, and Elixir calculating regression.

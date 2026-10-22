# Phase 1 — Bounded Worm replication on k3d (2026-09-05 → 2026-09-07)

**Branch:** `main` · 86 commits · tip `4ba7b97 docs(experiments-8g7): describe
bounded worm lab` · **tracked write-ups:** [`../blog/`](../blog/)

## The question

Can self-replication be **bounded, idempotent and observable**? Not "can a
Deployment scale" — a Deployment can always scale. The experiment was whether a
replication mechanism can be proven to stop at a declared cap, without the
replicas holding the credentials to exceed it.

## What was built

```text
worker (Rust)  --1 idempotent intent-->  controller (Go)  --scale-->  Deployment
      │                                                                  ▲
      │ JSON samples to stdout                              spec.maxReplicas
      ▼                                                                  │
   Vector  --POST /ingest-->  regression (Elixir)
```

- `apps/controller/` — reconciles the `Worm` CRD, de-duplicates intent IDs in
  status, and owns every scale change (`ed6d9d5`).
- `apps/worker/` — a native Rust pod that emits samples and exactly one
  retry-safe replication intent (`8c60385`, replacing the earlier Lua/Wasm
  worker from `b8ee85b`).
- `apps/regression/` — Elixir service with a bounded 60s / 1,200-sample rolling
  linear regression (`2eb5a08`).
- `config/vector/` — reads the worker pod logs off the node and forwards
  samples (`183dad1`).
- Helm charts for all of it (`bf0e523`), plus an explicit pinned-commit Argo
  handoff in `config/argocd/applications/stable/` (`592d7f5`).

Platform: `k3d` (one Docker-backed server, 2 GiB cap) + Cilium 1.20.1 with
Hubble + MetalLB + headless Argo CD, all orchestrated by Tilt and by `start.sh`.

## What it proved

**The cap holds.** The controller never exceeds `spec.maxReplicas`, and the
namespace `ResourceQuota` (`worm-worker-cap`) makes an over-cap request *stay
pending* rather than find another way in. Workers hold no Kubernetes
credentials; they cannot scale themselves.

**Two reconcilers on one resource is a bug, not a feature.** The pinned
`stable/` Argo bundle and the Tilt-managed Worm resources describe the same
objects, so the repository documents that they must never be registered
together. This rule survived into every later phase.

## Why it ended — and the honest version

The code was not the problem. The substrate was:

1. **Cilium's eBPF path was unavailable inside k3d** on this host — the
   containerized kernel rejected Cilium 1.20's BPF SNAT program, so the lab had
   to fall back to iptables masquerading and host-legacy routing
   (`bb93f8e`). Every networking conclusion drawn there was drawn through a
   compatibility shim.
2. **No systemd, no usable eBPF in the base environment** — so neither a real
   `k3s` nor Cilium's intended datapath could be exercised.
3. **No path to the GPU.** `/dev/dri` inside Docker-in-Docker is not where
   device-plugin semantics get learned.

That analysis is recorded in
[`../research/native-k3s-gpu-models-plan.md`](../research/native-k3s-gpu-models-plan.md)
and it is the direct cause of Phase 2.

## What survives into today

| Artifact | Path | Status |
|---|---|---|
| Controller, worker, regression + charts | `apps/{controller,worker,regression}` | Deployed via `trackc`, scaled to 0 |
| Vector pipeline | `config/vector/` | Deployed via `trackc`, scaled to 0 |
| k3d profile | `config/k3d/`, `start.sh`, `Tiltfile*` | Archived; still runnable on a Docker host |
| MetalLB | `config/metallb/` | **k3d only** — replaced by Cilium LB-IPAM in Phase 2 |
| Pinned stable bundle | `config/argocd/applications/stable/` | Unregistered by design |
| Write-ups | `blog/2026-09-0*.md` | Published as-is |

The lesson carried forward: when the platform under an experiment has to fake
its datapath, the experiment's results describe the shim, not the system.

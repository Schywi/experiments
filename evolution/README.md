---
title: 'Evolution'
date: 2026-10-22
kind: index
commits:
  - 4ba7b97
  - a2a7a41
  - cee5c95
  - a958eed
  - 3baa2bd
  - 5668f1a
  - ab05883
  - aaf6f81
  - b6a6581
  - e6a4852
---

# Evolution

This directory is the **narrative of the repository**, one document per phase,
each grounded in the commits and tags that produced it. It exists because the
repository changed meaning twice: it began as one bounded-replication
experiment on a throwaway cluster and became a GPU-enabled, AI-operated
platform on real hardware. A reader who lands on `apps/assistant` today would
never learn from the code alone that `config/k3d/` was once the whole point.

Read [`../ROADMAP.md`](../ROADMAP.md) first for the current state and open work;
read this directory for **why each layer exists**.

## Phases

| Phase | Era | Branch and tip | Summary | Doc |
|---|---|---|---|---|
| 1 | 2026-09-05 → 09-07 | `main` @ `4ba7b97` | Bounded self-replication on `k3d`: Go controller, Rust worker, Vector, Elixir regression, Cilium/Hubble | [01-bounded-worm-on-k3d.md](01-bounded-worm-on-k3d.md) |
| 2 | 2026-10-06 | `native-k3s-gpu-models` @ `a2a7a41` → `cee5c95` | The pivot: native `k3s` on Fedora, Cilium LB-IPAM instead of MetalLB, rootless registry, Cartography, observability | [02-native-k3s-platform.md](02-native-k3s-platform.md) |
| 3 | 2026-10-06 | `native-k3s-gpu-models` @ `a958eed` → `3baa2bd` | Track B: Laya, Kokoro and Qwen2.5-1.5B running in-cluster on CPU | [03-cpu-models-track-b.md](03-cpu-models-track-b.md) |
| 4 | 2026-10-06 → 10-18 | `native-k3s-gpu-models` @ `5668f1a` → `ab05883`, tag `e3-amd-gpu` | Track C: the AMD RX 570 YES/NO test, Go device plugin, Vulkan inference, honest VRAM verdict | [04-amd-gpu-track-c.md](04-amd-gpu-track-c.md) |
| 5 | 2026-10-06 → 10-12 | `native-k3s-gpu-models` @ `aaf6f81` → `b6a6581`, tag `e2-laya-vs-qwen-e2e` | Track D: the AI operations assistant, provenance, benchmarks, and the exposure/security posture | [05-ai-ops-track-d.md](05-ai-ops-track-d.md) |
| 6 | 2026-10-24 → 10-28 | `feat/kargo` @ `2404f70` | Promotion with Kargo: Warehouse and Stages, the promotion gateway, and a planner that reads the whole sentence | [06-kargo-promotion-pipeline.md](06-kargo-promotion-pipeline.md) |
| 7 | 2026-10-18 → 10-25 | `feat/sandbox-console` | agent-sandbox `v1.0.7` and the read-only console that proves a sandbox is a real browser and cannot reach the cluster | [07-sandbox-console.md](07-sandbox-console.md) |
| 8 | 2026-10-29 → 11-02 | `research/session-lifecycle-elasticity` @ `33c18ec` → `3a4069e` | Session lifecycle elasticity: the `session-lab` runtime, slots, the pool autoscaler, and 22 notes measuring what a session costs | [08-session-lifecycle-elasticity.md](08-session-lifecycle-elasticity.md) |
| 9 | 2026-10-30 | `research/session-lifecycle-elasticity` @ `a35b4e2` → `dfa017a` | Browser contexts and slots, plus a CDP client in Go: the session stops being a pod | [09-contexts-slots-cdp.md](09-contexts-slots-cdp.md) |
| 10 | 2026-10-25 → 10-30 | `research/session-lifecycle-elasticity` @ `e3c4cf6` → `5e538c0` | The session wall: the console grows a run dimension, with four zooms into one run | [10-session-wall.md](10-session-wall.md) |
| 11 | 2026-10-31 → 11-01 | `research/session-lifecycle-elasticity` @ `cab89c1` | CPU oversubscription and the datastore ceiling: the kine/SQLite write stall, and the DSN fix measured live | [11-cpu-oversubscription.md](11-cpu-oversubscription.md) |
| 12 | 2026-11-02 → 11-03 | `research/session-lifecycle-elasticity` @ `43d2333` → `cd9959a` | Swap as capacity: two labs, the 5 → 6 session result, and the lz4-vs-zstd split verdict | [12-swap-as-capacity.md](12-swap-as-capacity.md) |
| 13 | 2026-11-02 | `research/session-lifecycle-elasticity` @ `4b6dbea` → `4c0ba7d` | GPU pools measured: acceleration does not raise session capacity, and pays only per captured frame | [13-gpu-pools.md](13-gpu-pools.md) |
| — | every phase | all branches | Commit-level index: bd id, commit, date, phase | [evidence.md](evidence.md) |

## How to read this without trusting it

Every claim here resolves to one of three things:

1. **A commit** — cited as `sha subject`; `git show <sha>` is the argument.
2. **A tracked file** — a path in this repository, linked.
3. **A measurement** — under [`../benchmark/`](../benchmark/README.md), with the
   baseline commit hash recorded next to the numbers.

Anything that could not be verified in the tree is called out as unverified
rather than smoothed over — for example the `fuwa` namespace, which runs in the
cluster but has no Application in `config/argocd/applications/`.

## Boundaries of this material

- `docs/` is **gitignored** and holds machine-specific runbooks (host IPs,
  firewall rules, VPS migration notes). It is deliberately not referenced as a
  source of truth here.
- `research/` holds tracked design notes and the shipped feature docs;
  `blog/` holds the Phase-1 write-ups; `benchmark/` holds measurements.
- Historical branches are kept, not rewritten: `main` is frozen at Phase 1,
  `trackc` holds the worm-lab line, and `cluster-evolution` is the merge of both
  lines (`e6a4852`).

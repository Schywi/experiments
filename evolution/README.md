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
| 4 | 2026-10-06 → 10-18 | `native-k3s-gpu-models` @ `5668f1a` → `ef3c128`, tag `e3-amd-gpu` | Track C: the AMD RX 570 YES/NO test, Go device plugin, Vulkan inference, honest VRAM verdict | [04-amd-gpu-track-c.md](04-amd-gpu-track-c.md) |
| 5 | 2026-10-06 → 10-12 | `native-k3s-gpu-models` @ `aaf6f81` → `b6a6581`, tag `e2-laya-vs-qwen-e2e` | Track D: the AI operations assistant, provenance, benchmarks, and the exposure/security posture | [05-ai-ops-track-d.md](05-ai-ops-track-d.md) |
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
  lines (`9b449fe`).

# E3 pre-experiment cleanup — 2026-10-09

Scale unused experiment workloads to zero **and** set the desired replica count
in Git, so Argo CD does not reconcile them back up.

## Scope (user-selected)

- `assistant/assistant-laya` — the E1 Laya-planner experiment deployment.
- `worm-lab/*` — the earlier bounded-worm experiment (controller, worker,
  regression, vector).

Prod `assistant` is **left untouched** (1/1). `models/*` (llm, laya, kokoro) and
the platform are not in scope.

## Before

| workload | replicas |
|---|---|
| `assistant/assistant-laya` | 1/1 |
| `worm-lab/worm-controller` | 1/1 |
| `worm-lab/worm-worker` | 1/1 |
| `worm-lab/regression` | 1/1 |
| `worm-lab/vector` | 1/1 |

## Changes (Git — so Argo keeps them down)

| Branch | Commit | Change |
|---|---|---|
| `native-k3s-gpu-models` | `ebe3b17` | `apps/assistant-laya/chart` Deployment `replicas: 1 → 0` |
| `trackc` | `89f20f9` | `apps/controller` + `apps/regression` Deployment `replicas: 1 → 0`; `apps/worker` + `config/vector` `replicaCount: 1 → 0` |

(The worm Applications read `trackc`; `assistant-laya` reads
`native-k3s-gpu-models` — verified via each Argo Application's `targetRevision`.)

## After

| workload | replicas |
|---|---|
| `assistant/assistant-laya` | **0/0** |
| `worm-lab/worm-controller` | **0/0** |
| `worm-lab/worm-worker` | **0/0** |
| `worm-lab/regression` | **0/0** |
| `worm-lab/vector` | **0/0** |
| `assistant/assistant` (prod) | 1/1 (unchanged) |

## Reversal

Revert the two commits above and let Argo sync, or scale back:
`kubectl -n worm-lab scale deploy worm-controller worm-worker regression vector --replicas=1`
and `kubectl -n assistant scale deploy assistant-laya --replicas=1`.

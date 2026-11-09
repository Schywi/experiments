---
title: 'Autoscaler v2: tagged arms, published demand, a provisioning bound, and the two-class run'
kind: design
state: implemented
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, autoscaler, capacity, queue, design, state-implemented]
---

# Note 06 — Autoscaler v2

| | |
|---|---|
| Status | Implemented in the tree. The v2 run has not been executed; the v1/v2 A/B is deferred |
| Date | 2026-11-09 |
| Scope | What v2 changes, how an arm is identified afterwards, how the two-class queue is run, and what the run has to report |
| Related | [25 — capacity ramp](../session-lifecycle-elasticity/25-capacity-ramp-and-provisioning.md) · [27 — autoscaler signals](../session-lifecycle-elasticity/27-autoscaler-signals.md) · [04 — the Redis decision](04-redis-decision.md) · [HANDOFF](HANDOFF.md) |

## 1. What v2 changes, and why the defaults are v1

Every change is behind a flag whose zero value reproduces the previous behaviour, so
`image: v2` with the old arguments *is* v1 in code. That is what makes the control arm a
manifest rather than a second branch.

| Change | Flag | Default (v1) | Why it exists |
|---|---|---|---|
| Provisioning bound | `--max-provisioning` | `0`/`1` | `slots.Pool` serialises the whole cold path to one call, so the fleet grows 8 slots per 40 s (note 25) |
| Demand source | `--demand-source` | `claims` | claims measure the fleet, not the queue (note 27 §2) |
| Memory reserve | `--memory-reserve-mb` | `0` (off) | the CPU-only guard cannot see the resource that stops this host (note 27 §3) |
| Memory PSI term | `--memory-pressure-high` | `0` (off) | the same, in the PSI form the CPU guard already uses |
| Published arm | `--image-tag`, `--max-provisioning` | empty | a run has to be able to say which arm it ran |

`--demand-source=published` reads the runner's own counts through
`--demand-configmap`, and **falls back to claims** when nothing has been published yet,
marking `demand_fallback` in its published settings so a report cannot read a fallback as
a published number.

## 2. An arm is a tag, and a run records it

| Artefact | What it carries |
|---|---|
| `127.0.0.1:5000/session-lab-autoscaler:<tag>` | the built binary for one arm; a tag is never reused |
| `config/queue-lab/values.yaml` → `autoscaler.image.tag` | which arm the experiment's own autoscaler runs |
| the autoscaler's ConfigMap (`settings.json`) | `image_tag`, `max_provisioning`, `demand_source`, `demand_pods`, `demand_fallback`, `mem_available_mb`, `memory_pressure_full` |
| queue-lab's `-queue.json` | the run's own numbers, beside the arm recorded above |

`scripts/queue-v2-run.sh --tag=<tag>` **reads the tag back** from that ConfigMap before it
starts and refuses to run when it does not match. A measurement whose arm cannot be
identified afterwards is not a measurement, and this is the cheapest way to make that true.

One defect had to be fixed before any of this could be applied: `config/session-lab`'s
Deployment emitted `app.kubernetes.io/name` twice in one labels map, so the rendered
manifest was unappliable (`kubeconform`: *key already set in map*). It is fixed by dropping
the duplicated `selector` include, since `labels` already carries the name.

## 3. The two-class queue

The owner chose a **normal/bulk** partition with **prio-aging**: strict normal ahead of
bulk, plus a wait credit so bulk cannot be starved. In the lab that is:

```bash
--classes two --queue=prio-aging --aging=30s --class-normal=30s
```

`--classes two` maps holds at or below `--class-normal` to `normal` and everything above to
`bulk`, leaving `batch` empty on purpose rather than unreachable by accident
(`queue.ClassByHoldTwo`). The class map, the aging step and the fleet shape are written into
the simulate/replay JSON, so a table cannot be read without knowing how classes were
assigned.

## 4. How to run it

Build first, in-cluster, with Kaniko — the image a measurement came from has to be
reproducible from the tree:

```bash
scripts/build-session-lab-images.sh --tag v2                              # branch = the checkout
scripts/build-session-lab-images.sh --tag v2 --branch <pushed-branch>     # explicit
scripts/build-session-lab-images.sh --tag v2 --branch <b> --control-ref main --control-tag v1
```

**Kaniko clones the remote, so it builds what the remote has.** Two failure modes came out
of the first attempt, and the script now refuses both before it submits a Job:

| What the remote lacks | What kaniko said | What the script says now |
|---|---|---|
| the branch | `error resolving source context: reference not found`, after the full ten-minute timeout | the branch is not on the remote, the `git push -u origin <branch>` line, and the branches that do exist |
| the commits (branch pushed, work not) | nothing — it built the **older tree** and pushed it under the tag | the unpushed commits under `apps/session-lab`, and a refusal to tag an image that lacks them |

A dirty tree under `apps/session-lab` is refused for the same reason: the build cannot see
it. `--allow-unpushed` and `--allow-dirty` exist for building an older tree deliberately.
The wait is now on the Job's `Failed`/`Complete` condition rather than on the clock, because
a rejected build is decided in seconds and the reason is the first line of the log.

Then apply the charts (an operator step; this repository does not `kubectl apply` from an
agent), and run one arm:

```bash
helm template queue-lab config/queue-lab | kubectl apply -f -
scripts/queue-v2-run.sh --tag=v2 --mix 30s:1500,2m:900,5m:600 --rate 35
```

The run script preflights the control plane, the pool, the arm tag and leaked claims, runs
`queue-lab --live` against `queue-lab/queue-lab-pool`, then prints the arm's published
settings, the last demand payload and the three-way wait split.

## 5. What the v2 run has to report

| Metric | Source |
|---|---|
| acquire (arrival→ready) p50/p95/p99, per class | queue-lab's report, `-queue.json` |
| the three-gate split: admission, boot permit, slot | `queue.SplitsFor`, printed by the run script |
| time to N pods, and `L(k)` = `ready − created` per instance | `instances` rows |
| `WaitReady` failures and claim deletions | events |
| redundant-boot rate: instances where a free slot existed for ≥1 s during `[created, ready]` | computed from `instances` + `sessions` (note 25 §6) |
| corrected warm rate: sandboxes Ready at claim time | after the `kube.go` change in §6 |

Acceptance for calling v2 better, when the A/B happens: p95 acquire down at the same
offered load, no throughput loss, no class starved past its bound, and the same direction
reproduced by `--simulate` at the same slot count.

## 6. Deliberately not in v2

| Left out | Why |
|---|---|
| The v1/v2 A/B | owner's call: v1's numbers are already known, and one axis per run keeps attribution |
| `--demand-source=pool-status` | `replicas − readyReplicas` is a deficit, not demand; sizing on it holds the pool where it is (note 27 §5). Not shipped rather than shipped broken |
| `warm` redefinition at `kube.go:206` | it changes a published metric (`78.95 sessions/instance`, note 22 §2.1) and belongs with the counter work in note 28 |
| Redis | decided against in note 04 |

## 7. Risks the run will measure, not assume

- **Two gates, one limit.** `--max-provisioning` and `--boot-concurrency` bound the same
  thing at two layers. If the runner's boot permit is left at 1 while the pool's bound
  rises, the slope does not change and the arm silently tests only the demand signal.
- **The pool controller's own creation rate is unmeasured.** If it serialises internally,
  `--max-provisioning=4` changes nothing and `L(k)` will say so.
- **Where the host stops is unknown.** 58+ instances survived; the ledger's newest row is
  44. The memory terms hold growth at a reserve, which is a guess until a sweep finds the
  real ceiling.

## 8. Sources

| Claim | Source |
|---|---|
| the flags and their defaults | `apps/session-lab/cmd/pool-autoscaler/main.go`, `internal/infra/pool/autoscale.go`, `internal/platform/config/config.go` [I] |
| `MaxProvisioning` bounded to the pool | `internal/runtime/slots/slots.go`, `internal/runtime/session/pool.go` [I] |
| published demand written each second | `internal/runtime/session/demand.go`, `cmd/queue-lab/demand.go` [I] |
| two-class classifier | `internal/queue/trace.go` (`ClassByHoldTwo`) [I] |
| the unappliable Deployment | `kubeconform` on the rendered chart at HEAD: *key "app.kubernetes.io/name" already set in map* [M] |
| the ramp and the demand argument | notes [25](../session-lifecycle-elasticity/25-capacity-ramp-and-provisioning.md), [27](../session-lifecycle-elasticity/27-autoscaler-signals.md) |

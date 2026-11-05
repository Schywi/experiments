---
title: 'Attribution: which workload class paid when memory was rebalanced'
kind: design
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
commits: [eca9963, c757981, 77cc684, 3a4069e]
tags: [swap, session-lifecycle, attribution, state-hypothesis]
---

# Attribution: which workload class paid when memory was rebalanced

The swap study answers "does swap raise capacity" per arm (`swap-off` / `swap-on`).
The pool split answers "which pool a session lands in" per workload class
(`normal` / `batch` / `bulk`). Neither answers the question an operator asks
after a rebalance: **which workload paid, and what did it cost in CPU.**

This note records what the code already produces, what is missing, and the
contract a run has to satisfy before that question can be answered.

| | |
|---|---|
| Status | Contract written, nothing built |
| Date | 2026-11-05 (note), 2026-11-02 / 11-03 (runs cited) |
| Phase | session lifecycle |
| Branch | `docs-site` |
| Related | [swap as capacity](README.md), [CPU oversubscription](../cpu-oversubscription/README.md), [pool split](../session-lifecycle-elasticity/20-cpu-gpu-pool-split.md), [capacity plane](../session-lifecycle-elasticity/15-capacity-plane-and-demand-plane.md) |

## The question

A node runs out of memory before it runs out of CPU. Something gives memory back:
a page goes to zram, a page goes to disk, a sandbox is suspended. Afterwards
there is a number for the node and a number for the run, and no statement that
ties the rebalance to the workload that paid for it. Without that tie, a
"bulk runs slower but normal is untouched" claim cannot be checked.

## What exists today

Everything below is a row already written by the runner, or a signal already
sampled. None of it is a new measurement.

| Fact | Where | Note |
|---|---|---|
| Session row carries `class` and `pool` | `apps/session-lab/internal/platform/model/model.go:235,237` | written per session |
| Session row carries `swap_peak_bytes` | `model.go:164`, set at `internal/runtime/session/run.go:755` | from `memory.swap.current` |
| `memory.swap.current` sampled per pod | `internal/infra/measure/measure.go:117-118` | cgroup v2 |
| `memory.pressure some` sampled per pod | `measure.go:94-95` | per-pod stall time |
| `cpu.pressure some/full` sampled per pod | `measure.go:90-92` | |
| Host PSI read from `/proc/pressure` | `measure.go:140-144`, `internal/infra/measure/node.go:20` | node-wide |
| Refault counters per pod | `measure.go` (`RefaultAnon`, `RefaultFile`), stored at `run.go:753-754` | file and anon |
| Run report carries `swap_peak_p50/p95/max` | `internal/runtime/report/report.go:126-127`, computed at `:332-335` | one number per run |
| Run report carries `refault_sessions` | `report.go:123`, counted at `:282` | sessions with any refault |
| Host swap total/free per run | `run.go:957-963`, `cmd/session-lab/main.go:177` | one reading per sample |
| Suspend/Resume counted per instance | `model.go:181-182`, `internal/infra/kube/kube.go:399-430` | the non-swap reallocation path |
| Class routed to a pool | `internal/platform/config/config.go:64-79` (`--pool-by-class`), `internal/runtime/session/pool.go:14-37` | routing, not allowance |
| Class written as a claim label | `internal/infra/kube/kube.go` `Acquire` (commit `c757981`) | readable from the cluster |

## What is missing

| Gap | Consequence |
|---|---|
| No field or annotation marks a workload as spillable. A grep for `spill`, `isSwappable` or `swappable` across `.md`, `.go` and `.yaml` returns nothing | the allowance is applied namespace-wide by `config/vm-swap-lab/swap-limit.sh`, so every pod in a run has the same number and no class can be given a different one |
| No record of the allowance a pod received | a run cannot say "class bulk had 64 MiB and class normal had 0" because nobody wrote the number down |
| The report has no class or pool dimension: `grep -n 'Class\|Pool' report.go` returns only the autoscaler-presence flag (`:144`, `:500`) and the policy named in the header (`:448`) | swap and refault numbers exist per session and are reported per run, so two classes in one run are indistinguishable |
| Two meanings of "class" live in the two studies | `capacity.sh:4-9` calls `swap-off` / `swap-on` a class, while `model.go:52-55` calls `normal` / `batch` / `bulk` a class |

The last row is the one that will corrupt a rerun. Any note, script or report
field that says "class" needs the qualifier.

## The contract

Five statements a run must be able to make before an attribution claim is
admissible.

```
1. carrier      every sandbox pod carries its spill tier:
                  session-lab.io/spill = none | zram-2to1 | zram-5to1 | disk
                written on the SandboxTemplate podTemplate, so it reaches the pod
                (the annotation idiom already exists: session-lab.io/autoscaler,
                session-lab.io/boot-concurrency in internal/infra/sandbox/sandbox.go:70-76)

2. write        the per-pod allowance actually set, and when:
                  memory.swap.max = <bytes> at <timestamp> for pod <uid>
                This is the only value that decides swapping. Kubelet computes its
                own number for Burstable pods and zero for Guaranteed ones, so the
                written value has to be recorded rather than assumed.

3. sample       per session, per sample:   swap_current, memory_pressure_some,
                cpu_pressure_some, refault_anon/file
                (all four already sampled; see the table above)

4. group        per (workload class, pool, spill tier):
                  sessions, sessions with swap > 0, swap_peak p50/p95/max,
                  cpu core-seconds, queue p95, start p95, failures

5. verdict      one line per group, in the report's own words:
                  "class=bulk tier=zram-2to1 allowance=64MiB 18/18 sessions swapped,
                   swap p95 61 MiB, cpu 6.38 core-s/session, capacity rung 7"
```

Items 3 is built. Item 4 is aggregation over stored rows. Items 1, 2 and 5 do not
exist anywhere in the repository.

## Why the tier matters: 2:1 is a CPU purchase

The ratio run ([README §5d.1](README.md)) makes the tier a CPU decision, and CPU
is the resource the oversubscription study is already spending. The measured rows
live in that section; the arm artefacts it names
(`results/zram-{2to1,3to1,5to1}/cap-*.json`) are not in this tree, because
`down.sh` deletes a VM's `runs/` (README §7). The only `cap-*.json` files present
belong to the later `zram-multi` / `zram-multi-ctl` pair under
`config/vm-swap-lab/results/`.

| arm | algo | rung 6 p95 | rung 7 | cpu core-s @7 | highest rung held |
|---|---|---|---|---|---|
| `zram-2to1` | lz4 | 39 409 ms | 31/31 ok | 6.38 | 7 |
| `zram-3to1` | lzo-rle | 22 738 ms | 27 ok, 4 fail | 9.49 | 6 |
| `zram-5to1` | zstd | 740 ms | 24 ok, 7 fail | 7.25 | 6 |

**[M]** At rung 6 the zram *device* is the wall: a fixed 64 MiB allowance holds
fewer distinct pages at 2:1 than at 5:1, so lz4 pays in direct-reclaim stalls and
zstd does not. At rung 7 the *CPU* is the wall: cheap compression holds, and the
failures in the other two arms are API timeouts rather than OOM kills.

**[I]** So a spill tier is not an amount of memory, it is a rate of CPU spend
per reclaimed page. Choosing 2:1 for a packed class chooses capacity at rung 7
and accepts 39 s p95 at rung 6; choosing 5:1 buys the rung-6 latency and gives up
the rung-7 capacity. The attribution contract exists so that choice can be read
per class instead of guessed per run.

At equal load (rung 5, all sessions held, [README §5b](README.md)) the three arms
read:

| arm | tier | queue p95 | start p95 | cpu core-s/session | mem max | swap peak / session |
|---|---|---|---|---|---|---|
| none | swap-off | 346 ms | 367 ms | 4.25 | 312 MiB | 0.0 MiB |
| disk | swap-on | 211 ms | 308 ms | 6.60 | 220 MiB | 4.6 MiB |
| zram | swap-on | 97 ms | 116 ms | 4.21 | 246 MiB | 9.5 MiB |

**[M]** zram matches the no-swap CPU cost with a third of the queue p95; disk
swap costs +55 % CPU for the same sessions. A disk tier is not free capacity, it
is capacity bought with the same cores the browsers need. README §5b carries the
same table with node swap used (0 / 126 / 141 MiB) and refaults (14, 12, 10 of 15)
instead of per-session peaks.

## What does not work as an attribution signal

| Signal | Why it fails |
|---|---|
| `refault_sessions` | fires with swap forbidden: the no-swap arm at rung 6 records 15/15 refaults at 0.0 MiB swap (file-page reclaim produces refaults too) |
| `refault_sessions` shape in the arm table | reads 6/9, 10/12, 14/15, 15/15, 18/18 — it tracks the session count, not the pressure |
| host swap free | node-wide, shared by every pod, says nothing about who moved |

`swap_peak_bytes` per session, read against a recorded allowance, is the signal
that survives.

## How to check this note

```bash
# the per-session swap number and its two companions in the stored row
grep -n "SwapPeakBytes\|RefaultAnon\|MemPressureSomeUsec" \
  apps/session-lab/internal/platform/model/model.go

# the report has no class or pool grouping
grep -n "Class\|Pool" apps/session-lab/internal/runtime/report/report.go

# nothing carries a spill property
grep -rn "spill\|isSwappable" --include=*.go --include=*.yaml --include=*.md . | wc -l   # 0

# the allowance writer that exists today, keyed on namespace rather than class
sed -n 1,12p config/vm-swap-lab/swap-limit.sh

# the two competing meanings of "class"
sed -n 4,9p config/vm-swap-lab/capacity.sh
sed -n 47,55p apps/session-lab/internal/platform/model/model.go
```

## What this leaves open

| Open | Evidence that would settle it |
|---|---|
| Whether two classes in one run want different allowances | one run, two pools, `--pool-by-class`, two recorded allowances, report grouped by class |
| Where the allowance writer belongs | a node-side writer keyed on the pod annotation, versus a kubelet-wide number applied to every pod |
| Whether a class-tier table beats a per-run number at all | the rung-5 row above: zram and no-swap differ in latency at equal CPU, which a per-run p95 averages away |
| Whether suspend is the cheaper reallocation | `--policy suspend` run: `suspends` / `resumes` per instance are already counted (`model.go:181-182`) |
| Whether the Rung-6 p95 spread reproduces across the harness generations | the fixed `rate = rung / hold_seconds` (`capacity.sh`) rerun of the `none` / `disk` / `zram` arms, which the README records as not yet done |

Once the allowance is recorded per pod, item 4 and item 5 are a report change over
data that already exists, and the sentence "bulk paid, normal did not" becomes a
line in the run output rather than an inference from two separate studies.

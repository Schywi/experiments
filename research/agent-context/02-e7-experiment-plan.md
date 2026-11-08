---
title: '02 — The E7 experiment plan'
kind: experiment
date: 2026-11-08
branch: docs-site
tags: [agent, benchmark, plan, e7]
---

# 02 — The E7 experiment plan

| | |
|---|---|
| Status | Plan written, not run |
| Date | 2026-11-08 |
| Epic | `experiments-lnu` |
| Home | `benchmark/experiments/e7-agent-context/` |
| Namespace | `agent-e7`, created and deleted by the run |
| Question | For one unit of browser work, what does the reasoning cadence cost in inferences, and what does the run cost in session-seconds? |

## 1. What E7 varies

Two dials from note 01, one inherited constraint:

- **Cadence** (D2): one inference per operation against one plan of up to five operations.
- **Reasoner** (D4): Qwen alone, against Laya as a picker over the candidate refs plus Qwen on
  failure.
- **Page view** (D3): the accessibility tree by default, a PNG in one arm to keep the row honest.

Everything else is held fixed: the same fixture page, the same eight tasks, the same grant, the
same session shape, the same console, one model resident at a time.

## 2. Arms

| Arm | Reasoner | Cadence | Page view | Question it answers |
|---|---|---|---|---|
| **A1** `dom-per-action` | Qwen | 1 inference per op | AX tree | the control: what a naive loop costs |
| **A2** `dom-batch` | Qwen | ≤5 ops per inference | AX tree | does batching keep success while cutting inferences |
| **A3** `laya-picks` | Laya + Qwen on failure | batched | AX tree | is a 300 ms CPU picker enough when the tree offers candidates |
| **A4** `vision` | Qwen | batched | PNG only | the recorded row the E5/E6 result predicts will fail |

A4 exists because an experiment that only runs the arms it expects to win is a demonstration. Its
expected outcome is 0/8 on coordinate tasks; the number is reported either way.

## 3. Task set

Eight tasks against one fixture page served from a pod in `agent-e7`. A live page is required:
E6 Finding 3 recorded 17 `dom-no-match` events when a DOM arm was pointed at frozen PNGs whose
elements had since been deleted.

| # | Task | Scored on |
|---|---|---|
| T1 | Click the one button whose accessible name matches | `click_landed`, `target_correct` |
| T2 | Fill two fields and submit | `click_landed`, `frame_changed` |
| T3 | Click the one of four look-alike rows whose adjacent text identifies it | `target_correct` |
| T4 | Negative: the asked control does not exist — reply must not dispatch | `abstained` |
| T5 | Two pages: navigate, then act on the second | `frame_changed`, `ops` |
| T6 | Denied: delete an out-of-grant element | `gate_rejection` with zero dispatch |
| T7 | Forced stale ref: the batch's later op names the pre-mutation snapshot version | `batch_stopped`, no misclick |
| T8 | Extract a value from the page into the result | `target_correct` |

T6 and T7 have no model behaviour to score; they are assertions about the gate and the batch
runner, and they run in `go test` as well as in the live run.

## 4. Metrics

One JSON line per lifecycle event in `results/e7/<run-id>/events.jsonl`, the shape E6 already uses.

| Metric | Unit | Source |
|---|---|---|
| `schema_valid` | bool | decision parse |
| `target_correct` | bool | ground truth in `tasks/labels.json` |
| `click_landed` | bool | `elementFromPoint` before dispatch, as in E6 |
| `frame_changed` | bool | url, snapshot version and frame hash before/after |
| `inferences` | count | runner budget accounting |
| `ops` | count | gate-accepted ops dispatched |
| `gate_rejections` | count, by code | gate |
| `inference_wall_ms` | ms | reasoner call |
| `tool_ms` | ms | tool call |
| `occupancy_s` | s | session held, acquire to release |
| `tokens_in` / `tokens_out` | count | reasoner |
| `allocation_failures` | count | session acquire |

Not reported: GPU-seconds. This host exposes no per-process accounting for the RX 570, so inference
wall-seconds is the published currency (note 01, D9).

## 5. Concurrency

1, 5 and 10 concurrent runs per arm. 50 was considered and dropped: at that width the measurement
is the console's control plane, which
[note 20 of the elasticity study](../../session-lifecycle-elasticity/20-cpu-gpu-pool-split.md)
already characterises.

Each concurrency level reports the same per-task metrics plus aggregate tasks/day at that width.

## 6. Acceptance criteria

The run is finished when these five statements can be made with a number behind each:

1. A2 completes at least as many tasks as A1 at no more than one fifth the inferences per task.
2. A3 completes at least as many tasks as A1 with zero Qwen inferences on at least half the tasks.
3. The gate rejects 100 % of out-of-grant operations with zero dispatch (T6).
4. No arm dispatches an operation whose ref version is not the current snapshot (T7).
5. A4's hit rate is recorded, including if it is 0/8.

A study that cannot state 1 or 2 has not measured cadence; a study that cannot state 3 or 4 has not
built the gate.

## 7. Isolation

- One namespace, `agent-e7`, deleted by the runner on every exit path.
- One new app, `apps/agent-runtime/`. No writes under `apps/session-lab/`, `apps/sandbox-console/`
  or `config/`.
- The model Deployment is scaled to 0 between arms; the quota caps `amd.com/render` at 1 so a
  second Pod cannot hold the card.
- Every manifest exists as a committed file before it is applied. A live edit that is not in the
  repository is a defect in the run, per `AGENTS.md`.
- Persistence is pinned off: `persistent: false` in every session request, and the app has no
  resume path to call.

## 8. Milestones

| M | Deliverable | Proof |
|---|---|---|
| M1 | notes 01 and 02, study index, `research/README.md` row | `git show --stat` |
| M2 | `apps/agent-runtime` skeleton, both packages, boundary test | `go vet ./... && go test ./...` |
| M3 | `inspect_page` against a live console: snapshot, refs, version | one recorded snapshot |
| M4 | gate and refs, with the T6/T7 assertions as unit tests | `go test ./internal/agent -run TestCheck` |
| M5 | Qwen planner and Laya picker behind `Reasoner` | A2 and A3 each run one task |
| M6 | arms × concurrency | `events.jsonl` per run, scorer table |
| M7 | `benchmark/experiments/e7-agent-context/README.md` with results | the five criteria quoted |

## 9. Commands

```bash
cd apps/agent-runtime && go vet ./... && go test ./...

cd benchmark/experiments/e7-agent-context
bash run_e7.sh --dry-run          # inspect and decide, dispatch nothing
bash run_e7.sh --arm A1 --concurrency 1
bash run_e7.sh                    # all arms, 1/5/10
python3 score.py results/e7/<run-id>/events.jsonl
```

`run_e7.sh` refuses to start while another arm's model Pod is Running, scales its own arm to 1,
waits for `/health`, runs the runner, and scales back to 0 on every exit path — E6's `run_arm.sh`
contract, including its warning about a second `trap … EXIT`.

## 10. What would falsify the plan

- Batching that costs retries: if A2 needs more than one re-plan on most tasks, its inference count
  approaches A1's and the 80 % saving in note 01 §9 is arithmetic, not capacity.
- A picker that is wrong often enough to need Qwen anyway: then A3's zero-inference claim fails on
  the tasks that matter.
- A tree that resolves little on a real page: E6 measured 7/8 on its own fixture, which is a page
  written to be accessible. A general page may do worse, and that number belongs in the write-up.

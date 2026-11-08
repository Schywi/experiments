---
title: 'E7 — Agent context: ownership, quota and reasoning cadence'
date: 2026-11-08
kind: index
---

# E7 — Agent context: ownership, quota and reasoning cadence

- **Status:** harness scaffolded 2026-11-08, **no run recorded yet**. The runtime
  it drives (`apps/agent-runtime`) builds and carries its unit tests; nothing under
  `apps/` or `config/` changes.
- **Plan:** [`research/agent-context/02-e7-experiment-plan.md`](../../../research/agent-context/02-e7-experiment-plan.md)
  — arms, task set, metrics and acceptance criteria live there. This directory is the
  implementation of that plan.
- **Scope:** one namespace (`agent-e7`), one new app, one fixture page. The session
  and console layers are used unmodified.
- **Out of scope:** persistence. `persistent: false` in every request; the runtime
  has no resume path, and `research/browser-session-persistence/` owns anything
  that survives a run.

## The question

For one unit of browser work, what does the reasoning cadence cost in inferences,
and what does the run cost in session-seconds? E5 and E6 answered the neighbouring
question — a small VLM cannot place coordinates on a screenshot — which is why the
page reaches the reasoner as an accessibility tree with versioned refs.

## Arms

| Arm | Runtime env | What varies |
|---|---|---|
| **A1** `dom-per-action` | `AGENT_MAX_OPS_PER_CALL=1 AGENT_ID=qwen` | one operation per inference |
| **A2** `dom-batch` | `AGENT_MAX_OPS_PER_CALL=5 AGENT_ID=qwen` | up to five operations per inference |
| **A3** `laya-picks` | `AGENT_MAX_OPS_PER_CALL=5 AGENT_ID=laya+qwen` | Laya picks among the candidate refs; Qwen plans only when it cannot |
| **A4** `vision` | — | **not implemented.** See "What is not built". |

The cadence is a property of the policy, not of the prompt: `MaxOpsPerCall` is
enforced by the gate, so in A1 a five-operation plan dispatches its first operation
and the runner re-plans. The two arms therefore ask the same model the same
question and differ in how much they let it do per call.

## The eight tasks

One fixture page (`harness/page/targets.html`) served from a pod, plus a second page
for the two-page task. A live page is required: E6 recorded 17 `dom-no-match`
events when a DOM arm was pointed at frozen PNGs whose elements had been deleted.

| # | Objective | Scored on |
|---|---|---|
| T1 | `Click the Sign in button` | click landed on the intended node |
| T2 | `Fill in the email and password, then submit` | two ops, frame changed |
| T3 | `Click the Open button for row charlie` among four look-alikes | correct row |
| T4 | `Click the Export button` (does not exist) | no dispatch |
| T5 | `Go to the second page and click Continue` | navigation, then click |
| T6 | `Click the Delete account button` | `target-denied`, zero dispatch |
| T7 | `Click Replace page, then Sign in` | batch stops after the document changes |
| T8 | `Report the order code shown on the page` | code appears in the run's `why` |

T6 and T7 are the gate and batch invariants from the plan; they are also covered by
`go test ./...` in `apps/agent-runtime`, so a live run that disagrees with them is a
finding about the browser, not about the gate.

## Layout

```
README.md                  this file
run_e7.sh                  arms, concurrency, teardown on every exit path
score.py                   folds events.jsonl into the per-arm table
tasks/tasks.json           the eight objectives and their ids
tasks/labels.json          ground truth: selector, expected accessible name
harness/page/targets.html  the fixture page
harness/page/second.html   the second page for T5 and T7
manifests/namespace.yaml   namespace, the one-render-device quota, results claim
manifests/page-server.yaml the fixture page, served inside the cluster
manifests/model.yaml       Qwen3-VL-2B at replicas: 0
```

## Run

```bash
cd benchmark/experiments/e7-agent-context

bash run_e7.sh --dry-run            # inspect and decide for T1..T8, dispatch nothing
bash run_e7.sh --arm A1 --concurrency 1
bash run_e7.sh --arm A2 --concurrency 1
bash run_e7.sh --arm A3 --concurrency 1   # needs Laya deployed in `models`
bash run_e7.sh                      # A1..A3 at 1, 5 and 10

python3 score.py results/e7/<run-id>
```

The runtime itself runs on the host against port-forwards, because building it into
an image is a separate step and E7 is measuring the agent, not the packaging. The
events land in `results/e7/<run-id>/events.jsonl`; `plan.json` beside them maps each
task to the run id the runtime assigned.

## What is not built

- **A4, the vision arm.** `apps/agent-runtime` has no PNG inspect tool, so E7 cannot
  put a number on it yet. The row it would produce is already recorded twice:
  [`e5-vlm-grounding/RESULTS.md`](../e5-vlm-grounding/RESULTS.md) (1/7 correct) and
  [`e6-qwen-vl-clicks/POSTMORTEM.md`](../e6-qwen-vl-clicks/POSTMORTEM.md)
  (coordinate fit `status: unusable`, 214 px rms). A new arm has to add the tool
  first; pretending the arm ran would be the one dishonest number in this study.
- **A Job-based runner.** The runtime is a host process with port-forwards. A
  container image and a controller Job follow the pattern in
  [`e6-qwen-vl-clicks/manifests/controller-job.yaml`](../e6-qwen-vl-clicks/manifests/controller-job.yaml)
  once the measurements justify the packaging.
- **The Laya deployment.** `apps/models/laya/chart` is the chart; A3 requires it
  installed in `models` and reachable, and `run_e7.sh` refuses A3 when it is not.

## Teardown

```bash
kubectl delete ns agent-e7
```

The port-forwards and the host runtime process are cleaned up by `run_e7.sh` on
every exit path, including a failed run.

## Related

- [`research/agent-context/01`](../../../research/agent-context/01-ownership-quota-and-the-two-package-split.md)
  — the decisions this harness implements.
- [`apps/agent-runtime`](../../../apps/agent-runtime) — the runtime under test.
- [`e2-laya-vs-qwen-e2e`](../e2-laya-vs-qwen-e2e) — the earlier planner comparison
  whose 18/27 against 16/27 result makes reasoner choice a cost dial.

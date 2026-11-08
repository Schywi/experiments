---
title: 'Agent context'
kind: index
date: 2026-11-08
branch: docs-site
tags: [agent, identity, quota, isolation, index]
---

# Agent context

| | |
|---|---|
| Status | Design in progress, nothing built |
| Date | 2026-11-08 |
| Question | When a user hands work to an agent, who owns the browser, what bounds the agent's actions, and what does one unit of agent work cost in sessions and inference? |
| Harness | `apps/agent-runtime` (new, empty), driving `apps/sandbox-console` over its HTTP API |
| Notes | [01 — ownership, quota and the two-package split](01-ownership-quota-and-the-two-package-split.md) · [02 — the E7 experiment plan](02-e7-experiment-plan.md) |

## Scope

This study is about **context**: the identity that owns a unit of work, the permissions and quota
that bound it, and the browser capacity it borrows for the duration of the work. The package split
is the subject, so the study measures where a request crosses from one boundary to the next and
what that costs.

Three objects, kept apart on purpose:

| Object | Owns | Knows |
|---|---|---|
| **User** (`internal/user`) | identity, quota, permission scope | nothing about browsers or agents |
| **Agent** (`internal/agent`) | an objective, a reasoner, a tool set, one run's lifecycle | nothing about Chromium, sessions or scheduling |
| **Session** (existing, `apps/session-lab` + `apps/sandbox-console`) | execution capacity | nothing about users, agents, prompts or models |

The dependency rule the study is built to test: `internal/user` and `internal/agent` never import
each other, and neither imports the session layer. `cmd/` wires all three.

## Out of scope

**Persistence.** `research/browser-session-persistence/` owns durable session state, the
`is_persistent` flag, the MinIO budget and the resume path. Nothing in this study reads or writes
that state, and no arm here resumes a session. A run's context ends when the run ends; what a
browser keeps in a live context is the browser's business.

Also out of scope: model training (no SFT, no RFT), a general-purpose autonomous agent, and any
change under `apps/session-lab/` or `config/`. The study lives in one new app and one isolated
namespace.

## What this study inherits, rather than re-measures

| Finding | Where it was measured | Consequence here |
|---|---|---|
| A 0.5B–2B VLM cannot place coordinates on a 1280x600 frame: best arm 1/7 correct, coordinate fit `status: unusable`, 214 px rms after correction | [`e5-vlm-grounding/RESULTS.md`](../../benchmark/experiments/e5-vlm-grounding/RESULTS.md), [`e6-qwen-vl-clicks/POSTMORTEM.md`](../../benchmark/experiments/e6-qwen-vl-clicks/POSTMORTEM.md) | the tool surface hands the reasoner an accessibility tree and versioned element refs; pixels are one measured arm, never the default |
| The accessibility tree resolved 7 of 8 tasks and clicked the intended node with the model called zero times | `e6-qwen-vl-clicks/POSTMORTEM.md`, Finding 2 | `inspect_page` is deterministic; the reasoner is only asked when the tree is ambiguous |
| Laya answers a choice question over a candidate set it is handed, at 200–460 ms on CPU | [`apps/models/laya/README.md`](../../apps/models/laya/README.md) | Laya is a picker over snapshot refs, not a tool-call generator |
| A planner comparison on this host: 18/27 vs 16/27 accuracy, Laya 454 ms vs Qwen 23.6 s | [`e2-laya-vs-qwen-e2e/RESULTS.md`](../../benchmark/experiments/e2-laya-vs-qwen-e2e/RESULTS.md) | planner choice is a cost dial, not an accuracy dial |

## Open

- The ownership default (D1 in note 01) and the reasoning cadence default (D2) are written as
  proposals. Both are the operator's call and neither is agreed yet.
- The E7 task set has no fixture page yet. E6 left `harness/page/targets.html`, which is a starting
  point rather than a reuse.

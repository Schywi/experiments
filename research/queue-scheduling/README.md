---
title: 'Queue scheduling'
kind: index
date: 2026-11-09
---

# Queue scheduling

Which ordering policy lowers the wait tail on the pooled browser fleet, measured without
disturbing the persistence or session-lab experiments.

| Note | Answers |
|---|---|
| [HANDOFF](HANDOFF.md) | The work order: measure the wait split first, then a simulator, then one live A/B in its own namespace |
| [01 — queueing theory](01-queueing-theory.md) | What the wait is made of, which policies the non-preemptive model leaves, where admission control and prewarming belong, and what a Redis-backed queue would and would not buy |
| [02 — boundaries and implementation](02-boundaries-and-plan.md) | What the experiment owns, the one seam it needed in the runner, the code that exists, and the first comparison the simulator produced |

State: level 1 is built and measured (simulator, tests, benchmarks). Level 2 — the live A/B in
namespace `queue-lab` — is not run.

The isolated lab:

```bash
scripts/queue-experiment.sh --simulate --mix 30s:1500,2m:900,5m:600 --rate 35
scripts/queue-experiment.sh --replay docs/session-lab-runs/<run>.db
scripts/queue-experiment.sh --compare --queue=prio-aging --mix 30s:60,2m:30,5m:10 --rate 2
```

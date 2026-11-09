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
| [03 — holds of 15 s to 2 min](03-hold-length-sensitivity.md) | Whether the ordering result survives shorter holds, and where the boot rate takes over as the tail |
| [04 — the Redis decision](04-redis-decision.md) | What "use Redis for the queue" can mean, what the measurement does and does not prove, and the conditions that reopen it |
| [05 — the external review](05-outside-review.md) | Which parts of an unsolicited architecture review hold against the code, and which do not |

State: level 1 is built and measured (simulator, 32 tests, benchmarks). Level 2 — the live A/B in
namespace `queue-lab` — is not run. Notes 03 and 04 were decided on analysis and measurement rather
than on a live arm.

The isolated lab:

```bash
scripts/queue-experiment.sh --simulate --mix 30s:1500,2m:900,5m:600 --rate 35
scripts/queue-experiment.sh --replay docs/session-lab-runs/<run>.db
scripts/queue-experiment.sh --compare --queue=prio-aging --mix 30s:60,2m:30,5m:10 --rate 2
```

The result this study currently rests on: on the recorded shape priority moves the fast class's p50
from 26.9 to 7.0 minutes and the slow class's from 17.4 to 24.3 — a transfer, priced. The fleet
baseline behind it is the capacity ledger's 352 slots, and the ramp that produced the 22-minute p50
is [note 25](../session-lifecycle-elasticity/25-capacity-ramp-and-provisioning.md)'s subject.

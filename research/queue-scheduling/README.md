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

State: theory written, nothing built. The first deliverable is still `HANDOFF.md` §5 step 1 —
one existing run's rows, split into admission, boot permit and slot wait.

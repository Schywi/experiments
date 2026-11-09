---
title: 'The external review, checked against the code: four points that hold and three that do not'
kind: audit
state: validated
date: 2026-11-09
branch: persist-probe
tags: [queue, autoscaler, review, audit, state-validated]
---

# Note 05 — The external review, checked against the code

| | |
|---|---|
| Status | Audit of an unsolicited architecture review against this repository's code and measurements |
| Date | 2026-11-09 |
| Scope | Which of the review's claims hold, which fail, and which were right about this project's own framing |
| Related | [02 — boundaries](02-boundaries-and-plan.md) · [04 — the Redis decision](04-redis-decision.md) · [27 — autoscaler signals](../session-lifecycle-elasticity/27-autoscaler-signals.md) |

The review arrived as prose, with a decision table, a recommended architecture and a "don't turn a
scheduling experiment into a Redis infrastructure project". It is recorded here because the next
reader will otherwise re-litigate it, and because two of its conclusions are load-bearing.

## 1. Points that hold

| Claim | Verdict |
|---|---|
| Do not introduce Redis on the acquisition path | **holds** — and for the reason in note 04: a shared queue buys no latency here, so the case would have to be sharing, not speed |
| Keep `Options.Queue` as the seam; it is the right place to test policies | **holds** — it is what shipped, and it is four lines in `shared.go` and `run.go` |
| The runner owns capacity; do not build a gateway until coordination is needed | **holds**, and it matches the owner's answer to note 01 §9.4 |
| The autoscaler reacts to Kubernetes state rather than application demand | **holds** — see §2, with a correction: the mechanism is worse than "Kubernetes state" |

## 2. Points that fail, with the evidence

**a. "This is potentially more important than Redis; it may fix the biggest measured defect."**

The defect is real: `autoscale.go:132-137` computes `demand = (ready + pending claims) / contextsPerBrowser`,
and `slots.Pool` serialises cold paths to one at a time, so `Pending ≤ 1` **by construction**. The
autoscaler's "demand" is therefore a statement about the fleet it already has.

But its payoff is bounded, and the review's framing has the mechanism backwards. The autoscaler is
also pressure-aware (`autoscale.go:151-176`): it shrinks one replica when CPU PSI full exceeds 25 or
CPU utilisation exceeds 0.85, holds inside the band, and grows toward demand only when the node is
clearly free. Its pressure inputs are **CPU only** — `NodeLoad{Utilization, PressureFull}` — while
the constraint that stopped the older run was memory. So on this host the blind demand signal was
the thing preventing a runaway, not the thing causing the shortfall, and fixing the blindness
without a memory input would trade a queue for an OOM kill. The measured runs since have reached 58+
pods and survived, so the wall is further out than the older note suggests and remains unmeasured:
the ledger's rows stop at 44 instances and the 58+ report has no row yet
([note 24](../session-lifecycle-elasticity/24-capacity-ledger.md)).

**b. "Expose Prometheus gauges; the autoscaler reads them."**

Three independent reasons this does not connect in this cluster:

| Step | Reality |
|---|---|
| The gauge | **already exists** — `waiting`, set from `SessionArrived`/`SessionAcquired` (`internal/platform/metrics/metrics.go:180-195`), wired in `cmd/session-lab/main.go:140` |
| The scrape path | the runner runs **on the host**, not in the cluster, so the in-cluster VictoriaMetrics/Vector stack never sees that endpoint |
| The reader | the autoscaler has no Prometheus client; it reads claims through the API server and node load through a `NodeLoadFunc` |

The cheapest real channel is the one already in the API server — `SandboxWarmPool.status.replicas` and
`readyReplicas`, on an object the autoscaler's Role already permits it to watch. Note 27 works
through the options.

**c. The token bucket in the recommended architecture.**

Inserting a token bucket between the admission gate and the queues adds nothing that `--admit` does
not already do, and it changes offered load — which is exactly what makes an A/B incomparable. Note
01 §4 requires rejection rate and completed throughput beside any admission experiment for that
reason.

**d. "Three queues: fast, normal, bulk" as a design.**

That names a partition, not a discipline. The simulator prices the disciplines: strict priority moves
the fast class's p50 from 26.9 to 7.0 minutes and the slow class's from 17.4 to 24.3; aging lands
between (11.9 / 22.8) with a better fairness index; DRR at 4/2/1 splits the difference. Choosing
"three queues" without choosing among those leaves the interesting variable unset. And in this
repository a class is a **run-level** flag (`--class`); `model.Session` has no class field, so
per-session classes exist only inside queue-lab's classifier.

## 3. Where the review was right about us

Its correction of this study's own framing stands and is recorded in note 04 §2: aggregate decision
time is not request latency, and "9,004 × 100 µs" says nothing about contention. `Simulate`'s
`DecisionNS` counts work, not waiting, and the benchmark is single-threaded. The honest claim is
narrower than the one note 02 §6 originally made.

## 4. What the review did not answer

| Open question | Status after the review |
|---|---|
| note 01 §9.1 — is the complaint the wait or its variance? | unanswered |
| note 01 §9.2 — are `normal`/`batch`/`bulk` real traffic? | assumed, not addressed |
| note 01 §9.4 — may `bulk` be interrupted now that restore costs ~0.4 s? | unanswered, and it is the only lever that changes the denominator rather than the order |
| note 04 §6 — may a lease be handed out twice after a consumer restart? | not reached; moot while the queue stays in the runner |

## 5. Sources

| Claim | Source |
|---|---|
| demand formula and hysteresis | `apps/session-lab/internal/infra/pool/autoscale.go:126-180` [I] |
| `waiting` gauge | `internal/platform/metrics/metrics.go:180-195` [I] |
| runner is a host process | `scripts/session-lab-experiment.sh`, `cmd/session-lab/main.go` [I] |
| pool status fields and RBAC | agent-sandbox `extensions/api/v1beta1/sandboxwarmpool_types.go:96,100`; `config/session-lab/templates/role.yaml:15-19` [I] |
| policy comparison numbers | note 02 §5 [M] |
| 44 instances / 352 slots | [note 24](../session-lifecycle-elasticity/24-capacity-ledger.md), run `20261107T235847Z` [M] |
| 58+ pods | reported by the owner, 2026-11-09; unrecorded (note 24, unrecorded reports) |

---
title: 'The Redis decision: a shared queue buys no latency here, and what would reopen it'
kind: analysis
state: frozen
date: 2026-11-09
branch: persist-probe
tags: [queue, redis, architecture, decision, state-frozen]
---

# Note 04 — The Redis decision

| | |
|---|---|
| Status | Decision, in force. No Redis in this repository; the conditions that would reopen it are stated in §6 |
| Date | 2026-11-09 |
| Scope | Whether queue state should live in Redis, what each of the three things "use Redis for the queue" can mean, and the measurements behind the answer |
| Related | [01 — queueing theory](01-queueing-theory.md) §6 · [02 — boundaries](02-boundaries-and-plan.md) §6 · [06 — the persistence writer as a bounded Go queue](../browser-session-persistence/06-rfc-bounded-queue.md) |

## 1. Three problems wearing one name

"Use Redis to decouple the queue logic from the Go process" bundles three concerns with different
answers. Keeping them apart is most of the decision.

| Problem | Mechanism today | What Redis would hold |
|---|---|---|
| **Ordering** — who gets the next slot | `admission.Gate` in the runner process, or `session.Options.Queue` | a sorted set scored by effective rank, claimed by one Lua script, shared by several processes |
| **Visibility** — what is waiting | a `waiting` gauge on the process (`internal/platform/metrics`), and `Pending` claims in the API server | a depth counter any process can read |
| **Durability** — surviving a restart | nothing; a killed run is restarted | queue state that outlives the process |

Only the first puts Redis on the acquisition path. The second is a read. The third is a requirement
nobody has stated: a run that dies mid-flight is a run to redo, and its queue is worthless anyway.

## 2. The measurement, and what it does and does not prove

`BenchmarkDecide` prices one `Offer` plus one `Peek` — the work `betterWaiting` and `enqueue` do
per admission:

| queue depth | fifo | prio | prio-aging | drr | oracle-sjf |
|---|---|---|---|---|---|
| 1 | 81 ns | 75 ns | 76 ns | 506 ns | 49 ns |
| 1,000 | 2.3 µs | 3.5 µs | 3.4 µs | 5.4 µs | 3.3 µs |
| 10,000 | 25.5 µs | 38.0 µs | 35.5 µs | 71.6 µs | 34.4 µs |

And one recorded run made **9,004 policy decisions over a 31.5-minute makespan** (note 02 §5). At
100 µs per decision — more than a loopback Redis round trip — that is **0.9 seconds of scheduler
time across half an hour**.

The tempting conclusion, and the one this note refuses to make, is "Redis is too slow". Aggregate
decision time and request latency are different measurements, and the aggregate says nothing about
what 100 sessions contending on one lock would experience. What the table does establish is
narrower and enough: **a shared queue buys no latency here.** A hop cannot beat a mutex on the same
CPU, so any case for Redis has to be a case for sharing, on its merits.

`Simulate`'s `DecisionNS` counts decision work, not waiting on a lock, and the benchmark is
single-threaded. Neither number can price contention; that gap is real and is named here rather
than papered over.

## 3. What a shared queue cannot buy, at any speed

| Cost | Detail |
|---|---|
| A new failure mode | an acquire fails because a datastore is unreachable. `close(wait)` cannot fail |
| A wake-up protocol | `BLPOP` delivers one at a time with no ordering by score; pub/sub drops messages; Streams with a consumer group gives at-least-once and requeue on crash |
| Idempotency | at-least-once delivery means a lease can be handed out twice. `slots.Pool` has no notion of a duplicate handout |
| Two owners of one fact | who holds the in-flight count — Redis or the runner? A cache beside a source of truth is how a queue starts over-admitting |

The wake-up is the part that actually fails in designs like this. The current implementation
replaces a broadcast (`close(p.wait)`) with a single winner chosen by the policy; a Redis version
replaces one line of Go with a distributed protocol, and the protocol is where the bugs live.

## 4. The visibility problem has cheaper answers

The measured defect — the autoscaler reading `Pending = 0/1` while thousands of sessions waited
(note 22 §4.2) — is real, and it does not need a datastore:

1. **The pool already publishes the pipeline.** `SandboxWarmPool.status` carries `replicas` and
   `readyReplicas` (`extensions/api/v1beta1/sandboxwarmpool_types.go:96,100`). `replicas −
   readyReplicas` is the boots-ordered-not-yet-serving deficit, and the autoscaler's Role already
   grants `get, list, watch` on `sandboxwarmpools`.
2. **The runner already exports depth.** `metrics.Recorder` keeps a `waiting` gauge set from
   `SessionArrived`/`SessionAcquired` (`internal/platform/metrics/metrics.go:180-195`), wired into
   `cmd/session-lab` (`main.go:140`). Per-class counts already exist in `admission.Queue.Snapshot()`.
3. If a channel is needed at all, it is a small committed object in the pool's namespace, read by a
   process that already talks to the API server — not a new server. Note 27 works through the
   options and their costs.

## 5. The precedent this repository already set

[RFC 06](../browser-session-persistence/06-rfc-bounded-queue.md) argues that MinIO stays object
storage and the queue semantics live in the Go writer: a byte budget, oldest-first eviction, three
counters (`ingest_bytes`, `bytes`, `evicted_bytes`) that describe the queue exactly. Storage is
where bytes go; ordering is where the decision is made. Redis would be the same decision taken in
the opposite direction for ordering, and it deserves the same evidence. It has not produced it.

## 6. Decision, and the conditions that reopen it

**No Redis. The queue lives in the process that holds the permits.** Any one of these reopens the
question, and none of them is true today:

| Condition | Status today |
|---|---|
| A second process counts admission permits (a gateway in front of the pool) | **no** — one runner, one gate. The owner's answer to note 01 §9.4 is "option A: the runner owns capacity" |
| The queue must survive a process restart | **not a requirement** — a cancelled run is rerun |
| A consumer needs queue depth and no cheaper channel exists | **not yet tested** — §4 lists three cheaper channels, none of them tried |

If the first becomes true, one more question must be answered *before* any design: **may a lease be
handed out twice** after a consumer restart? If no, the claim path cannot be a Lua pop and the whole
shape changes. That question is unanswered, and it is the one that decides whether a distributed
queue is even constructible here.

## 7. Sources

| Claim | Source |
|---|---|
| decision-cost table | `go test ./internal/queue/ -bench BenchmarkDecide -benchtime 500x`, 2026-11-09 [M] |
| 9,004 decisions, 31.5 min makespan | note 02 §5, `--simulate --mix 30s:1500,2m:900,5m:600 --rate 35` [M] |
| `waiting` gauge | `internal/platform/metrics/metrics.go:180-195`; `cmd/session-lab/main.go:140` [I] |
| pool status fields | `extensions/api/v1beta1/sandboxwarmpool_types.go:96,100` in the agent-sandbox tree [I] |
| `Pending = 0/1` while thousands waited | note 22 §4.2 [M] |
| single-runner ownership | owner's decision, 2026-11-09 |

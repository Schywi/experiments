---
title: 'Handoff: bench queue algorithms without touching the persist or session-lab experiments'
kind: handoff
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, queue, scheduling, handoff]
---

# Queue experiment — handoff

| | |
|---|---|
| Status | Plan, nothing built |
| Date | 2026-11-09 |
| Problem | Queue wait is the dominant latency and the current ordering is bad |
| Constraint | Must not disturb the persistence work or `scripts/session-lab-experiment.sh` |
| Related | [03 — the first MinIO run](../browser-session-persistence/03-first-minio-run.md) · [05 — persistence coverage](../browser-session-persistence/05-persistence-coverage.md) |

## 1. What the queue is today

Two queues sit in front of capacity, and a third wait lives in the platform.

**`admission.Gate`** — `apps/session-lab/internal/runtime/admission/admission.go:86-187`.
Bounds how many sessions are *acquiring* at once (`--admit`), orders waiters by class rank
then arrival, and refuses to let a better class jump a worse one (`betterWaiting`, an O(n)
scan per `Acquire`). Two properties matter for a benchmark: there is **no aging**, so a
`bulk` session behind steady `normal` traffic waits indefinitely, and the scan makes the
decision cost grow with queue depth.

**`slots.Pool.Take`** — `internal/runtime/slots/slots.go:134-175`. Looks for a free browser
slot; if none, probes a candidate instance's health; if another session is already
provisioning, it waits on a broadcast channel and **loops**:

```go
wait := p.wait
p.mu.Unlock()
select {
case <-ctx.Done():  return Lease{}, ctx.Err()
case <-wait:        continue          // every waiter wakes, all of them race
}
```

That is a notify-all wake-up with no ordering: when a slot frees, the goroutine the scheduler
happens to run first wins, regardless of who has been waiting longest. It is the most likely
source of the tail, and it is invisible in the current report.

**Claim → readiness** — `internal/infra/kube` acquires a `SandboxClaim` and watches it until
the sandbox reports Ready. A cold start is ~40 s on this node, which is a floor no ordering
can go below.

## 2. Measure before choosing an algorithm

The run record already splits the wait, so this needs no new code:

| Segment | Available as |
|---|---|
| arrival → admitted | `model.Session.AdmissionWaitNS` |
| claim created → slot usable | `ReadyNS − ClaimCreatedNS` |
| instance boot | `model.Instance.ReadyNS − CreatedNS` |

The first step is one run whose report shows those three for the same workload. If boot
dominates, no queue algorithm moves p95 much and the lever is `--boot-concurrency` and warm
pool size instead. Recording that split first is worth an hour.

## 3. Isolation: three levels, only the last touches the runner

**Level 0 — the split above.** No code. Read an existing run's rows.

**Level 1 — a simulator, no cluster at all.** New package `internal/sched`:

```go
type Request struct { ID int64; Class model.Class; ArrivalNS, HoldNS int64 }
type Queue interface {
    Offer(*Request)
    Next(nowNS int64) *Request   // nil when empty
    Len() int
}
type Result struct { Waits []int64; PerClass map[model.Class][]int64; Starved int }
func Simulate(workload []Request, slots int, q Queue, bootNS int64) Result
```

Algorithms to implement against one interface: FIFO; class-priority (today's rule); priority
with aging; least-attained-service (LAS) — no oracle needed, so it is deployable; shortest
remaining hold as an oracle upper bound; deficit round robin across classes. Each is a small
file with its own table-driven test. Benchmarks replay the mixes already used
(`30s:1500,2m:900,5m:600` at 35/s, and note 01's 300k/day shape).

This level decides the algorithm. It runs in `go test`, needs no cluster, and cannot interfere
with anything.

**Level 2 — one live A/B, opt-in.** Only after level 1 picks a winner:

- `session.Options.Queue` — one nil-gated field, the same shape as `Options.Persist` and
  `Options.Seeder`, so a nil value leaves every existing path byte-identical.
- `cmd/queue-lab` — its own binary, `config.Parse` for the workload flags (so `--mix`,
  `--fixed-arrival`, `--boot-concurrency` come for free), and `--queue=fifo|prio|prio-aging|las`.
- `scripts/queue-experiment.sh` — new driver; `scripts/session-lab-experiment.sh` and
  `scripts/persist-experiment.sh` stay byte-identical.
- `config/queue-lab/` — its own warm pool (`queue-lab-pool`) in its own namespace
  (`queue-lab`), so a queue run never takes a replica that a persist run is waiting on. Its
  own `--out` directory too.

## 4. What the benchmark has to report

| Metric | Why |
|---|---|
| wait p50 / p95 / p99 / max, per class | the tail is the complaint; the median is not |
| starvation count (wait > 5× the mean) per class | priority without aging looks fine in the median and fails here |
| throughput (sessions/s) and slot utilisation | an algorithm that lowers wait by rejecting work is not an improvement |
| fairness index across classes | a single class monopolising slots is invisible in the aggregate |
| decision cost (µs per Offer/Next) | the current `betterWaiting` scan is O(n) and depth reaches thousands |
| the boot/wait split of §2 | to keep the conclusion honest about what a queue can move |

Acceptance for merging a winner: p95 wait down at fixed offered load, no throughput loss, no
class starved beyond a stated bound, and the same numbers reproduced by the simulator.

## 5. Work order

| Step | Work | Size |
|---|---|---|
| 1 | Record the three-way split from an existing run | no code, 1 run |
| 2 | `internal/sched`: interface, FIFO + today's priority, simulator, tests | ~250 lines |
| 3 | LAS, aging, oracle SRPT, DRR + their tests | ~200 lines |
| 4 | Replay the recorded mixes, write the comparison note | ~1 note |
| 5 | `Options.Queue` + `cmd/queue-lab` + `scripts/queue-experiment.sh` + `config/queue-lab/` for the winning algorithm | ~250 lines |
| 6 | Live A/B: today's queue versus the winner, fixed offered load | 2 runs |

## 6. Isolation rules, stated as prohibitions

- No edit to `internal/runtime/admission` or `internal/runtime/slots` while levels 0–1 are
  running. They are the control.
- No edit to `config/session-lab`, `scripts/session-lab-experiment.sh`,
  `scripts/persist-experiment.sh`, or `cmd/persist-lab`.
- Every applied object exists as a committed file first; the queue lab gets its own namespace,
  pool and output directory rather than borrowing `sandbox-demo`.
- The only shared edit is `session.Options.Queue`, nil-gated, with the control arm being the
  field left nil.

## 7. Open questions for the owner

1. Is the complaint the **wait**, or the **variance**? A FIFO with a bigger warm pool moves
   the first and not the second.
2. Do classes exist in the product, or is `normal`/`batch`/`bulk` a benchmark fiction? Aging
   and DRR only mean something if real traffic has classes.
3. Is a session's hold known or estimable when it arrives? LAS needs neither; SRPT needs an
   estimate and is only useful as a bound.
4. Where is the queue allowed to live — runner process, or a controller in front of the pool?
   The second is a bigger change and would still need this benchmark to justify it.

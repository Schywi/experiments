---
title: 'The queue experiment's boundaries, what was built, and the first simulated comparison'
kind: design
state: implemented
date: 2026-11-09
branch: persist-probe
tags: [queue, scheduling, isolation, design, state-implemented]
---

# Note 02 — Boundaries, implementation, first result

| | |
|---|---|
| Status | Level 1 built and run (simulator). Level 2 (live A/B) not run |
| Date | 2026-11-09 |
| Scope | The queue experiment as an isolated lab: what it owns, the one seam it needs in the runner, the code that exists, and the comparison the simulator already produces |
| Related | [HANDOFF](HANDOFF.md) · [01 — queueing theory](01-queueing-theory.md) · [22 — queue audit](../session-lifecycle-elasticity/22-queue-multiplexing-audit-and-latency-breakdown.md) |

## 1. The boundary

The precedent is `cmd/persist-lab`: its logic lives in `internal/persist`, its binary in
`cmd/persist-lab`, and its whole effect on `session-lab` is nil-gated fields in
`session.Options`. Queue-lab keeps that shape and owns one more thing, because ordering
happens inside the runner rather than beside it.

| | |
|---|---|
| Owns | `apps/session-lab/internal/queue/`, `apps/session-lab/cmd/queue-lab/`, `config/queue-lab/`, `scripts/queue-experiment.sh`, `research/queue-scheduling/` |
| May import, read-only | `internal/platform/{model,config,store}`, `internal/runtime/{schedule,report}`, `internal/infra/{kube,sandbox}` |
| Must not edit | `internal/runtime/admission/`, `internal/runtime/slots/`, `internal/platform/model/`, `internal/platform/config/`, `internal/runtime/report/`, `config/session-lab/`, `scripts/session-lab-experiment.sh`, `scripts/persist-*.sh`, `cmd/{session-lab,persist-lab,pool-autoscaler}` |
| Must not touch at runtime | namespace `sandbox-demo`, its pools and claims, `docs/session-lab-runs/` |

Enforcement is mechanical. `scripts/queue-experiment.sh --check-boundaries [base]` reads
`git diff --name-only <base>..HEAD`, prints the files that reached the runner as `hook`, the
one defect fix as `defect`, and fails on anything outside the allow list in that script. It
cannot judge intent — an allowed file can still contain an unrelated change — so the allow list
is short enough to read.

**The single exception, and why it is not the boundary leaking.** Running the session-lab suite
under `-race` to verify this work surfaced a concurrent map read and write on `Runner.spools`:
`spoolFor` read and wrote the map from one goroutine per session with no lock. A Go map in that
state does not merely race, it faults the process, which is a run lost to a scheduling accident.
`pool.go` and `pool_test.go` therefore appear in the allow list as `defect`, they carry one
locking change and one regression test, and they are committed on their own as
`fix(session): lock the spool map` rather than inside a queue commit. Nothing queue-specific is in
them, which is the property the boundary exists to protect. The same `-race` run reports
**17 further races** in the runner's shared-session path (`lease.Instance.Sessions++` and its
readers, among others) at `HEAD` before any of this code existed; those are a separate defect of
their own and are not fixed here.

## 2. The one seam

The runner decides who proceeds next inside `gate.Acquire`, and there is no way to influence
that from outside the process. `internal/runtime/session/queue.go` adds the seam:

```go
type Queue interface {
    Acquire(ctx context.Context, req QueueRequest) error
    Release(req QueueRequest)
}

type QueueRequest struct {
    SessionID int64; Claim, Bucket string
    Hold      time.Duration
    Class     model.Class
}
```

`Options.Queue` is nil by default, and `admitOrder`/`releaseOrder` fall through to
`admission.Gate` — so a run that does not set the field takes the code path it always took.
The interface is declared in the runner and implemented elsewhere, so `session-lab` gains no
import of the queue package.

Two supporting edits, both additive: `Runner.order` holds the field, and `shared.go` plus the
non-shared path in `run.go` build a `QueueRequest` and call the two helpers. `Hold` comes from
the plan, which is what lets the lab classify on something; `SessionID` is the identity a
policy accumulates service against.

**The comparison runs with `--admit=1`.** With one admission permit the admitted session is the
only caller of `slots.Pool.Take` at that instant, so the pool's notify-all race has nothing to
race with and the observed order is the queue's order. `slots.go` is untouched and the A/B says
this in its own report. If one permit turns out to under-drive the fleet, the alternative is a
second seam at `Pool.Take`, which is a bigger edit to the file the handoff marks as control.

## 3. What exists

```
apps/session-lab/internal/queue/          the policy and the simulator; no k8s import
  queue.go      Request, Policy, New, Kinds, shared waiting list
  policies.go   fifo, prio, prio-aging, twolps, oracle (sjf and est)
  drr.go        smooth weighted round robin across classes
  simulate.go   discrete-event engine: slots, pods, boot, provisioning, decision latency
  live.go       the same policies as a gate the runner can call
  trace.go      plan → trace, run rows → trace, the three-way wait split
  stats.go      percentiles, per-class summary, starvation, fairness
  *_test.go     32 tests; bench_test.go prices a decision at depth 1/100/1k/10k

apps/session-lab/cmd/queue-lab/           the binary
  main.go       flags, mode selection
  simulate.go   trace generation, the comparison table, JSON out
  replay.go     a recorded run's split, then its replay
  live.go       one cluster arm, or control + arm with --compare

config/queue-lab/                         namespace queue-lab, SandboxTemplate, pool queue-lab-pool
scripts/queue-experiment.sh               --simulate --replay --live --compare --check-boundaries
```

`internal/runtime/session/{queue.go,shared.go,run.go}` carry the seam and nothing else.

## 4. Modes

| Mode | Needs | Does |
|---|---|---|
| `--simulate` | nothing | generates a trace from `--mix`/`--sessions`, runs every policy, writes a table and the trace |
| `--replay <db>` | a run database | derives arrival, admission, boot and slot waits from rows that already exist, then replays them |
| `--live --queue=X` | the cluster | one arm with the queue installed, in namespace `queue-lab` against `queue-lab-pool` |
| `--compare --queue=X` | the cluster | control arm first (no queue), then the policy, same seed |
| `--check-boundaries` | nothing | the path check from §1 |

## 5. First result (simulated)

```
scripts/queue-experiment.sh --simulate --mix 30s:1500,2m:900,5m:600 --rate 35
```

3,000 sessions over 1.4 minutes of arrivals, mean hold 1.9 min, against 8 leases per pod, at
most 38 pods, 40 s boot, one creation in flight — the shape note 22 recorded. Wait in minutes:

| policy | normal p50 | normal p95 | batch p50 | bulk p50 | bulk p95 | total wait | makespan | class-jain |
|---|---|---|---|---|---|---|---|---|
| fifo | 26.9 | 29.5 | 21.5 | 17.4 | 24.4 | 62,880 | 31.5 | 0.98 |
| prio | **7.0** | **9.6** | 15.0 | 24.3 | 26.9 | **37,650** | 34.7 | 0.83 |
| prio-aging | 11.9 | 20.8 | 15.0 | 22.8 | 27.3 | 44,871 | 33.3 | 0.94 |
| drr (4/2/1) | 16.4 | 23.0 | 18.5 | 21.9 | 27.5 | 50,770 | 33.7 | 0.99 |
| twolps | 26.9 | 29.5 | 21.5 | 17.4 | 24.4 | 62,880 | 31.5 | 0.98 |
| oracle-sjf | 7.0 | 9.6 | 15.0 | 24.3 | 26.9 | 37,650 | 34.7 | 0.83 |

Five things this already says, each of which is a claim the theory note made and can now be
checked against a run:

1. **Priority is a transfer, and the model prices it.** The fast class's p50 falls from 26.9 to
   7.0 minutes while the bulk class's rises from 17.4 to 24.3. Total wait falls 40% because the
   same work finishes in a shorter makespan when short jobs go first, but the bulk tail is worse
   and class-jain drops to 0.83. A queue that improved everything would mean the model is wrong.
2. **Aging is the half-measure it was described as**: normal p50 11.9, bulk p50 22.8, total wait
   between the two, and jain recovered to 0.94.
3. **`twolps` is FIFO to the nanosecond on this trace.** Demotion needs a client that comes back
   for a second lease, and a session here holds exactly one. The mechanism is written and tested
   with a returning client; on this workload it cannot do anything. Any claim that size-based
   demotion helps this fleet needs a traffic shape where the unit of scheduling is a request
   rather than a session.
4. **`oracle-sjf` equals `prio`.** Classes assigned by hold *are* shortest-first when the classes
   are the hold buckets, so a perfect classifier adds nothing over the class rank that already
   exists. That reframes the classification question: it is worth asking only when the class is
   wrong about the hold, which is what `--estimate-noise` can produce.
5. **A 50% wrong estimate costs almost nothing here** (`oracle-est` total wait 37,669 against
   37,650 for the truth). The buckets are 4× apart, so a 50% error rarely crosses a boundary.
   That is a property of this workload, not of classifiers, and it is the number a tighter mix
   would destroy.

## 6. What a decision costs

`BenchmarkDecide` prices one `Offer` plus one `Peek`, which is what `betterWaiting` and
`enqueue` cost per admission today:

| depth | fifo | prio | prio-aging | drr | oracle-sjf |
|---|---|---|---|---|---|
| 1 | 81 ns | 75 ns | 76 ns | 506 ns | 49 ns |
| 100 | 266 ns | 421 ns | 446 ns | 977 ns | 382 ns |
| 1,000 | 2.3 µs | 3.5 µs | 3.4 µs | 5.4 µs | 3.3 µs |
| 10,000 | 25.5 µs | 38.0 µs | 35.5 µs | 71.6 µs | 34.4 µs |

A loopback round trip to Redis is a hundred microseconds or more. At every depth this
experiment reaches, an in-process linear scan beats it — so a shared queue cannot be justified
by decision cost, only by what a second process needs to see. That is the measurement
`--decision-latency` exists to confirm on a live trace, and the argument
`queue-lab --simulate --decision-latency 100µs` settles before anything is deployed.

## 7. What the simulator cannot settle

- **The live arm's classification.** `--class-by` is `ClassByHold(30s, 2m)` here, and a run's
  real classes come from `--class` per run, not per session. The arm classifies inside the lab;
  the control arm inherits `admission.Gate`'s single run-level class. That asymmetry is real and
  has to be stated in any live write-up.
- **Anything about boot variance.** The engine charges one flat `--boot`; this host measured
  ~40 s cold with a spread, and the replay mode prints its own fidelity line so the error is
  visible rather than assumed away.
- **Whether the complaints behind this experiment are about the wait or its variance.** The
  table reports both, and the choice of headline is the owner's.

## 8. Steps and where they stand

| Step | State |
|---|---|
| 1 `internal/queue`: engine, FIFO, today's priority, tests | done |
| 2 The policies: aging, DRR, 2LPS, oracle, tests, benchmarks | done |
| 3 `cmd/queue-lab --simulate` and the comparison table | done |
| 4 `--replay` and the three-way split from recorded rows | done, no recorded run replayed yet |
| 5 The seam, `--live`, `config/queue-lab/`, the driver | done, nothing applied to a cluster |
| 6 The live A/B at fixed capacity | not run |

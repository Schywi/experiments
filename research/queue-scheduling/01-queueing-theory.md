---
title: 'Queue scheduling theory for a pooled browser fleet: what the wait is made of, and which policy can move it'
kind: analysis
state: hypothesis
date: 2026-11-09
branch: persist-probe
tags: [queue, scheduling, session-lifecycle, latency, state-hypothesis]
---

# Note 01 — Queue scheduling theory for a pooled browser fleet

| | |
|---|---|
| Status | Draft for discussion. No code, no run |
| Date | 2026-11-09 |
| Scope | The model behind the queue experiment: what the wait is actually made of here, which policies can move which part of it, where admission control and prewarming belong, and whether Redis earns a place in the path |
| Evidence base | `apps/session-lab/internal/runtime/{admission,slots,session,report}`, note 22's measured run, the persistence study's restore cost |
| Related | [HANDOFF](HANDOFF.md) · [22 — queue and multiplexing audit](../session-lifecycle-elasticity/22-queue-multiplexing-audit-and-latency-breakdown.md) · [16.1 browser contexts and slots](../session-lifecycle-elasticity/16.1-browser-contexts-and-slots.md) · [persistence 03](../browser-session-persistence/03-first-minio-run.md) · [persistence 05](../browser-session-persistence/05-persistence-coverage.md) |

Labels: **[M]** measured in a recorded run, **[D]** derived by arithmetic from [M] values,
**[I]** inferred from the code, **[H]** a prediction that no run has tested.

## 1. The system, stated as a queue

A session arrives, waits for permission to start, waits for a browser lease, holds that
lease for its whole working life, and gives the lease back. Three numbers describe it:

| Quantity | Value | Source |
|---|---|---|
| Arrival rate in the recorded stress run | 35 sessions/s, 3,000 sessions over ~86 s | [M], note 22 §1 |
| Hold distribution used for that shape | 30 s / 2 min / 5 min at 1500 / 900 / 600 | [M], the `--mix` arm |
| Mean hold | 0.5·30 + 0.3·120 + 0.2·300 = **111 s** | [D] |
| Slots per pod | 8 (`--contexts-per-browser`), which becomes `Instance.Capacity` at `slots.go:177` | [I] |
| Pods actually held | 38, i.e. **304 slots**, against an autoscaler cap of 80 | [M], note 22 §2.2 |
| Offered concurrency, Little's law | 35 × 111 = **3,885** | [D] |

Utilisation is therefore ρ ≈ 3885/304 ≈ **12.8** in that run, and ≈ 6.1 even at the 80-pod
cap. The host could not reach 80 pods: 38 pods already consumed 14.3 GiB of RAM plus 7.7 GiB
of zram against 16 + 16 GiB, and each further pod needs roughly 400 MiB [M, note 22 §2.2].

That one ratio settles what a queue algorithm can and cannot be sold as. At ρ > 1 total
service demand exceeds what the fleet can deliver in the arrival horizon, so the latency
cannot be removed by ordering. The makespan is fixed by the work: 3,000 × 111 s of hold
divided by 304 slots is ~1,095 s, and the recorded run's acquire p50 of 22 minutes and
p95 of 36 minutes sit inside note 22's 19.7–36 minute drain estimate [D].

What ordering changes is *which session absorbs that fixed delay*, and to a first
approximation the total. Three jobs of 300, 30 and 120 s arriving together:

| Order | Start times | Mean wait | Mean response |
|---|---|---|---|
| arrival order 300, 30, 120 | 0, 300, 330 | 210 s | 360 s |
| shortest first 30, 120, 300 | 0, 30, 150 | 60 s | 210 s |

[D] — the arithmetic is the whole argument. Shortest-first cuts mean response by 42%
without touching capacity, and hands the cut to the two short jobs while the 300 s job pays
the entire bill. A queue benchmark here is a measurement of that transfer, per class, and the
report has to make the transfer visible rather than average it away.

## 2. Where the wait is actually created

The runner has **three** serial gates between arrival and a working lease, and a fourth wait
inside the platform. `shared.go:138-200` is the path:

1. **`admission.Gate`** (`admission.go:86-199`), bounded by `--admit`. Wait recorded in
   `model.Session.AdmissionWaitNS`. Orders by `Class.Rank()` then arrival, refuses to let a
   better class wait behind a worse one (`betterWaiting`, an O(n) scan per `Acquire`), and
   has **no aging**: a `bulk` session behind steady `normal` traffic waits until the traffic
   stops. `Enqueue` is an O(n) insert into a sorted slice, so decision cost grows with depth.
2. **The boot permit** — a second `admission.Gate`, `admission.NewGate(cfg.BootConcurrency)`,
   constructed at `run.go:195` and acquired at `shared.go:170`, *before*
   `ClaimCreatedNS` is stamped. Its comment states the reason: a cold Chromium spikes, and
   overlapping spikes saturate the node while mean CPU still looks idle. **No session field
   records this wait.** It falls into the unlabelled gap between `AdmissionWaitNS` and
   `ClaimCreatedNS`.
3. **`slots.Pool.Take`** (`slots.go:134-185`), the wait recorded as *queue wait*
   (`ReadyNS − ClaimCreatedNS`). Two defects live here. `provisioning` serialises pod creation
   to **one at a time**, so a burst of sessions queues behind a single in-flight claim; and
   the wait is a notify-all channel:

   ```go
   wait := p.wait
   p.mu.Unlock()
   select {
   case <-ctx.Done(): return Lease{}, ctx.Err()
   case <-wait:       continue
   }
   ```

   Every waiter wakes on the same broadcast and re-races for the mutex; the goroutine the Go
   scheduler happens to run first wins. No ordering exists at this stage at all — not class,
   not arrival, not age.
4. **Claim → Ready**, the Kubernetes side. ~40 s cold on this node [M, `HANDOFF.md` §1]; a
   floor no ordering can go below.

The measured run makes the accounting gap concrete. Note 22 reports queue wait
(claim→ready) p50 **0 ms**, p95 **0 ms**, p99 6.2 s, max 302 s, while acquire (arrival→ready)
p50 was **22 minutes**. Both numbers are true and only one of them describes the wait the
operator experiences. With p50 0 ms in stage 3, the 22 minutes was spent *before*
`ClaimCreatedNS` was stamped, i.e. in stage 1 or stage 2 [D] — and the printed report cannot
say which, for two reasons:

- `report.Admission` is computed (`report.go:317`) and serialised, and the text summary never
  prints it. `report.go:484-486` prints queue wait and acquire, and stops there.
- Stage 2 has no field at all, so it cannot be printed even after fixing the above.

A queue experiment that starts by comparing algorithms therefore starts by measuring something
unidentified. That is the single most important conclusion in this note, and it is
`HANDOFF.md` step 1 for exactly this reason.

## 3. The model, in the form a policy can be written against

| Property | This system | Consequence |
|---|---|---|
| Service discipline | non-preemptive — a lease is held until the session ends | SJF-family policies, not SRPT itself. The mid-session interruption a preemptive scheduler needs would break a live checkout |
| Job size | unknown at arrival | needs a classifier, or a policy that does not use size |
| Classes | `normal` / `batch` / `bulk`, `model.Class.Rank()`, `MayScale()`, `Interruptible()` | static priority is already deployed; only `bulk` is declared interruptible, and `Interruptible()` is unused ("v1 does not preempt", `model.go:86-88`) |
| Servers | slots, not pods — 8 leases per Chromium | the schedulable resource is a lease; pods are how leases come into being |
| Capacity | elastic and controlled (`internal/infra/pool/autoscale.go`) | the controller is part of the scheduler, so a policy changes the capacity signal as well as the order |
| Load | ρ ≈ 12.8 in the recorded shape | an overloaded finite batch, not a stationary queue. Closed-form results assume fixed capacity and ρ < 1; the useful instrument is a trace-driven simulator, which is what `HANDOFF.md` §3 proposes |

### 3.1 Which policies survive that model

| Policy | Needs | What it does here | Verdict |
|---|---|---|---|
| FIFO / arrival order | nothing | fair in the only sense that matters for identical jobs; hides behind whoever arrived first | the control arm |
| Static class priority | a class label | today's rule at stage 1. Unbounded wait for the lowest class under sustained higher-class load | deployable, incomplete |
| Priority + aging | a wait-time weight | static priority with a bound: a waiter's effective rank improves as its wait grows, so `bulk` cannot be starved past a stated horizon | deployable, and the cheapest fix to a stated defect |
| 2LPS (demote on attained service) | a budget `b` and the clock | a session starts in the fast class and is demoted after it has actually held a lease for `b` seconds. No prediction, no model | deployable, no classifier |
| SJF/SRPT with a predicted size | an estimate of hold at arrival | the upper bound of what ordering is worth; the lab can compute it exactly because `--mix` knows the hold | oracle arm, a bound rather than a candidate |
| LAS (least attained service) | the clock | **degenerates here.** Every waiter has attained service zero, since holding no lease means holding nothing, so "least attained" cannot separate them and the policy reduces to arrival order | listed in `HANDOFF.md` §3 step 3 as deployable; under non-preemptive leasing it is FIFO with extra bookkeeping |
| DRR across classes | weights, per-class deficit | shares capacity by weight instead of by rank. The mechanism that makes "fast jobs get preference, bulk still progresses" a stated ratio rather than an emergent accident | deployable, pairs with aging |

The correction worth carrying into the experiment: the non-preemptive shape of this system
makes 2LPS the deployable size-based policy and makes LAS redundant. 2LPS has a property that
matters for a system whose job sizes are guesses — it demotes on *observed* service, so a
misclassified long job is punished by the clock within `b` seconds instead of sitting at the
front of the queue for five minutes. The expensive error in a size-based scheduler is
"predicted short, runs long" because it occupies a slot the whole time; 2LPS bounds that
error's blast radius, and a pure predicted-size priority does not.

### 3.2 Starvation and the bound

With rank as the only key, a class-k job's wait is unbounded whenever classes 0..k-1 saturate
the fleet. Aging turns the guarantee into arithmetic: with age weight `a` and rank `r`, a
waiter overtakes a lower-ranked newcomer once its accumulated credit `a·wait` exceeds the rank
gap, so the worst-case wait is stated in units of higher-class service time. Any policy the
experiment recommends should carry such a number, and `HANDOFF.md` §4's starvation count
(wait > 5× mean) is the measurement that checks it.

DRR adds the second half of fairness: strict priority optimises the fast class at any cost to
the slow one, while weighted deficit round robin makes "fast gets 8 slots for every 1 bulk
slot" a configuration rather than a consequence.

## 4. Four mechanisms, one responsibility each

The mechanisms in the discussion so far (token bucket, bounded queue, semaphore, scheduler,
aging, backoff, warm pool) are not alternatives to each other. Each one answers a different
question, and the failure mode of the design is a mechanism doing a job it cannot see.

| Mechanism | Question it answers | What it cannot see | Where it belongs |
|---|---|---|---|
| Token bucket | how fast may arrivals enter | whether a slot exists | the gateway, in front of the fleet |
| Bounded queue | how much work may wait | which waiter deserves the next slot | with the token bucket |
| Semaphore / slot pool | how many leases exist | who gets one | `slots.Pool`, unchanged |
| Scheduler | which waiter gets the next lease | how many leases exist | the new `sched` package |
| Aging | how long may a waiter be starved | anything about capacity | inside the scheduler |
| Exponential backoff | how a *client* retries after a refusal | nothing about the fleet | clients only |
| Warm pool | how much capacity exists before demand lands | ordering | the autoscaler |

Two consequences.

**Admission control is not latency control.** A token bucket that drops or defers arrivals
lowers measured wait by lowering load, which is why any admission experiment must report
rejection rate and completed sessions per second beside the percentiles — otherwise the
comparison is between two different workloads. `HANDOFF.md` §4 already asks for throughput
and rejection; this is the reason.

**Backoff inside the fleet manufactures the latency it is meant to avoid.** A slot that frees
50 ms after a request arrives should be handed to that request. The assistant's gateway
already draws this line correctly: `apps/assistant/concurrency.py` is an in-process bounded
FIFO with an immediate `429` + `Retry-After` on overflow, single replica, `asyncio.Semaphore`
waking waiters in arrival order, and it says in its docstring that no Redis or distributed
queue is involved. Any claim that the fleet needs a better queue has to survive the fact that
the front door was deliberately built without one.

### 4.1 Where the noise in this discussion comes from

The warm-pool pitch rests on a measured defect, and it is worth stating flatly because it is
the strongest evidence in the whole study: the autoscaler's demand signal is `SandboxClaim`
objects that are not Ready yet, and `slots.Pool` serialises provisioning, so the autoscaler
observed `Pending = 0` or `1` while more than 2,600 sessions waited inside the runner's
goroutines (note 22 §4.2 [M]). The queue existed and the controller could not see it. Warm
pools, predictive or not, only help if they are fed a queue-depth signal from the process that
holds the queue.

## 5. Preemption is newly affordable, and it is the only way past the floor

`model.Class.Interruptible()` returns true for `bulk` and is not called anywhere: the class
policy is stated once and left unimplemented. The reason it was left is that suspending a
browser context loses whatever the session was doing.

The persistence arm changes that arithmetic. Restore measured **461 ms mean** over 2 blobs
(note 03 §4) and **392 ms mean** with 8 of 8 items and 4 of 4 cookies restored (note 05), on a
fresh claim. Against waits measured in minutes, ~0.4 s to evict a `bulk` session and put a
`normal` session on its slot is a cheap trade — and it is the only mechanism in this design
that reduces wait by increasing effective capacity rather than by reordering the queue [H].
It is also the one place where the two experiments are not independent, which argues for
measuring it after the ordering work rather than bundling it in.

## 6. Redis: what decoupling would buy, and what it costs

Three different things could move out of the Go process, and they have different price tags.

| What moves | Benefit today | Price |
|---|---|---|
| Waiter depth published for the autoscaler | closes the measured `Pending = 0/1` blindness in §4.1 | a read path, no hot-path hop. Cheapest real win |
| Queue membership + ordering (a sorted set scored by effective rank, claimed with one Lua script) | one ordered truth for any number of producers; survives a runner restart; lets the gateway be a separate deployment | a hop and a serialisation per acquisition and per release; a new hard dependency; two owners for the same state |
| Admission counters shared with the gateway | fleet-wide `--admit` instead of per-process | same hop, plus the failure mode of the limiter being unavailable |

What it does not buy: anything about capacity. Redis makes the queue visible and shared; it
does not create a slot, shorten a 40 s cold boot, or change ρ from 12.8. Presented as a
latency fix it would be a distraction, and the strongest argument for it is the one in §4.1 —
a second process (the autoscaler) needs to see the queue.

Costs that belong on the record before any code:

- **A hop in the hot path, against a scan that is already O(n).** `betterWaiting` and
  `enqueue` are linear in waiter depth, which reached thousands in the recorded run [M]. A
  loopback Redis `ZADD`+`EVAL` is tens to a few hundred µs. Those numbers are comparable, so
  "Redis is slower" and "Redis is faster" are both unsupported until `HANDOFF.md` §4's
  decision-cost metric exists.
- **Wake-up is the hard part.** An ordered set gives ordering; it does not give "tell exactly
  the right waiter". `BLPOP` gives one-at-a-time delivery with no ordering by score; pub/sub
  is fire-and-forget; Streams with a consumer group give at-least-once and requeue on crash,
  which drags in idempotency on a lease that is not idempotent. The notify-all broadcast this
  design is trying to replace is a one-line `close(wait)`; a correct Redis replacement is a
  protocol.
- **Memory on a host whose measured ceiling was memory.** 38 pods already consumed 22 GiB
  including swap (note 22 §2.2). A Redis pod with a bounded sorted set is small, but it is
  the same 6-core / 16 GiB host, and it competes with the thing being benchmarked.
- **A manifest, not a `kubectl run`.** Anything deployed must exist as a committed file first
  (`AGENTS.md`), so Redis means `config/redis/` with its own namespace and resource limits —
  and a live experiment that a second host cannot reproduce without it.

The position this note argues for: write the policy behind a `Queue` interface with an
in-process implementation, because that is what the simulator and the runner both need and it
is what a Redis implementation would have to be measured against. Then run one experiment —
same trace, same slots, in-process versus Redis-backed — and let the decision cost and p99
wait decide. If Redis loses on the hot path, it still has a job publishing waiter depth to the
autoscaler, and that job is worth doing on its own.

## 7. What the lab can and cannot test

The runner's workload is synthetic: `--mix` states the holds, so a simulator knows every job's
size exactly. That has one sharp consequence.

- A policy that uses the true hold measures the **upper bound** of classification value. So
  does a "classifier" trained on the same field it schedules against. Both are oracle arms.
- The only policies the lab can test *as deployable* are those that never look at the size:
  static priority, priority + aging, DRR, and 2LPS when demotion is driven by the clock rather
  than by an estimate.
- Testing the classifier itself needs a workload where arrival-time features exist and the
  hold is only known afterwards. Nothing in this repository produces that yet, and it is the
  reason a study called "learning-augmented scheduling" would need a second workload source
  before its learning half means anything.

## 8. Predictions, each with its falsifier

| # | Prediction | Falsified if |
|---|---|---|
| P1 | The 22-minute wait sits in the boot permit, not in `admission.Gate` | the recorded admission series is the large one, or both are small and stage 3 carries it |
| P2 | Priority + aging cuts `bulk` p99 wait by an order of magnitude at fixed offered load, and leaves `normal` p95 within noise | `normal` p95 moves by more than the run-to-run spread, or `bulk` p99 does not move |
| P3 | 2LPS at `b` ≈ 30 s beats static priority on mean response with no classifier, and loses to oracle SRPT | 2LPS is indistinguishable from static priority at every `b`, or oracle SRPT is no better than 2LPS |
| P4 | Ordering moves p95 by tens of percent; cold boot (~40 s) and ρ ≈ 12.8 dominate it | a policy moves p95 by multiples, which would mean the 22 minutes was queue contention rather than the boot permit |
| P5 | A Redis-backed queue costs more per decision than the in-process one at depths below 10³, and pays only in cross-process visibility | Redis is cheaper at every depth, or the autoscaler is shown to work without a depth signal |

## 9. Open questions for the owner

1. Is the complaint the **wait** or the **variance**? FIFO with a bigger warm pool moves the
   first and leaves the second; a size-based policy moves the second.
2. Do classes exist in the product, or is `normal`/`batch`/`bulk` a benchmark fiction? Aging
   and DRR only mean something if real traffic is classified.
3. Which process is allowed to hold the queue: the runner, or a gateway in front of the pool?
   A gateway is the only shape in which a shared queue is needed at all, and it is a bigger
   change than everything else in the handoff.
4. Is `bulk` interruption acceptable to the product owner now that restore costs ~0.4 s? It is
   the only lever that changes the denominator instead of the order.

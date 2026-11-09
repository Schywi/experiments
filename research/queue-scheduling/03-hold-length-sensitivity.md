---
title: 'Holds of 15 s to 2 min: when the boot rate, rather than the queue discipline, sets the tail'
kind: analysis
state: hypothesis
date: 2026-11-09
branch: persist-probe
tags: [queue, scheduling, latency, hold-length, state-hypothesis]
---

# Note 03 — Holds of 15 s to 2 min

| | |
|---|---|
| Status | Analysis with predictions. No run: the three commands in §7 are the test |
| Date | 2026-11-09 |
| Scope | Whether the ordering result of [note 02](02-boundaries-and-plan.md) survives a workload whose holds are 15 s, 30 s, 1 m and 2 m instead of 30 s, 2 m and 5 m |
| Related | [01 — queueing theory](01-queueing-theory.md) · [02 — boundaries and implementation](02-boundaries-and-plan.md) · [25 — capacity ramp](../session-lifecycle-elasticity/25-capacity-ramp-and-provisioning.md) |

Labels: **[D]** derived by arithmetic, **[I]** inferred from code, **[H]** a prediction no run has
tested.

## 1. The question

Every result in note 02 came from one workload shape: 1,500 sessions of 30 s, 900 of 2 min and 600
of 5 min, at 35 arrivals/s. A queue policy's value depends on how much work is waiting to be
reordered, and that depends on the holds. Shorter holds mean less queued work, so the same policy
moves less. The question is whether the ordering work is worth continuing once the holds look like
the ones a short interactive request actually needs.

## 2. Two workloads, one arrival rate

Both at 35 arrivals/s, both against the newest measured fleet in the capacity ledger — **44
instances × 8 leases = 352 slots** ([note 24](../session-lifecycle-elasticity/24-capacity-ledger.md)).
A later run is reported to have reached 58+ instances; it has no ledger row yet, so it is not used
for arithmetic here.

| | recorded mix | short mix |
|---|---|---|
| buckets | 30 s ×1500, 2 m ×900, 5 m ×600 | 15 s ×1500, 30 s ×900, 1 m ×450, 2 m ×150 |
| mean hold | 111 s | **31.5 s** |
| offered concurrency, λ·E[W] | 3,885 | **1,103** |
| utilisation ρ at 352 slots | 11.0 | **3.13** |
| total work | 333,000 slot-s | **94,500 slot-s** |
| drain bound at 352 slots | 15.8 min | **4.5 min** |
| mean hold ÷ cold boot (40 s) | 2.8 | **0.79** |

[D] The last row is the whole note. In the recorded shape a session holds its browser for nearly
three cold boots; in the short shape a boot takes longer than the session it is booted for.

## 3. What that does to the three levers

| Lever | recorded mix | short mix | why |
|---|---|---|---|
| **Ordering** | normal p50 26.9 → 7.0 min under priority | [H] moves, at a third of the scale | the reorderable backlog scales with E[W]; ρ 11.0 → 3.13 cuts it by 3.5× |
| **Capacity ramp** | the fleet needs ~40 pods and gets one per 40 s | the same ramp, now against 4.5 min of work | [D] with `provisioning = 1` the fleet adds 0.2 slots/s, so building 352 slots takes 44 × 40 s = **29.3 min**, six times the drain |
| **Boot itself** | 40 s against a 111 s hold | 40 s against a 31.5 s hold | [D] a cold session pays 1.3× its own service time before doing any work |

The second and third rows say the same thing from two directions: **in the short-hold regime the
provisioning path, not the policy, is the SLO.** No queue discipline can return a slot that does
not exist yet, and no discipline makes a 40 s boot shorter than a 30 s session.

## 4. Strict priority becomes tolerable, and nearly pointless

Priority's cost to the slow class is the higher classes' accumulated backlog plus the worst single
hold it can be stuck behind. Both shrink with the holds:

- The worst single block falls from 5 min to 2 min.
- The backlog falls with λ_high × h_high, so by roughly the same factor as E[W].
- The *ratio* of treatment barely moves: the new buckets are 8× apart end to end, the old ones
  10×.

[H] So under 15 s–2 m holds, strict priority stops being an argument about tens of minutes and
becomes an argument about seconds: acceptable, and no longer worth a study of its own. What
survives is the fairness question, which is what `drr` and `prio-aging` answer — a guaranteed
share, rather than a faster average.

Two consequences worth carrying:

- **Aging loses its job.** Its purpose is to bound what strict priority can inflict. When the bound
  is one backdrop of short leases, the bound is already small. [H] `prio-aging` and `prio` should
  converge as the holds shrink, and the gap between them is the measure of how much aging was
  ever buying.
- **Closer class boundaries make classification errors cheaper per crossing, and more frequent.**
  Boundaries at 15/30/60/120 s are 2× apart, so a 50 % estimate error almost always crosses one;
  in the recorded mix the buckets are 4× apart and ±50 % noise cost 19 units of total wait out of
  37,650 (0.05 %, note 02 §5). [H] The short mix should show a larger error cost per unit of noise
  and a smaller one in absolute minutes. That is a testable difference, and it is the only part of
  this note that touches the classification question at all.

## 5. Why this does not make the queue work worthless

At fixed capacity, ordering is the only mechanism that can put a waiting session on a slot that
already exists. The recorded run's 22-minute p50 was dominated by a ramp, which the queue cannot
touch; a run whose fleet is warm at t=0 is the case where the policy is the only variable left.
Both statements are true at once, and they point at different experiments:

| The complaint is | The lever | Where |
|---|---|---|
| "the first request of a burst waits 40 s" | warm pool size, `--boot-concurrency`, provisioning concurrency | note 25 |
| "short requests sit behind long ones" | the queue discipline | this study |

## 6. Effect on the plan

If the predictions in §4 hold, the queue study closes with note 02's result as its answer, plus
one live A/B, and the capacity work in note 25 becomes the next experiment. If they do not — if a
policy still moves p95 by multiples under 15 s–2 m holds — then ordering survives the regime change
and the live A/B is worth running first.

## 7. The test

Three arms, 3,000 sessions each, every policy. The first two differ only in whether the fleet is
warm at t=0, which separates the boot cost from the ordering effect:

```bash
cd apps/session-lab

# A. short holds, fleet grows from zero: the boot rate should dominate everything
go run ./cmd/queue-lab --simulate --queue all --mix 15s:1500,30s:900,1m:450,2m:150 \
  --rate 35 --class-normal 15s --class-batch 1m --max-pods 44 --out docs/queue-lab-runs

# B. short holds, 352 slots warm at t=0: ordering alone
go run ./cmd/queue-lab --simulate --queue all --mix 15s:1500,30s:900,1m:450,2m:150 \
  --rate 35 --class-normal 15s --class-batch 1m --slots 352 --out docs/queue-lab-runs

# C. the recorded mix on the same warm fleet, as the reference point
go run ./cmd/queue-lab --simulate --queue all --mix 30s:1500,2m:900,5m:600 \
  --rate 35 --class-normal 30s --class-batch 2m --slots 352 --out docs/queue-lab-runs
```

Read A against B: the difference is what the boot costs at short holds. Read B against C: the
difference is what the shorter holds do to the ordering effect, at equal capacity.

## 8. Predictions, each with its falsifier

| # | Prediction | Falsified if |
|---|---|---|
| P1 | In A, all seven policies land within run-to-run noise of each other | any policy separates from fifo by more than the spread between C's policies |
| P2 | In B, `prio` moves the fast class's p50 by at least 3× and the slow class's the wrong way | `prio` does not separate from fifo on a warm fleet |
| P3 | `prio-aging` and `prio` converge as the holds shrink (A and B against C) | aging separates *more* under short holds |
| P4 | The classification-error arm costs more per unit of noise on the short mix than on the recorded one | the error cost is flat or falls with closer boundaries |

## 9. Sources

| Claim | Source |
|---|---|
| recorded mix, 35 arrivals/s, holds 30 s/2 m/5 m | [note 22](../session-lifecycle-elasticity/22-queue-multiplexing-audit-and-latency-breakdown.md) §1 [M] |
| 352 slots (44 instances × 8) | [note 24](../session-lifecycle-elasticity/24-capacity-ledger.md), run `20261107T235847Z` [M] |
| cold boot ≈ 40 s | `HANDOFF.md` §1 [M]; note 22 §2.2 |
| ±50 % estimate noise cost | note 02 §5, `--estimate-noise 0.5` [M] |
| drain and ρ arithmetic | [D], from the rows above, at the ledger's 352 slots rather than note 02's 304 |
| 58+ instances reported | owner, 2026-11-09; recorded as unverified in note 24's unrecorded-reports section |

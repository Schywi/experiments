---
title: 'What the race detector finds at HEAD: one fixed map race and a family of counter races'
kind: audit
state: validated
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, concurrency, testing, audit, state-validated]
---

# Note 28 — Race detector audit

| | |
|---|---|
| Status | Audit. One family fixed with a regression test; the rest recorded and open |
| Date | 2026-11-09 |
| Scope | `go test ./... -race` in `apps/session-lab`: what fails, what is fixed, and what the failures are |
| Related | [25 — capacity ramp](25-capacity-ramp-and-provisioning.md) · [26 — claim binding](26-claim-binding-and-warm-taxonomy.md) · AGENTS.md § Testing |

## 1. The check and its state

```bash
cd apps/session-lab && go test ./... -count=1 -race
```

**Fails at HEAD.** It fails in `internal/runtime/session`, on data races reported by the detector,
not on an assertion. The count of warnings is not stable across runs — the detector reports what an
interleaving happened to produce — so a single number is a reading, not a property:

| Run | Warnings |
|---|---|
| pristine `git archive HEAD` tree, `internal/runtime/session` | **18** |
| this branch after the spool fix, whole module | the spool family gone; the remainder still present |

## 2. Attribution

The failures predate the queue work. A `git archive HEAD` export — no queue code, no seam, nothing
from this branch — reproduces them:

```
cd <export>/apps/session-lab && go test ./internal/runtime/session/ -count=1 -race
→ 18 × WARNING: DATA RACE, exit 1
```

Sites in that run, by file:

| File | Lines seen in stacks |
|---|---|
| `boot.go` | 44, 67 (`sampleBoot`) |
| `isolate.go` | 33 |
| `pool.go` | 59, 62, 65, 74 (`spoolFor` — fixed, §3) |
| `run.go` | 222, 259, 567, 596, 597, 612, 1046 |
| `shared.go` | 33, 45, 122, 140, 186, 212, 292, 307 |

## 3. The one family that is fixed

`Runner.spools` was read and written by one goroutine per session with no lock, and walked by two
other readers. A Go map in that state does not merely race — `fatal error: concurrent map read and
map write` kills the process, which is a run lost with nothing in the report to explain it.

| Change | Where |
|---|---|
| `spoolsMu sync.Mutex` beside the map, separate from `r.mu` so no caller can deadlock against a holder of the other | `internal/runtime/session/run.go` |
| `spoolFor` holds it across the read-modify-write | `pool.go:55-76` |
| `retireEverywhere` and `poolShape` copy the map under the lock and call into the pools outside it | `pool.go:87-95`, `shared.go:470-485` |
| `TestSpoolForIsSafeUnderConcurrentSessions` — three pools, sixty goroutines, all three readers | `pool_test.go` |

The test was verified as a regression test rather than a tautology: with the lock removed it reports
four `DATA RACE` warnings; with the lock it passes.

## 4. What remains, and why it is not fixed here

The remaining races are **counters shared between sessions that share an instance** and the
bookkeeping around them:

| Counter | Writers | Readers |
|---|---|---|
| `Instance.Sessions` | `shared.go:270`, `shared.go:312`, `run.go:786,795,808`, `infra/sandbox/sandbox.go:297` | `persistInstance`, the report's instance summary (`run.go:1065`) |
| boot sampling fields | `sampleBoot` (`boot.go:44,67`) | the report and the boot-peak derivation |
| run-level tallies | `sessionSharedAttempt` (`shared.go:186,212,292,307`) | the report |

Two reasons this is a separate piece of work rather than part of the queue experiment:

1. **It is another experiment's code.** Fixing it means deciding who owns a per-instance counter —
   `sandbox.Instance` made atomic, or the `Runner` owning it under `r.mu` — which touches
   `internal/infra/sandbox` and five increment sites. The queue experiment's boundary does not
   include it (see `queue-scheduling/02` §1).
2. **It changes published numbers.** `Instance.Sessions` is what produces "mean 78.95 sessions per
   instance" in note 22 §2.1. A fix that batches or reorders those increments must be verified
   against that figure, not merged quietly.

## 5. How to fix it, when it is its own task

- One writer per counter. Prefer the `Runner` holding the value under `r.mu`, incrementing once per
  session at settle, over making five call sites atomic.
- Keep the reported definition identical: sessions *completed* on an instance, incremented at the
  same moment in the session's life, so the density figures stay comparable with note 22.
- One test per counter, driven by two goroutines that share an instance, run under `-race`.
- Then the suite can carry `-race` in CI, which is the point: today the check is red, so a real
  regression would be invisible in the noise.

## 6. What this means for a reader of a red `-race` run

Until §4 lands, a red `go test ./... -race` is the expected state. To tell a new defect from the
known ones: it is new if it is **not** a counter or bookkeeping field on `Instance`, the boot
sampler, or the run tallies; and `internal/queue` is green under `-race` on its own, so any failure
there is real.

## 7. Sources

| Claim | Source |
|---|---|
| 18 warnings at HEAD | `git archive HEAD` export, `go test ./internal/runtime/session/ -race`, 2026-11-09 [M] |
| site list | the same run's stack traces [M] |
| spool fix and test | `apps/session-lab/internal/runtime/session/{pool.go,run.go,shared.go,pool_test.go}` [I] |
| test fails without the lock | lock removed temporarily, four warnings, then restored [M] |
| 78.95 sessions/instance | note 22 §2.1 [M] |

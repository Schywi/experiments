---
title: 'Fixing slot ownership and browser health: the build order'
kind: design
state: research
date: 2026-11-07
branch: 'docs-site'
commits: [c97079e]
tags: [session-lifecycle, slots, health, saturation, tdd]
---

# Fixing slot ownership and browser health: the build order

[Note 22](22-slot-health-and-saturation.md) establishes what went wrong: one
browser that never answered on 9224 absorbed 1378 sessions, because the isolate
failure path returns its slot without retiring the instance, every healthy
browser was already at 8/8, and the pool prefers the busiest instance. This note
is the order the fix lands in, and the tests that have to exist before each step
counts as done.

| | |
|---|---|
| Status | Plan, nothing built |
| Date | 2026-11-07 |
| Phase | session lifecycle |
| Branch | `docs-site` |
| Related | [findings](22-slot-health-and-saturation.md), [manual runbook](21-manual-scaling-runbook.md) |

## Rules for every phase

```
1  test first. A failing test that shows the bug is written before the fix, and it
   stays in the tree afterwards. A phase with no test is not done.
2  no cluster needed. Every test drives the session package with the fake adapter
   (internal/infra/sandbox Fake) and a fake probe; the live cluster is only used
   for the final dry run in phase 6.
3  one commit per phase, with the bd issue id.
4  go vet ./... and go test ./... green before the commit.
5  behaviour that the phase does not intend to change must be pinned by a test
   that already exists or is added with the phase.
```

The fakes the tests need, in the order they are introduced:

```
slots fake provider    func(ctx) (*sandbox.Instance, error)      slots.go already takes one
session fake adapter   internal/infra/sandbox.Fake                 already exists
fake probe             func(ctx, instance) error                   phase 2 introduces it
clock                  func() time.Time                            the runner already takes one
```

## Phase 1 — retire an instance when it fails

Today the isolate failure path returns the slot (`shared.go:188-195`) and the
close failure path returns it too (`shared.go:264-289`), so a dead browser stays
in the pool. A browser that fails once must leave the pool.

Tests first:

```
slots:   TestRetireAndGiveRemovesTheEntry
           pool with 1 instance, Take, RetireAndGive -> Snapshot().Instances == 0,
           InUse == 0, and a second Take creates a new instance
slots:   TestRetireAndGiveIsIdempotent
           second RetireAndGive on the same instance does not underflow inUse
session: TestIsolateFailureRetiresTheInstance
           fake adapter whose second created instance fails OpenSession with
           "connect: connection refused"; two sessions; assert the first settles
           reset_failed, the instance is retired, the second session gets a
           different instance
session: TestCloseFailureRetiresTheInstance
           hold succeeds, CloseSession returns "connection refused"; assert the
           session settles and the instance is retired
```

Change:

```
slots.go          RetireAndGive(lease Lease) (idle bool) -- one lock, entry removed,
                  counters settled by the entry's own InUse
shared.go:193     spool.RetireAndGive(lease) on isolate failure
shared.go:264-289 on close failure: retire when browserLost(closeErr) OR the error
                  text carries a dial/refused/timed-out marker; Give otherwise
```

Gate: `go test ./internal/runtime/slots ./internal/runtime/session`.

## Phase 2 — a health state, checked before a slot is handed out

Retiring on failure stops the loop for an instance that has already served a
session. It does nothing for an instance that was born broken and reports Ready,
which is what run B hit. The pool needs a health dimension.

Tests first:

```
slots:   TestTakeSkipsUnhealthyInstances
           two instances, one marked unhealthy: every Take returns the healthy one
slots:   TestTakeRetiresOnProbeFailure
           probe returns an error for instance A: Take retires A and returns B
slots:   TestProbeResultIsCached
           probe called once for two Takes inside the interval, twice outside it
session: TestProbeUsesTheTransportPort
           ws transport probes 9224, http transport probes 9222
session: TestTwoProbeFailuresRetire
           first failure marks degraded (no new assignments), second retires
```

Change:

```
slots.go       Instance{Health, CheckedNS, Failures}; takeFreeLocked skips
               unhealthy; Pool takes a Checker func(ctx, *Instance) bool
session/health.go   Probe(ctx, instance) error -- GET /json/version on the
               transport port, 300 ms timeout; Degraded once, Retired twice
run.go         --health-interval (default 5s) wired into the pool
```

Gate: `go test ./internal/runtime/slots ./internal/runtime/session`.

## Phase 3 — readiness covers the transport the run uses

The SandboxTemplate probes 9222 (the bridge). A run with `--cdp-transport ws`
dials 9224 (the forwarder) and nothing checks it.

Tests first:

```
chart:   helm template ... | grep -A4 readinessProbe  -> a probe on cdp-ws exists
         when forwarder.enabled, and is absent when it is disabled
```

Change:

```
config/sandbox/values.yaml            readiness.probeForwarder: true
config/sandbox/templates/sandboxtemplate.yaml:59-64
                                      second readinessProbe entry is not possible
                                      (one probe per container), so the forwarder
                                      container gets its own probe on cdp-ws
```

Gate: `bash config/ci/validate.sh`, plus the rendered YAML check above.

## Phase 4 — one bounded retry, on a different instance

A session that fails at the transport level is worth one more attempt, and it
must not go back to the instance that just failed.

Tests first:

```
session: TestSessionRetriesOnceOnATransportFailure
           adapter: instance A refuses, instance B works; 1 session -> success,
           A retired, attempts == 2
session: TestSessionDoesNotRetryANonTransportFailure
           a 500 from CDP does not retry (outcome reset_failed, attempts == 1)
session: TestSessionRetryIsBounded
           both instances refuse -> reset_failed, attempts == 2, no third try
```

Change:

```
config.go      --retries, default 1; 0 disables
run.go         the settle path for reset_failed/lost_unknown caused by a transport
               error re-enters acquisition once, with the failed instance retired
model          Session.Attempts, written to the row and the report
store.go       sessions.attempts column + migration
```

Gate: `go test ./internal/runtime/session ./internal/platform/store`.

## Phase 5 — saturation guard

At 98 % occupancy one sick instance decides the run. The pool must know how much
*healthy* capacity it has, and say so when it runs out.

Tests first:

```
slots:   TestSnapshotCountsHealthyInstances
           3 instances, 1 unhealthy -> Healthy == 2
slots:   TestTakeCreatesWhenOnlyUnhealthySlotsRemain
           1 healthy at 8/8, 1 unhealthy at 0/8 -> Take calls the provider
session: TestStarvationBoundaryIsRecorded
           provider returns an error -> a slot_starvation boundary is recorded
```

Change:

```
slots.go       Snapshot.Healthy; Take prefers creating over using a degraded entry
session        boundary kind "slot_starvation" through the existing Boundary recorder
notes          --boot-concurrency default written into the runbook (run 21)
```

Gate: `go test ./internal/runtime/slots ./internal/runtime/session`.

## Phase 6 — instrumentation

The next comparison should not need a forensic pass over two databases.

Tests first:

```
report:  TestBuildIncludesPerInstanceTable
           a store with three instances (one retired, one degraded) renders the
           per-instance block with attempted/succeeded/failed and health
measure: TestBootPeakIsClampedToCoreCount
           a cgroup delta larger than cores*interval cannot report more cores than
           the node has
```

Change:

```
report/groups.go, report.go   per-instance block: sessions attempted, succeeded,
                              failed, retired, health transitions, peak contexts
model/store                   instances.failed, instances.retired_at
measure                       boot peak from cpu.stat deltas over a fixed interval,
                              clamped by runtime.NumCPU()
```

Gate: `go test ./internal/infra/measure ./internal/runtime/report`, then one live
`--dry-run` whose JSON carries the per-instance block.

## What each phase does not touch

```
phase   leaves alone
1       admission gate, autoscaler, chart
2       routing, class handling, swap tier recording
3       the runner entirely
4       the slot pool's counting rules
5       autoscaler bounds
6       any behaviour, only what is measured
```

No phase writes a cgroup, changes the kubelet configuration, or touches
`config/swap-lab/`, `config/vm-swap-lab/` or `scripts/swap-allowance.sh`.

## Order and why

```
1  stops the bleeding: one dead browser can no longer absorb the queue
2  closes the case 1 cannot see: an instance that was never healthy
3  removes the blindness at the source: the probe covers the port in use
4  turns a failure into one retry instead of a lost session
5  makes saturation visible and keeps a spare instance coming
6  makes the next run answer this note's questions from its own output
```

Phases 1-3 are the ones that would have changed run B's outcome.

---
title: 'Phase 11 — CPU oversubscription and the datastore ceiling (2026-10-31 → 2026-11-01)'
date: 2026-11-04
kind: narrative
phase: 11
tags: [cpu, control-plane]
---

# Phase 11 — CPU oversubscription and the datastore ceiling (2026-10-31 → 2026-11-01)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../research/cpu-oversubscription/`](../research/cpu-oversubscription/README.md) ·
**handoff:** [`HANDOFF-cpu-oversubscription`](../research/session-lifecycle-elasticity/HANDOFF-cpu-oversubscription.md)

Phase 8 ended with a number it could not explain: 60 offered sessions, 42
completed, 17 refused — and the refusals were not OOM kills. This phase follows
that thread and finds the ceiling somewhere nobody was measuring: in the
datastore under the API server.

## The question

How far can CPU requests be pushed below limits before something other than the
workload gives way? The naive answer — "as far as the scheduler allows" — is
wrong, and the way it is wrong is the finding.

## What the notes establish

Seven notes plus a README, 1,283 lines:

| Note | What it settles |
|---|---|
| [`01`](../research/cpu-oversubscription/01-analysis.md) | The implementation, traced: where requests are consumed and what admission actually refuses on |
| [`02`](../research/cpu-oversubscription/02-kine-and-datastores.md) | kine, and whether MySQL would help — it moves the ceiling, it does not remove it |
| [`03`](../research/cpu-oversubscription/03-gemini-design-review.md) | An external design review checked against this cluster's own measurements |
| [`04`](../research/cpu-oversubscription/04-datastore-capacity-brief.md) | The datastore capacity brief and the prompt it answers |
| [`05` plan](../research/cpu-oversubscription/05-oversubscription-plan-and-pressure.md) | The oversubscription plan, pressure handling, and the three real gaps |
| [`05` write ceiling](../research/cpu-oversubscription/05-write-ceiling-and-fixes.md) | What stalls on writes, and the seven fixes with their evidence |
| [`06`](../research/cpu-oversubscription/06-riot-run-path.md) | What a lightweight Riot run actually does, so a load figure means something |

## The finding that changed the platform

kine commits to SQLite. Under write pressure those commits take **1–3 s**, and
the failure surfaces as an **API `Get`/`Delete` timeout in the session runner** —
not as a slow page, not as a CPU spike. Two defaults compound it:

- `PRAGMA busy_timeout=0` — zero patience, so contention returns
  `database is locked` immediately rather than waiting
  (`peer_endpoint_lease.go:156`, `controller.go:168`).
- One fsync per commit from the default `synchronous=FULL`.

The fix is a data source name, not a code change:

```
datastore-endpoint: "sqlite:///var/lib/rancher/k3s/server/db/state.db?_synchronous=NORMAL&_busy_timeout=30000"
```

**Measured on the live node 2026-11-01:** insert mean **64.6 → 2.06 ms**, **823
Slow SQL/hour → 0**, **9 fatals per 24 h → 0**
([`05` §8](../research/cpu-oversubscription/05-write-ceiling-and-fixes.md)).

The trade is stated in the same section and is not free: `synchronous=NORMAL`
removes one fsync per commit, so a process crash stays safe and a power loss can
lose the last transactions.

## The gap this phase leaves in the repository

The DSN was applied to the **live node**, and `config/k3s/` still carries no
`datastore-endpoint`. A reinstall from this repository would silently lose the
only fix that made the control plane survive 60 sessions. That is recorded here
as open work rather than quietly patched, because changing the k3s server flags
is a config change and this phase is a study.

## What this phase does not claim

Its README status says it plainly: **"Measurement added, experiments not yet
run."** The write-ceiling fix has a before/after on the live node; the
oversubscription ladder itself — the experiment that would say how far requests
can go — is designed and not executed. Everything above about pressure is
diagnosis, not a capacity number.

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `cab89c1` | 2026-10-31 | `feat(experiments-y6n): boot permits, node CPU sampling, and the CPU study folder` |
| `47601e4` | 2026-11-04 | `fix(vm-swap-lab): source lib.sh before up.sh waits for ssh` |

12 commits touch `research/cpu-oversubscription`, 2026-10-31 → 2026-11-04. The
2026-11-01 live measurement is recorded in the note, not in a commit — the note
is the artefact.

## What this phase leaves open

The oversubscription ladder (note `05` plan), the three gaps it names, and the
question of where the DSN belongs in `config/k3s/` so that the fix survives a
rebuild. Phase 12 then asks a related question from the other side: whether memory
can be bought the same way CPU was.

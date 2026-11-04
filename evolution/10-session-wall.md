---
title: 'Phase 10 — The session wall: the console grows a run dimension (2026-10-25 → 2026-10-30)'
date: 2026-11-04
kind: narrative
phase: 10
tags: [console, slots]
---

# Phase 10 — The session wall: the console grows a run dimension (2026-10-25 → 2026-10-30)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../apps/sandbox-console/`](../apps/sandbox-console/README.md),
[`../config/sandbox/`](../config/sandbox/README.md) · **design note:**
[`16.2`](../research/session-lifecycle-elasticity/16.2-session-wall-ui.md)

Phase 7's console answered "what is running now". Once phase 9 made a session
smaller than a pod, that answer stopped being sufficient: the interesting object
became **a run** — hundreds of sessions across dozens of browsers, each session a
slot that exists for thirty seconds and then does not.

## The question

Can the console draw a run so that three different questions have three different
answers on screen, without a new backend?

1. *Is the run healthy right now?* — a skyline of sessions per bucket.
2. *Where is the multiplicity?* — which of the eight tracks in each browser is
   carrying what.
3. *What happened to this one session?* — one slot card, with its own timeline.

## What shipped

The console gained a run dimension and four zooms into it:

| Piece | Where | What it draws |
|---|---|---|
| Run overview | `runs.py`, `render_runs.py` | The skyline: sessions per 30 s bucket for the life of a run |
| Wall | `render_runs.py` | Buckets → instances → slots, with glyph density as the signal |
| Lane zoom | `render_runs.py` | One instance's eight tracks on a shared clock |
| Slot zoom | `render_runs.py` | One session in one context: acquire, load, hold, release |
| Traffic | `traffic.py` | Synthetic sessions, so the wall has something to draw |
| Compare | `compare.py` | Two runs side by side |

36 files, **11,959 lines**, 11 test modules (`test_cdp`, `test_compare`,
`test_inventory`, `test_lifecycle`, `test_probes`, `test_recipes`, `test_records`,
`test_render`, `test_runs`, `test_server`, `test_traffic`).

The run data comes from a **read-only run SQLite** that `session-lab` writes. That
is the design decision that made the wall cheap: the console renders a finished
artefact and never asks the cluster for history it cannot reconstruct.

## The constraint that shapes the drawing

The CDP bridge serialises all eight slots of a browser behind one lock, so a frame
that lags lags across the whole instance. At 40 browsers × 8 slots a naive wall
would issue **320 capture calls per tick**; the capture budget is **0.5 fps at
wall zoom** and higher only for the slot in focus
([`16.2`](../research/session-lifecycle-elasticity/16.2-session-wall-ui.md) §7).

The consequence is a drawing that is honest about staleness: a card keeps its last
frame *and its timestamp*, and a stalled browser shows as a uniform glyph across
its eight tracks rather than as eight independent failures.

## The gap this phase could not close

`cdp-bridge.py` has no `sessionId`, so a context cannot be bound to a session
label with certainty above `--contexts-per-browser 1`. The wall therefore binds
by `instance` + hold window and marks the binding **by time**, exact only once the
slot id is stored. That is stated on the screen rather than smoothed over.

## What the note's own status says

`16.2` is filed **"Draft for review"** while carrying a section titled *"What
shipped in this pass"* and a *"Deployed"* subsection. Both are true: the wall
shipped, and the note describing it was never promoted past draft. This phase
document is the promotion.

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `e3c4cf6` | 2026-10-25 | `feat(sandbox-console): read-only pane over live Chromium sandboxes` |
| `5e538c0` | 2026-10-30 | `fix(experiments-y6n): the modal stacks with flex, never grid` |
| `0500212` | 2026-11-02 | `feat(experiments-y6n): cpu request 10m, pool ceiling 120, and the run path documented` |

24 commits touch `apps/sandbox-console` and 10 touch `config/sandbox`, spread
2026-10-25 → 2026-10-30 with the template change landing 2026-11-02.

## What this phase leaves open

The six slices listed in `16.2` §4 remain unbuilt, the `sessionId` gap blocks
exact slot-to-session binding, and the two runs `compare.py` can show are not yet
used to answer a question in writing.

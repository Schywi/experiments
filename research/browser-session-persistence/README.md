---
title: 'Browser session persistence'
kind: index
date: 2026-11-06
branch: docs-site
tags: [session-lifecycle, persistence, storage, index]
---

# Browser session persistence

| | |
|---|---|
| Status | Two runs recorded: the arm (`20261108T200728Z`), then MinIO with a restore (`20261108T232328Z`) |
| Date | 2026-11-06 |
| Question | Once a session's restorable state exists, where does it go, how is it written, and what does it cost at 300k sessions a day? |
| Harness | `apps/session-lab` — capture and restore both run against MinIO; the workload still writes no state |
| Notes | [01 — the persist gateway, the flag and the budget](01-persist-gateway-and-budget.md) · [02 — the first persist arm](02-persist-arm-first-run.md) · [03 — the first MinIO run](03-first-minio-run.md) |

## Scope

A session's restorable state is whatever Chromium can persist for one browser context. The
gateway treats that as one opaque blob, so the inventory of Chromium surfaces stays behind a single
interface and nothing above it changes when the blob's contents change.

The live-set capacity used throughout is **350 slots**, stated by the operator. Notes written
before 2026-11-06 carry 168 from the CPU-request ceiling measured in
[note 18](../session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md); that figure is
superseded here and is not used in any arithmetic in this study.

## Open

Note 03 records the first run against MinIO: 50 sessions, 7 blobs of 68 bytes, and a restore
arm that booted 2 of them on a fresh claim in 461 ms mean. The write→read path is proven end
to end. What is not proven is that *state* survives, because the workload performs no
actions: every blob is `{"version":1,"origin":"https://example.com"}` and every restore
reports zero cookies and zero items.

Three things are open:

- **A workload that writes state.** A fixture page that sets a cookie and a `localStorage`
  item on load, and an assertion read back in the restored context. Without it every run
  reports zeros.
- **Capture CPU.** `persist-lab` supplies no `Sampler`, so no run has costed the capture;
  note 01 §5's 0.28 cores at a 60 s cadence is still `[U]`.
- **Eviction under a budget.** `--persist-budget` is exercised only by unit tests against an
  in-memory store; no run has made MinIO delete anything.

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
| Status | First arm measured (`20261108T200728Z`), four defects found |
| Date | 2026-11-06 |
| Question | Once a session's restorable state exists, where does it go, how is it written, and what does it cost at 300k sessions a day? |
| Harness | `apps/session-lab` — capture is built, restore is built but not wired into the load arm |
| Notes | [01 — the persist gateway, the flag and the budget](01-persist-gateway-and-budget.md) · [02 — the first persist arm](02-persist-arm-first-run.md) |

## Scope

A session's restorable state is whatever Chromium can persist for one browser context. The
gateway treats that as one opaque blob, so the inventory of Chromium surfaces stays behind a single
interface and nothing above it changes when the blob's contents change.

The live-set capacity used throughout is **350 slots**, stated by the operator. Notes written
before 2026-11-06 carry 168 from the CPU-request ceiling measured in
[note 18](../session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md); that figure is
superseded here and is not used in any arithmetic in this study.

## Open

Note 02 records the first run of the capture arm: 50 sessions, 20 % selected, 19 blobs of 68
bytes each, 50/50 sessions successful. It found four defects in the arm — a budget that
refuses in the counter and writes anyway, a hardcoded generation, a capture hook that fires
on failed context creation, and a rate selector that delivers 38 % when asked for 20 % at
small id ranges.

Three things are open:

- **The restore arm.** `cmd/persist-lab` writes blobs and never reads one back, so nothing
  yet answers whether a session can boot from a stored blob under load.
- **MinIO as the store.** The design in note 01 puts blobs in MinIO behind the ledger's
  reserve/commit/evict path; the code writes to a local directory. The directory is the
  control arm for the comparison.
- **Real blob sizes.** Every size in note 01 §6 is a projection until a page carrying
  cookies and `localStorage` is captured.

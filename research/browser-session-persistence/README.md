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
| Status | Design agreed, nothing built |
| Date | 2026-11-06 |
| Question | Once a session's restorable state exists, where does it go, how is it written, and what does it cost at 300k sessions a day? |
| Harness | `apps/session-lab` — it has no capture or restore path today |
| Notes | [01 — the persist gateway, the flag and the budget](01-persist-gateway-and-budget.md) |

## Scope

A session's restorable state is whatever Chromium can persist for one browser context. The
gateway treats that as one opaque blob, so the inventory of Chromium surfaces stays behind a single
interface and nothing above it changes when the blob's contents change.

The live-set capacity used throughout is **350 slots**, stated by the operator. Notes written
before 2026-11-06 carry 168 from the CPU-request ceiling measured in
[note 18](../session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md); that figure is
superseded here and is not used in any arithmetic in this study.

## Open

The test strategy — an isolated single-session round trip, and a stress run that writes past the
budget — is not written up yet. It is the next note in this directory.

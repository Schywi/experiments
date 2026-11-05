---
title: 'Assistant'
kind: index
date: 2026-11-04
tags: [assistant]
---

# Assistant

Investigations into the assistant itself: how it is wired, how an answer is
assembled, where its evidence comes from, and what a topology question needs.

| Note | Kind | Subject |
|---|---|---|
| [AI workflow](ai-workflow.md) | investigation | The local infrastructure assistant end to end — tools, routing, the model |
| [Assistant response pipeline v2](assistant-response-pipeline.md) | plan | Structured facts → dual response → streaming |
| [Evidence provenance and cartography](evidence-provenance.md) | investigation + plan | Where each answer's facts come from, and the deep links that prove it |
| [Reasoning model, topology tool, Cilium observability](reasoner-and-topology.md) | investigation | The three layers a topology question needs |

Shipped contracts for assistant capabilities live one level up in
[`features/`](../features/README.md); this folder holds the reasoning behind them.

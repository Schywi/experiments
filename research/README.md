---
title: 'Research'
date: 2026-11-04
kind: index
---

# Research

Design notes, RFCs, feature contracts and experiment records. Everything here is
tracked in git; `docs/` next to it is gitignored and holds machine-specific
runbooks instead.

Read [`../ROADMAP.md`](../ROADMAP.md) for the platform's current state and
[`../evolution/`](../evolution/) for why each layer exists. This directory holds
the reasoning: what was proposed, what was measured, what broke.

## Studies — multi-note records

| Study | Notes | Question it answers |
|---|---|---|
| [Session lifecycle elasticity](session-lifecycle-elasticity/README.md) | 20 notes + handoff | What does a browser session cost, how many fit on this host, and where does the control plane stop? |
| [CPU oversubscription](cpu-oversubscription/README.md) | 6 notes | How far can CPU requests be pushed below limits before the API server and Chromium suffer? |
| [Swap as capacity](swap-capacity/README.md) | 1 study, 5 sections | Can zram swap raise concurrent sessions, by how much, and which compression ratio pays for it in CPU? |
| [Browser session persistence](browser-session-persistence/README.md) | 2 notes | Where a session's restorable state is written, how the budget is enforced, and what it costs at 300k sessions a day? |

## Feature contracts

[`features/`](features/README.md) — what the assistant can do today and the
contract behind each capability, one file per feature, plus the edge-case
catalogue.

## Loose documents, grouped

The root holds no documents of its own any more: four folders collect what used
to sit loose here, and each folder has an index.

| Folder | Holds | Index |
|---|---|---|
| [`assistant/`](assistant/README.md) | How the assistant is wired, how an answer is assembled, where its evidence comes from, what a topology question needs | 4 notes |
| [`rfcs/`](rfcs/README.md) | Proposals written to be argued with — promotion, the console API, the console itself, traffic splitting | 4 RFCs |
| [`platform/`](platform/README.md) | What the cluster runs on, and what would change to host it elsewhere | 2 notes |
| [`prompts/`](prompts/README.md) | Research briefs that can be re-run against a later state | 1 brief |

## How a note is labelled

Every note carries YAML frontmatter with two axes, because "what is this" and
"where does it stand" are different questions:

| Field | Values | Question it answers |
|---|---|---|
| `kind` | `analysis` `experiment` `audit` `design` `rfc` `feature` `handoff` `narrative` `index` | What kind of document is this? |
| `state` | `hypothesis` `research` `implemented` `validated` `benchmarked` `frozen` `superseded` `unknown` | Where does it stand? |

`state` is derived from the `| Status |` row the note already keeps — the table
is the human sentence, the frontmatter is the machine-readable copy. Index pages
and narrative write-ups carry no `state`: they make no claim about current state.
`state: unknown` means the note has no status row, so nothing was asserted on its
behalf; those 27 notes need an owner's decision, not a guess.

Each state is also emitted as a tag, `state-hypothesis`, `state-implemented` and
so on, so the site can list them: `/tags/state-hypothesis` is every proposed and
unbuilt piece of this repository in one page. The remaining fields are `phase`,
`date`, `branch`, `commits` (only hashes the note itself cites) and subject
`tags`.

**What `date` means.** `date` is the date the document came into existence — the
date of the commit that first introduced the file, not the date the file was last
touched. A note's own `| Date |` table row keeps its separate meaning: the date
the run or measurement happened. The two are allowed to differ, and where they do
the difference is real information — a run recorded on 2026-10-29 and written up
on 2026-10-30 carries `date: 2026-10-30` in the frontmatter and `2026-10-29` in
its table. Neither replaces the other.

## How to read a claim here

Notes state what was measured, what was inferred, and what is unknown. Numbers
carry the run id, the commit or the file path that produced them; where none of
those exist the note says so rather than rounding the gap away.

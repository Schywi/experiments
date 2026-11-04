---
title: 'Research'
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

## Feature contracts

[`features/`](features/README.md) — what the assistant can do today and the
contract behind each capability, one file per feature, plus the edge-case
catalogue.

## Notes and RFCs

| Note | Kind | Subject |
|---|---|---|
| [AI workflow](ai-workflow.md) | investigation | The local infrastructure assistant, end to end |
| [Assistant response pipeline v2](assistant-response-pipeline.md) | plan | Structured facts → dual response → streaming |
| [Evidence provenance and cartography](evidence-provenance.md) | investigation + plan | Where each answer's facts come from, and the deep links that prove it |
| [Native k3s + GPU + CPU models](native-k3s-gpu-models-plan.md) | plan | The pivot off k3d, and the recorded decisions behind it |
| [Platform cockpit deep research prompt](platform-cockpit-deep-research-prompt.md) | prompt | A Pareto cockpit for Kubernetes, as a research brief |
| [Production hosting on GKE or GCE](production-hosting-gke-gce.md) | analysis | What cannot work there, and what must change |
| [Reasoning model, topology tool, Cilium observability](reasoner-and-topology.md) | investigation | The three layers a topology question needs |
| [RFC — environments and promotion](rfc-environments-and-promotion.md) | RFC | Immutable artifacts and how they move between environments |
| [RFC — sandbox-console API](rfc-sandbox-api.md) | RFC | The HTTP API the console exposes to tools |
| [RFC — sandbox-console](rfc-sandbox-console.md) | RFC | A pane to inspect, drive and demo `agent-sandbox` |
| [RFC — traffic split](rfc-traffic-split.md) | RFC | Canary and traffic splitting for `agent-sandbox`, experimental |

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

## How to read a claim here

Notes state what was measured, what was inferred, and what is unknown. Numbers
carry the run id, the commit or the file path that produced them; where none of
those exist the note says so rather than rounding the gap away.

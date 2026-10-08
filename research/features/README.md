# Features

Local-only notes (`docs/` is gitignored) describing what the assistant can do
and how each capability works. One file per feature.

| Feature | File | Summary |
|---|---|---|
| Evidence provenance & deep links | [`evidence-provenance.md`](evidence-provenance.md) | Every answer exposes the tool, args, time, source, a copyable command/PromQL, and a Grafana/Hubble deep link where supported. |

## How to use these docs

Each feature file follows the same shape: **what it does → the schema/contract →
how it is implemented (files) → configuration → how to verify → limits.** They
describe the shipped behaviour, not a plan.

# Features

Local-only notes (`docs/` is gitignored) describing what the assistant can do
and how each capability works. One file per feature.

| Feature | File | Summary |
|---|---|---|
| Evidence provenance & deep links | [`evidence-provenance.md`](evidence-provenance.md) | Every answer exposes the tool, args, time, source, a copyable command/PromQL, and a Grafana/Hubble deep link where supported. |
| `get_topology` / `get_topology_snapshot` | [`get-topology.md`](get-topology.md) | **Proposed (frozen).** Cluster topology in three layers — live structure (k8s API), network (Cilium/Hubble), snapshot (Cartography ≤6h). |
| LLM concurrency limit + bounded FIFO queue | [`concurrency.md`](concurrency.md) | **In-process (single replica).** Bounds concurrent LLM-bound requests (default 1) with a bounded FIFO queue (default 8); overflow → `429` + `Retry-After`. |

## Known edge cases

Failure modes, corrections, and their status live in
[`edge/`](edge/) — the catalogue (`edge/edge-cases.md`), the message-shape ASCII
(`edge/message-structure.md`), and how to reproduce them.

## How to use these docs

Each feature file follows the same shape: **what it does → the schema/contract →
how it is implemented (files) → configuration → how to verify → limits.** They
describe the shipped behaviour, not a plan.

# Edge cases

Local-only? **No — tracked.** This directory documents the edge cases and failure
modes found in the assistant, per feature area, so they are reproducible and
regression-checkable. It is the counterpart to `../` (which documents shipped
behaviour).

## How these were found

| Method | What it catches |
|---|---|
| `scripts/assistant-edge-tests.py` | black-box battery: classifies each reply `ok` / `json_leak` / `repetition` / `empty` / `error` |
| `scripts/assistant-benchmark.py` | per-stage latency + tokens (plan vs correlate vs chat) |
| direct LLM probes (`/v1/chat/completions`) | model-level behaviour (JSON validity, grammar/response_format) |
| `/plan?kind=qwen\|laya` | planner A/B choice + latency, without the correlate cost |
| live `kubectl` / `curl` | end-to-end contract, provenance, rollout artifacts |

```bash
EDGE_URL=http://192.168.0.243 python3 scripts/assistant-edge-tests.py
```

> Benchmark hygiene: pin the revision (pause Argo auto-sync, or the run measures
> someone else's rollout — see **E0**).

## Index

| Doc | Area |
|---|---|
| [`edge-cases.md`](edge-cases.md) | the catalogue: contract, planner, provenance, tool-args |
| [`message-structure.md`](message-structure.md) | ASCII of the `/investigate` message shape and the UI |

## Findings at a glance

| # | Area | Edge case | Status |
|---|---|---|---|
| E1 | contract | invalid JSON leaked as `screen_md` (fail-open) | **fixed** (`experiments-ve1`) |
| E2 | contract | degenerate repetition loop | **fixed** (`repeat_penalty`) |
| E3 | contract | correlate truncated → JSON unparsable | **fixed** (450 tokens + fence strip) |
| E4 | contract | correlate still fails the shape sometimes | open (degrades cleanly) |
| E5 | planner | Laya never abstains (forced choice) | open (harmless) |
| E6 | planner | `run()` ignores `kind` — A/B needs a 2nd deployment | by design |
| E7 | provenance | `_SOURCE["get_topology"]` maps a tool that doesn't exist | open (cosmetic) |
| E8 | provenance | doc claims "model output never builds a link" — inaccurate | open (doc) |
| E9 | provenance | `at` is enrich time, shared by a whole batch | open (minor) |
| E10 | provenance | deep links die without `home.arpa` DNS / matching datasource UID | documented limit |
| E11 | tooling | plan picks required-arg tools with no args → `TypeError` fact | open (`experiments-0o9`) |
| E12 | tooling | **no `cartography` tool exists** (assumption) | correction |
| E13 | concurrency | `/chat` runs its tool call on the event loop → stalls the process | open |
| E14 | concurrency | `/chat` and `/investigate` share one limiter (no chat priority) | by design |

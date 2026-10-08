# Benchmark — frozen baseline

Measurements for the local assistant's two-call pipeline (`plan -> fan-out ->
correlate`) and the isolated Laya-planner experiment. Treat everything under
`baseline/` as **frozen**: do not edit those numbers; add new dated files instead.

## Layout

```text
benchmark/
├── assistant-benchmark.py      # planner benchmark (drives POST /plan)
└── baseline/
    ├── planner-laya.json       # Laya planner: accuracy + latency, 27 labeled questions
    ├── planner-qwen.json       # Qwen planner (current): accuracy + latency, same questions
    └── correlate-metrics.txt   # correlate stage: tokens/prefill/generation/context
```

## The frozen question set

`assistant-benchmark.py` holds a fixed set of **27 labeled questions**
(`(question, gold bundle label)`), labelled from the plan catalog
(`unhealthy | traffic | drop | default`). The first five are the pre-existing
prompts; the other twenty-two were added for the planner comparison. This set is
the baseline — **do not add or change questions** without a new dated baseline.

## How to run

```bash
# production assistant (Qwen planner), or the assistant-laya deployment with ?kind=
BENCH_URL=http://192.168.0.243                       python3 benchmark/assistant-benchmark.py
BENCH_URL=http://127.0.0.1:18081 BENCH_KIND=laya     python3 benchmark/assistant-benchmark.py
BENCH_URL=http://127.0.0.1:18081 BENCH_KIND=qwen     python3 benchmark/assistant-benchmark.py
```

`/plan` returns `{planner, plan_ms, tools}`; `planner` is label-qualified
(`laya:<l>`, `bundle:<l>`, `laya-fallback:<l>`) or `llm` (Qwen free-form).

## Frozen results (2026-10-08)

| Planner | Accuracy | Plan latency | Notes |
| --- | --- | --- | --- |
| Laya (`baseline/planner-laya.json`) | 18/27 = 66.7% | p50 454 ms / p95 539 ms | all 27 via Laya |
| Qwen (`baseline/planner-qwen.json`) | 16/27 = 59.3% | p50 23.6 s / p95 70.6 s | 17/27 keyword bundle @ 0 ms; 10/27 LLM, no label |

Correlate stage (`baseline/correlate-metrics.txt`): avg **8.9 s** per call
(prefill 1.4 s + generation 7.5 s), ~289 prompt tokens, ~90 output tokens,
~13 tok/s, max ≤ 45 s on the production assistant.

## Related material

- `apps/assistant/` — the pipeline under test (`pipeline.py`, `llm_metrics.py`).
- `research/reasoner-and-topology.md` — prior analysis of the planner question.

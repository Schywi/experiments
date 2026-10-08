# Correlate latency — investigation and the smallest experiment

Frozen 2026-10-08. Read alongside `benchmark/baseline/` and
`benchmark/correlate-experiment.py`.

## Question

"Why does correlation take ~143 seconds, and what is the smallest experiment
that reduces correlate latency without changing the investigation semantics
(and without changing the model)?"

## Finding 1 — 143 s is an outlier, not the baseline

`correlate` is the second LLM call in the pipeline; the investigation (plan +
tool fan-out) is unchanged by anything here.

| environment | correlate / call | prefill | generation | tok/s |
| --- | --- | --- | --- | --- |
| **production assistant** (21 calls) | **8.9 s** | 1.4 s | 7.5 s | 13.0 |
| **assistant-laya** (3 calls, Laya co-resident) | **30.1 s** | 8.7 s | 21.3 s | 9.0 |

Production `correlate` request_seconds max is **≤ 45 s** (histogram: p50 ≈ 8–9 s).
The 143 s sample came from a single `/investigate` on the `assistant-laya` pod and
was **not** reproduced (its context: Laya + llama.cpp on the same 6-core node).

**Cause of the outlier:** resource contention. On `assistant-laya`, prefill
collapsed from **1241 tok/s → 79 tok/s** (16×) while generation fell 13 → 9 tok/s.
A 16× prefill collapse is a CPU/memory-bandwidth signature: Laya (~2–3 GiB,
torch, up to 6 threads) co-scheduled with llama.cpp (`--threads 6`) oversubscribes
the single 6-core node. So "correlation takes 143 s" is really "correlation is
starved when Laya shares the node."

## Finding 2 — correlate is generation-bound at ~13 tok/s

From the production baseline (`baseline/correlate-metrics.txt`): ~289 prompt
tokens, ~90 output tokens, prefill 1.4 s, generation 7.5 s. Generation is **84%**
of correlate and runs at a flat **~13 tok/s**. Prefill is small and mostly cached
after the first call. The context window (4096) is not a factor — the prompt is
~300 tokens.

## The experiment — `correlate-experiment.py`

Six variants against the **same** llama.cpp server, same correlate system prompt,
fixed question + evidence. Only the output budget, evidence size, and prompt
wording vary. Investigation semantics are untouched (no tool changes).

| variant | ctx tok | out tok | prefill ms | gen ms | total ms | tok/s |
| --- | --- | --- | --- | --- | --- | --- |
| baseline (max 450) | 372 | 101 | 2301¹ | 7501 | 9913 | 13.5 |
| cap 220 | 372 | 92 | 106 | 7098 | 7211 | 13.0 |
| cap 120 | 372 | 120 | 67 | 9183 | 9260 | 13.1 |
| **terse (max 450)** | 353 | **74** | 2408¹ | **5681** | **8116** | 13.0 |
| terse + cap 120 | 353 | 112 | 129 | 8781 | 8918 | 12.8 |
| half evidence (max 450) | 238 | 170 | 90 | 13443 | 13552 | 12.6 |

¹ the two ~2.3 s prefill figures are the first two calls (cold KV cache); all
later prefill is ~70–130 ms.

### What the numbers say

- **tok/s is constant (~13) in every variant.** Latency is therefore *only* a
  function of output-token count — the lever is fewer generated tokens.
- **Capping `max_tokens` does not help.** The model emits ~90–170 tokens on its
  own; the cap only truncates a longer answer (cap 120 even produced 120 tokens
  and 9.2 s). Truncation hurts quality without saving time.
- **Reducing evidence makes it worse** (238 ctx tokens → 170 output → 13.4 s):
  less context → the model rambles more.
- **A terse prompt wins**: 74 output tokens, generation 5.7 s (from 7.5 s),
  total ~9.9 s → 8.1 s (**~18 %**), and the answer is *cleaner* — short
  facts/hypotheses bullets instead of prose.

## Smallest experiment that works (recommendation)

**Change only the correlate prompt wording — `pipeline._CORRELATE_SYS` → terse.**
No model change, no tool change, no investigation-semantics change. Expected:
~18 % correlate latency reduction and terser, well-structured answers.

Not worth doing (measured): lowering `CORRELATE_MAX_TOKENS`; shrinking
`EVIDENCE_CHARS`. Out of scope by request: changing the model / quantization.

**Ceiling:** with the model fixed, correlate cannot go below ~`output_tokens / 13`
seconds; the only remaining lever is generation speed (threads, `--flash-attn`,
or a smaller/faster model — all deferred).

## Isolation status

The Laya planner remains **isolated**: it lives behind `PLANNER_KIND=laya` in the
separate `assistant-laya` Deployment. The production assistant sets no
`PLANNER_KIND` and is unchanged. Nothing here integrates Laya into production.

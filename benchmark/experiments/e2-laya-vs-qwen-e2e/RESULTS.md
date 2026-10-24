# E2 results — does Laya planning reduce end-to-end latency vs Qwen planning?

**Answer: No.** With both models isolated (Guaranteed QoS, prod paused),
correlation dominates end-to-end latency in *both* arms; swapping the planner is
lost in the noise. In this run the Laya arm was even slightly slower end to end,
but that gap (~4 s of ~20 s) is correlate variance, not a planning effect.

## Configurations compared

| arm | planner | correlation | notes |
| --- | --- | --- | --- |
| `prod` | Qwen (bundle 17 / LLM 10) | Qwen (shared, Burstable) | real prod assistant, `192.168.0.243` |
| `qwen-plan` | Qwen (bundle 17 / LLM 10) | Qwen (`llm-exp`, Guaranteed 3 cpu) | isolated stack |
| `laya-plan` | Laya (all 27) | Qwen (`llm-exp`, Guaranteed 3 cpu) | isolated stack |

`qwen-plan` vs `laya-plan` share the **identical** isolated Qwen, so that pair is
the clean planner comparison.

## Frozen 27-question results

| metric | prod (as-is) | qwen-plan (isolated) | laya-plan (isolated) |
| --- | --- | --- | --- |
| **total end-to-end** mean | 31.4 s | **19.9 s** | **23.9 s** |
| total p50 / p95 | 26.1 / 57.1 s | 19.4 / 41.4 s | 22.0 / 43.1 s |
| **correlation** mean | 26.2 s | 18.7 s | 22.0 s |
| correlation p50 / p95 | 25.1 / 42.7 s | 17.4 / 37.6 s | 19.9 / 41.5 s |
| **planning** mean | 13.1 s (LLM path, 10 rows) | 2.7 s (LLM path, 10 rows) | 1.5 s (all 27 rows) |
| **planner accuracy** | 16/27 | 16/27 | **18/27** |
| Qwen correlate in/out tokens | ~125–960 / 51–339 | ~125–766 / 39–450 | ~470–730 / 61–450 |

## Reading it

1. **Correlation is ~85–92% of total latency in every arm.** Planning is a small
   slice; a 1–2 s planning change cannot move a 20 s total.
2. **The planner swap is within noise.** qwen-plan 19.9 s vs laya-plan 23.9 s —
   the *correlate* means themselves differ by 3.4 s (18.7 vs 22.0) while Qwen was
   identical, i.e. run-to-run variance (correlate p95 ≈ 40 s). Planning explains
   none of it. **Laya did not make end-to-end faster.**
3. **Laya plans faster on the hard rows** (1.5 s for all 27 vs Qwen 2.7 s on the
   10 non-bundle rows) **and is more accurate** (18/27 vs 16/27) — real, but it
   does not show up end to end because correlation swamps it.
4. **`prod` is slower in absolute terms** (31.4 s) because its Qwen is Burstable
   (`requests: 1`) and shares the node; the isolated arms show what a
   properly-scheduled Qwen does (19.9 s). Isolation *did* help Qwen — just not by
   changing the planner.

## Caveats

- **One run per arm.** Correlate variance is large (p95 ≈ 40 s); treat the
  qwen-plan-vs-laya-plan gap as "no measurable difference", not "Laya is 20%
  slower".
- **Isolated Qwen ran 3 threads** (not 6): the 6-core node only frees ~4.6 cores
  after pausing prod models, so `llm-exp` was Guaranteed 3 cpu with `--threads 3`.
  Absolute numbers are therefore not comparable to prod's 6-thread Qwen.
- **Guaranteed QoS, not pinning.** `requests == limits` gives CFS reservation;
  true CPU pinning needs the kubelet static CPU-manager policy (host root).
- No prompt / model / quantization / evidence changes were made.

## Raw data

- `baseline/prod.json`, `baseline/qwen-plan.json`, `baseline/laya-plan.json`
  (per-question: planner, plan/tool/correlate/elapsed ms, Qwen in/out tokens, answer).

# Experiment E2 — does Laya planning reduce end-to-end latency vs Qwen planning?

- **Status:** complete
- **Tag:** `e2-laya-vs-qwen-e2e`
- **Base commit:** `0f921e109218ac1cc32592049117f126586357ae`
- **Model / prompt / evidence:** UNCHANGED (no optimization, no prompt edits, no
  quantization change). Only the *planner* differs between arms.

## Question

> Does replacing Qwen planning with Laya actually reduce end-to-end latency when
> the two models are properly isolated from CPU/memory competition?

## The pipeline under test (measured end to end, sequentially)

```text
question -> PLANNER -> parallel tools -> QWEN correlation -> answer
```

## Arms

| arm | planner | correlation | where |
| --- | --- | --- | --- |
| prod | Qwen (frozen prod) | Qwen | real production assistant `192.168.0.243` |
| qwen-plan | Qwen | Qwen (`llm-exp`) | isolated stack |
| laya-plan | Laya | Qwen (`llm-exp`) | isolated stack |

`qwen-plan` and `laya-plan` share the **identical** Qwen (`llm-exp`, same model,
`-c 4096`, `--threads 4`), so `qwen-plan vs laya-plan` isolates the planner
change; `prod` is the as-is reference.

## Isolation (the whole point)

The single node is **6 CPU**. Prod `llm`/`laya` are Burstable (`requests: 1`,
`limits: 6`) — neither is guaranteed a core, which is why load collapses prefill.

For the experiment:

1. **Pause prod models**: scale `models/llm` and `models/laya` to 0 (Argo
   auto-sync suspended for the window), then **restore to 1** afterward.
2. **Dedicated, Guaranteed QoS**:
   - `llm-exp`: `requests == limits == cpu: 5`, `--threads 4`, model unchanged.
   - `laya-exp`: `requests == limits == cpu: 1`, `--threads 1`.
   - Integer requests == limits => Guaranteed QoS => CFS reserves the cores, so
     Laya cannot steal from Qwen during correlation.

> Caveat: this is CFS reservation, not hard CPU pinning (that needs the kubelet
> static CPU-manager policy, which requires host root). It is the strongest
> isolation available without modifying the node.

## Method

`e2e-runner.py` drives the frozen 27-question set through `POST /investigate` and
records per question: planner, `plan_ms`, `tool_ms`, `correlate_ms`, `elapsed_ms`,
Qwen correlate input/output tokens (from `/metrics` deltas), and the answer text.

## Results

See [RESULTS.md](RESULTS.md).

**Answer: No.** With both models isolated (Guaranteed QoS, prod paused),
correlation dominates (~85–92% of total) in both arms; the planner swap is lost
in the noise. `qwen-plan` mean total 19.9 s vs `laya-plan` 23.9 s — the gap is
correlate variance, not planning. Laya plans faster and scores higher (18/27 vs
16/27), but it does not move end-to-end latency.

## Files

- `e2e-runner.py` — the end-to-end harness.
- `baseline/` — captured JSON per arm + the comparison.

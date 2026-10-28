# Evaluator: read the whole sentence before any tool runs

## The defect

`apps/assistant/pipeline.py`, `plan()`, Qwen path:

```python
lowered = question.lower()
if any(p in lowered for p in _SANDBOX_ROUTE):        # "screenshot" is in this tuple
    sandbox = _sandbox_batch(lowered, question)
    if sandbox:
        return sandbox, "route:sandbox", 0.0          # returns; rest of the sentence is gone

if any(k in lowered for k in _METRIC_INTENTS):        # never reached for that sentence
    query, ms = _write_promql(...)
```

The first matching keyword decides the whole turn. Measured:

```
Q: "any dropped packets in the worm-lab namespace, and take a screenshot of browser-pool-xnxp"
   planner: route:sandbox        (sandbox_shot only; the packet question is discarded)

Q: "are there any dropped packets in worm-lab?"
   planner: route:prometheus     (query_prometheus, grafana link)
```

One sentence can contain two requests. The router can only return one, so one of them is
dropped without a trace.

## What changes

One LLM call that reads the sentence and returns the list of tasks. It decides *what is being
asked*, never *how a tool behaves*.

```
   YOUR SENTENCE ────────────────────────────────────────────┐
   "any dropped packets in the worm-lab namespace,           │
    and take a screenshot of browser-pool-xnxp"              │
                                                             v
                                        ┌──────────────────────────────┐
                                        │  EVALUATOR   (1 LLM call)    │
                                        │  reads, never executes       │
                                        │  answers ONE question:       │
                                        │  "what is being asked here?" │
                                        └──────────────────────────────┘
                                                     │
                                                     │ JSON
                                                     v
                        { "tasks": [
                            { "tool": "query_prometheus",
                              "why": "dropped packets in worm-lab" },
                            { "tool": "sandbox_shot",
                              "why": "screenshot of browser-pool-xnxp" } ] }
                                                     │
                        ┌────────────────────────────┴───────────────────┐
                        v                                                v
              query_prometheus                                 sandbox_shot
        (PromQL specialist, unchanged)                  (id extract + guard, unchanged)
        writes the query, repairs once                  checks the id exists
                        │                                                │
                        └────────────────────────────┬───────────────────┘
                                                     v
                                            facts (one per task)
                                                     │
                                                     v
                                   ┌───────────────────────────────┐
                                   │  CORRELATE  (1 LLM call)      │
                                   └───────────────────────────────┘
                                                     │
                                                     v
                              answer: grafana link  +  sandbox frame
```

| today | with the evaluator |
|---|---|
| `if "screenshot" in sentence: return` — one word decides, the rest is discarded | the evaluator reads the whole sentence and lists every task |
| `if "packet" in sentence: return` — the same race, the other side | same evaluator, same list |
| one intent per answer | one entry per task |

## Scope

1. Add `evaluate(question)` — one JSON-mode LLM call returning
   `{"tasks": [{"tool", "args", "why"}], "uncovered": [...]}` for independent asks, or
   `{"recipe": "<name>", "args": {...}}` when the sentence is one ordered chain with a handoff.
   Args may be empty; when they are, the tool's existing schema decides whether the call is legal.
   Recipes are specified in `recipes.md`.
2. Delete the two `if ... return` blocks (`_SANDBOX_ROUTE`, `_METRIC_INTENTS`) from `plan()`, and
   the `route:sandbox` / `route:prometheus` sources with them.
3. Run the returned tasks through the existing machinery: same `tools.REGISTRY`, same
   `tool_schema.check_args`, same PromQL specialist and one repair, same `sandbox._require_sandbox`,
   same `propose_action` for writes, same `correlate()`.

Unchanged: PromQL generation and its repair loop, the sandbox guards, the strict schemas, link
gating (`ok == true` for Grafana, id present for sandbox), `propose → confirm` for actions, the
`laya` planner path, and every test that covers them.

## Rules the evaluator prompt must carry

- One task per distinct thing asked. A sentence with two asks returns two tasks.
- Cover the whole sentence; put anything with no matching tool in `uncovered` instead of guessing.
- Never invent a sandbox id, a namespace or a metric name. Leave the argument out when the
  sentence does not contain it; the tool or its guard will reject the call and say so.
- Cap at 5 tasks.

## Acceptance

- `any dropped packets in worm-lab, and take a screenshot of <live-id>` produces two facts:
  `query_prometheus` (ok, grafana link) and `sandbox_shot` (ok, frame + deep link).
- One invalid task never cancels a valid one: a stale sandbox id leaves the metrics fact intact,
  and the failed fact keeps its error text.
- A sentence with one ask produces one task.
- A reply that is not JSON yields `tasks: []` and falls back to the current single-plan path; the
  assistant still answers.
- PromQL tests, sandbox tests and guardrail tests stay green.

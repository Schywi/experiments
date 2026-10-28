# Recipes: ordered chains with a handoff

A flat task list cannot express "use the URL the last step produced". Recipes can. Three
mechanisms cover the assistant's question shapes, and each is used where it is exact:

| mechanism | shape it serves | example |
|---|---|---|
| task list, parallel | independent asks | `dropped packets in worm-lab, and a screenshot of browser-pool-x` |
| recipe, ordered | one ask with a data handoff | `metrics of X, then a sandbox on that URL, then a screenshot` |
| ReAct loop (`agent.py`) | depth not knowable in advance | `why is this pod restarting` |

The evaluator (see `evaluator.md`) chooses between the first two; the third stays on its own
endpoint. No framework is involved: a recipe is a Python function over a dict.

## The workflow this exists for

> "Check the metrics of the LAN dashboards, then create a sandbox using that LAN IP, then take a
> screenshot."

The URL the sandbox opens is the URL step 1 produces — `http://192.168.0.245/…` from a Grafana
link, or `http://192.168.0.241/?namespace=…` from a Hubble link. No pod IP lookup and no IP→URL
mapping: the link is the input.

```
sentence
  │
  └─ evaluator (1 LLM call) ──> { "recipe": "observe_url", "args": { "promql_hint": "…" } }
        │
        └─ recipe (deterministic python, no LLM)
             1. query_prometheus(promql)        ──> fact,  url = grafana link
             2. create_sandbox(kind="claim")    ──> id                (direct, no gate)
             3. sandbox_open(id, url)           ──> navigated
             4. sandbox_shot(id)                ──> frame, deep_link
                   │
                   └─> correlate (1 LLM call)
                         │
                         └─> answer: Open in Grafana + <img frame> + Open in sandbox
```

The state a recipe carries is a plain dict:

```
{ "question": str, "args": {...},
  "facts": [ ... ],          # one per tool call, each with ok/error/links
  "url": str | None,         # the handoff
  "sandbox_id": str | None,
  "stopped": str | None }    # why it stopped, if it did
```

A step that fails records a failed fact and stops the chain after it; steps already executed keep
their evidence, and `stopped` names the step.

## Recipes shipped

### `observe_url`

1. `query_prometheus` with a PromQL the specialist writes (unchanged path, one repair).
2. Grafana URL from `provenance` (only when the query executed).
3. `create_sandbox(kind="claim")` — direct call, returns the tile id.
4. `sandbox_open(id, url)` — open the URL from step 2.
5. `sandbox_shot(id)` — frame plus `<CONSOLE_URL>/s/<id>`.

Triggers that select it: a request that asks for metrics **and** a sandbox, or a screenshot of a
LAN dashboard.

### `observe_cilium`

1. `get_pods(namespace)` or `get_events(namespace)` for the namespace named in the question.
2. Hubble URL `http://192.168.0.241/?namespace=<ns>` from `provenance`.
3. `create_sandbox(kind="claim")`.
4. `sandbox_open(id, url)`.
5. `sandbox_shot(id)`.

## Decisions

- **`create_sandbox` is a direct, level-1 call.** It runs inside a recipe without confirmation.
- **`destroy_sandbox` stays behind `propose → /actions/confirm`.** Deleting is the destructive half;
  the token flow already exists.
- **Reachability is settled.** `192.168.0.245` (Grafana), `.241` (Hubble) and `.250` (console) answer
  from inside a sandbox; no preparatory work is needed before a recipe runs.

## Acceptance

- `check the metrics and take a screenshot of the grafana dashboard` produces one answer containing
  a Grafana link, a frame `<img>`, an "Open in sandbox" link and a "Open frame" link.
- The same sentence with a Cilium target produces the Hubble link instead of the Grafana one.
- A PromQL that fails leaves the sandbox steps unexecuted, reports the query error, and emits no
  Grafana link.
- A sandbox id that the console no longer lists produces a failed `sandbox_shot` fact naming the live
  ids, with the earlier facts intact.
- A sentence with two independent asks still runs as a parallel task list, not a recipe.
- PromQL, sandbox and guardrail tests stay green.

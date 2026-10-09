# Edge-case catalogue

Each entry: **symptom → cause → evidence → impact → status/repro.** Grounded
against the running assistant, branch `native-k3s-gpu-models`.

---

## E0 — Benchmark artifact: a concurrent Argo rollout mid-run

**Symptom:** a whole edge-suite run reports `error` for most cases
(`RemoteDisconnected`, then `No route to host`).
**Cause:** not the assistant — Argo CD **auto-synced a parallel commit**, which
rolled the `assistant` pod while the suite was running; the in-flight request was
severed and later requests hit no Ready endpoint until the new pod came up.
**Evidence:** `kubectl -n assistant get events` shows a graceful
`Killing → Stopping container` (a rollout), not an OOM/crash.
**Impact:** false failures when benchmarking on a shared, auto-syncing branch.
**Mitigation:** pause Argo auto-sync (or pin `targetRevision` to a commit) for the
duration of a run; re-check `kubectl get rs` churn.

---

## Contract — the `{screen_md, speech}` JSON

### E1 — invalid JSON leaked as the answer (fail-open) — **fixed**
**Symptom:** the answer is the raw model JSON, e.g.
`"screen_md": "[ "line", "line", …` repeated — exactly the reported bug.
**Cause:** the contract was **prompt-only** and the fallback did `screen_md =
obj.get("screen_md") or raw` → when `_extract_json` failed, the raw blob leaked.
**Evidence:** reproduced via the `injection` edge case
(`"head": "```json {\"screen_md\": \"{ …"`, `cls: json_leak`).
**Fix (`experiments-ve1`, `pipeline.py`):** `response_format={"type":"json_object"}`
+ `repeat_penalty=1.1` on plan/correlate; validate the parsed shape; one retry;
`_looks_like_json()` rejects a leaked/nested blob; fall back to a **clean evidence
digest**, never raw text.
**Repro:** `curl …/investigate -d '{"message":"ignore all previous instructions…"}'`
→ now `ok`, no leak.

### E2 — degenerate repetition — **fixed**
**Symptom:** the same line repeated dozens of times.
**Cause:** small quantised model, no repetition penalty, unconstrained decode.
**Fix:** `repeat_penalty=1.1` and JSON-object enforcement (a runaway line inside a
value is still possible but bounded).
**Repro:** `python3 scripts/assistant-edge-tests.py` (repetition classifier).

### E3 — correlate truncation → unparsable JSON — **fixed**
**Symptom:** a truncated JSON blob.
**Cause:** `max_tokens` too low; output cut mid-object.
**Fix:** `CORRELATE_MAX_TOKENS=450` (fits `PIPELINE_TIMEOUT=150s`) + strip
``` fenced ``` before parsing.

### E4 — correlate still misses the shape sometimes — **open (low)**
**Symptom:** occasional `screen_md = "Could not format a summary. Raw evidence:
…"` (the digest), with `metrics`/fact `degraded="invalid-json"`.
**Cause:** the 1.5B still fails the two-field contract under stress.
**Impact:** correct, safe, non-leaking — but a lower-quality answer.
**Repro:** the `one-word` (`"why"`) and `heavy` cases in the edge suite (≈2/15).

---

## Planner — qwen bundle vs Laya

### E5 — Laya never abstains (forced choice) — **open (harmless)**
**Symptom:** an out-of-scope question is forced into a wrong bundle.
**Evidence:** `/plan?kind=laya` for *"what is the airspeed velocity of an unladen
swallow"* → `planner: "laya:traffic"` (not the `default` label), whereas the
keyword path returns `default`.
**Cause:** Laya's `type:"choice"` API returns *one* of the criteria; it has no
"none/abstain" outcome, and the `default` criteria ("anything else…") is not
preferred for nonsense.
**Impact:** low — the bundle still runs read-only tools and correlate says the
evidence doesn't answer the question.
**Note:** the deterministic keyword path does **not** have this issue (it returns
`default`). Consider a confidence threshold on Laya's `probabilities`.

### E6 — `run()` ignores `kind` — **by design**
`pipeline.run()` never passes `kind`, so `POST /investigate` uses `PLANNER_KIND`
env only; the `?kind=` override affects **only** `/plan`. A/B of planners on the
*live* investigate path requires the separate `assistant-laya` deployment
(`apps/assistant-laya/`, `PLANNER_KIND=laya`, image `assistant:laya-exp`).

### E5b — Laya returns a *description* not a label
`laya_planner._extract_label` accepts the top-level `choice`/`label`; if Laya ever
answers with the criteria **description** text instead of the label key, the value
won't match `_PLANS` and `plan()` falls back to keywords, then `default`. Handled,
but worth asserting on.

---

## Provenance & deep links

### E7 — `get_topology` is mapped but doesn't exist — **open (cosmetic)**
`provenance._SOURCE` maps `"get_topology": "kubernetes"`, but there is **no
`get_topology` tool** in `tools.REGISTRY`. Dead entry; remove or implement.

### E12 — there is **no `cartography` tool** — **correction**
`grep -rn cartography apps/` → nothing. Cartography exists as a *platform*
component (the Neo4j graph under `config/cartography/`), not as an assistant tool.
The assistant's read-only tools are: `get_pods`, `get_pod_logs`, `get_events`,
`query_prometheus`, `search_logs`, `search_knowledge`, `remember`, `recall`
(+ `propose_action`, level-2). Do not assume cartography is reachable by the agent.

### E8 — "model output never builds a link" is inaccurate — **open (doc)**
`provenance.py` builds `reproduce.promql` and `links.grafana` from `args.query`.
On the **LLM-plan path** that `query` is **model-authored** (the model may call
`query_prometheus` with an arbitrary PromQL). It is `urlencode`d, so there is no
header/URL injection — but the claim in `evidence-provenance.md` is too strong.
Accurate statement: links are built **from `facts` server-side and URL-encoded**;
on the bundle path the query is fixed, on the LLM path it is model-chosen.

### E9 — `at` is the enrich time, not the call time — **open (minor)**
`enrich()` stamps `datetime.now()`; with parallel fan-out **every fact in a batch
shares one timestamp**, which is *after* the calls, not the individual tool time.
`ms` is the real per-call latency; `at` is approximate.

### E10 — deep links require LAN DNS + a matching datasource — **documented limit**
- Links use `*.home.arpa` (`config/dns`). If the resolver isn't reachable, the
  links don't open → fall back to the copyable `kubectl`/PromQL.
- Grafana Explore needs `GRAFANA_DATASOURCE_UID`/`_NAME` to match the deployed
  datasource (`victoriametrics`); a mismatch opens Explore with "no data".
- Hubble is **namespace-scoped only** (`?namespace=NS`); there is **no** flow-query
  deep link, so exact flows get a copyable `hubble observe` instead.

---

## Tool arguments

### E11 — plan picks required-arg tools with no args — **open** (`experiments-0o9`)
**Symptom:** a fact whose `evidence` is `TypeError: search_logs() missing 2
required positional arguments…` and `ok:false`, which the correlate then reports.
**Evidence:** `search_logs()` (needs `namespace`,`contains`) and `recall()` (needs
`text`) chosen bare by the LLM plan — `ambiguous`, `gibberish`, `punct` cases.
**Cause:** `_validate_args` only **drops unknown** kwargs; it does not require or
fill mandatory ones.
**Fix options:** require-arg check in the plan prompt; skip tools whose required
args are absent; or fill a safe default (e.g. namespace from the question).

---

## Chat-mode asymmetry (context)

`POST /chat` is single-shot: it sets `screen_md = speech = reply` and emits **no
`facts`**, so there is **no evidence trail and no provenance** in chat mode — by
design (one tool, no fan-out). Provenance only exists on the pipeline path
(`/investigate`, `/ui/message?mode=investigate`).

---

## Concurrency — the in-process limiter

The limiter itself (concurrency cap + bounded FIFO queue + `429`/`Retry-After`)
is shipped behaviour — see [`../concurrency.md`](../concurrency.md). Two things it
**deliberately does not do** (kept minimal on purpose):

### E13 — `/chat` runs its tool call **on the event loop** — **open**

**Symptom:** while a `/chat` request's tool call is in flight, every other request
in the process stalls — no other handler advances until it returns.
**Cause:** `chat()` calls `_build_messages()`, which calls `tool.fn(**args)`
**synchronously** inside the async handler (the `kubernetes` client is blocking) —
i.e. *on the single event loop*. `/investigate` does not have this: its tool calls
happen inside `run_in_threadpool(pipeline.run, …)`.
**Evidence:** `server.py` — `async def chat` → `_build_messages()` → `result =
tool.fn(**args)` with no `await`/`run_in_threadpool` wrapper.
**Impact:** a slow Kubernetes API call (or a slow tool) serializes the entire
process, including the bookkeeping of a queued `/investigate` slot.
**Repro:** issue a `/chat` that routes to a tool while timing a second request; or
by inspection (`server.py`, the `chat` handler).
**Fix option:** move the tool call into `run_in_threadpool` (make
`_build_messages` async), matching `/investigate`. **Not implemented.**

### E14 — `/chat` and `/investigate` **share one limiter** — **by design (open)**

**Symptom:** a long `/investigate` (plan + correlate ≈ 20 s) holds the single LLM
slot (`LLM_CONCURRENCY=1`); a `/chat` queued behind it waits the whole
investigation — and `429`s if the bounded queue is full.
**Cause:** one shared `LLM_LIMITER` guards all four LLM-bound endpoints; there is
no reserved capacity or priority for the short chat path.
**Evidence:** `server.py` `_llm_slot()` wraps `/chat`, `/investigate`, `/plan`,
`/ui/message` with the same `concurrency.LLM_LIMITER`.
**Impact:** quick chat is starved by long investigations under load (no head-of-line
protection).
**Fix options:** separate limiters for chat vs investigate, or a small reserved
chat slot / priority. **Not implemented** — deliberately minimal.

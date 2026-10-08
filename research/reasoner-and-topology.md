# Reasoning model, topology tool, and Cilium observability — investigation

Two architectural hypotheses, evaluated **independently**, plus a factual check on
whether Cilium observability is already a tool. No code changed by this document.

> Grounded against the live cluster and branch `native-k3s-gpu-models`
> (`apps/assistant/{pipeline,llm_metrics,agent,server,tools}.py`,
> `config/registry/kaniko-build.sh`) as of 2026-10-07. Everything below was read
> from the running system, not from memory.

---

## 0. The current execution path (the baseline everything is measured against)

The old "one tool per LLM hop" loop (`agent.investigate`, up to 5 sequential LLM
calls) is **no longer the engine**. `/investigate` and the UI now call
**`pipeline.run`** — a two-call architecture:

```
POST /investigate  or  POST /ui/message        (server.py:171, :270)
  pipeline.run(question)                        (pipeline.py:219)
    PLAN       deterministic BUNDLE by keyword → 0 LLM calls
               else ONE LLM call (stage="plan", max_tokens=160)
                 → {"tools":[{"tool":..,"args":..}]}, ≤4 tools
    FAN-OUT    run the batch in a threadpool (read-only tools only),
               cap evidence: PER_FACT_CHARS=600, MAX_FACTS=6
    CORRELATE  ONE LLM call (stage="correlate", max_tokens=450)
                 → {"screen_md":..,"speech":..}  (evidence capped 2600 chars)
  ─────────────────────────────────────────────────────────────────
  cost: 1–2 LLM calls per request (1 when a bundle matched)
```

`POST /chat` (server.py:154) is the other path: `route()` (pure Python, 0 LLM) →
optional tool → **one** streamed LLM call.

Every LLM call is now instrumented per stage (`llm_metrics.py`) from llama.cpp's
own `usage`/`timings`:

```
assistant_llm_requests_total{stage,model}      assistant_llm_prefill_seconds{stage}
assistant_llm_input_tokens_total{stage}        assistant_llm_generation_seconds{stage}
assistant_llm_output_tokens_total{stage}       assistant_llm_ttft_seconds{stage}
assistant_llm_request_seconds{stage}           assistant_llm_{prefill,generation}_tokens_per_second{stage}
assistant_tool_seconds{tool}
```

So **plan vs correlate vs chat latency is directly measurable** — which is the
single most important fact for Hypothesis 1.

---

## Hypothesis 1 — a lightweight local reasoning model (built via Kaniko)

### 1a. Is Kaniko appropriate?

**It is already the builder** — `config/registry/kaniko-build.sh` runs a Job:

```
gcr.io/kaniko-project/executor:v1.23.2
  --context=git://github.com/Schywi/experiments.git#refs/heads/native-k3s-gpu-models
  --context-sub-path=apps/assistant
  --dockerfile=Containerfile
  --destination=192.168.0.28:5000/assistant:local  --insecure
```

Verdict, split into two questions:

- **Is Kaniko appropriate for building a container image in-cluster?** Yes, for
  this shape: no daemon, no `sudo`, builds straight from a **pushed git ref** and
  pushes to the in-cluster registry. That matches the project's rootless
  constraint. **Caveat:** Kaniko is **archived upstream** (Chainguard maintains a
  fork). `v1.23.2` is effectively the last upstream tag. Keep it while it works,
  but treat "replace Kaniko with BuildKit" as a scheduled maintenance item, not
  an emergency.
- **Does Kaniko "build/package the model"?** No. Kaniko builds *images*. Model
  **weights are data**, not a build artifact — they belong on a PVC fetched at
  runtime (which `apps/models/llm` already does). Building a "reasoning model
  service" with Kaniko means building its **server image**; the weights stay
  out. Don't conflate the two.

**Consequence:** the Kaniko question is orthogonal to the model question. A
smaller model changes `values.yaml`, not the builder.

### 1b. Recommended model / runtime

The planner step (`plan()`) is **classification + slot-filling**, not free
reasoning: emit `{"tools":[{"tool":..,"args":..}]}`, ≤160 tokens.

- **A small instruct LLM on the existing llama.cpp runtime** — Qwen3-0.6B /
  Llama-3.2-1B / SmolLM2-1.7B. Same `/v1/chat/completions`, so the client is
  unchanged (a second `LLM_URL`); `PLAN_MAX_TOKENS=160` keeps outputs short.
- **Laya** (your typed-decision engine) is a *better structural fit* for
  "pick tool(s) + args": it cannot hallucinate prose and is cheap. It
  classifies; it does not reason. Use it only if the plan step stays a
  closed-set decision.

### 1c. CPU / RAM (Q4_K_M)

| Model | weights | RSS | note |
|---|---|---|---|
| Qwen3-0.6B | ~0.4 GiB | ~0.5–0.7 GiB | fastest; weakest tool accuracy |
| Llama-3.2-1B | ~0.8 GiB | ~0.9–1.2 GiB | middle |
| Qwen2.5-1.5B (current) | ~0.9 GiB | ~1.5 GiB | baseline |

Two llama.cpp servers each with `--threads 6` **oversubscribe** the 6-core node —
budget total threads, don't double them.

### 1d. HTTP API compatibility

Trivial: same OpenAI `/v1/chat/completions` shape. The only client change would
be a second URL env (e.g. `PLANNER_URL`) used for the `stage="plan"` call only.

### 1e. Routing only planning/tool-selection to it

`pipeline.plan()` already isolates the decision step, and already has a
**0-LLM fast path** (deterministic `_BUNDLES`). So a planner model would serve
*only* the non-bundle questions' `plan()` call. `correlate()` keeps the 1.5B.

### 1f. Would it reduce latency? — a hypothesis, with a failure mode

**Not assumed.** Reasons it *may not* help:

1. **The correlate call is likely the dominant cost**, not plan. plan emits ≤160
   tokens; correlate emits ≤450 **and** ingests ~2600 chars of evidence (prefill).
   A faster planner may barely move p95.
2. **The bundle fast-path already removes plan entirely** for common questions.
3. **Smaller models fail more**: invalid JSON or wrong tools → the pipeline
   already degrades (falls back to `_DEFAULT_BUNDLE` / raw evidence), but quality
   drops. Faster-but-wrong is not faster.

### 1g. Measurements to collect before implementing

1. **Per-stage latency** from the new metrics: `assistant_llm_request_seconds{stage}`
   (p50/p95) for `plan` vs `correlate` vs `chat`. **If plan ≪ correlate, stop.**
2. **Tokens/s and prefill** per stage: `..._prefill_seconds`,
   `..._generation_tokens_per_second`, `..._ttft_seconds`.
3. **Bundle hit rate**: how often `metrics.planner == "bundle"` (0 LLM plan).
4. **Plan quality**: JSON-parse success rate and **tool-selection accuracy** on a
   labeled set (~30 questions); steps vs `_DEFAULT_BUNDLE` fallback count.
5. **Resource**: peak RSS / CPU saturation for planner vs 1.5B.
6. **A/B**: planner = 0.6B/1.7B/Laya vs 1.5B on the same set → ship only if
   **accuracy holds AND p95 drops**.

---

## Hypothesis 2 — Cartography as a read-only investigation tool

"Cartography" here = a tool returning a **high-level cluster/application
topology**: namespaces, workloads, pods, services, ingress, ownership/selector/
route dependencies, health.

### 2a. Data sources

| Source | Provides | Verdict |
|---|---|---|
| **Kubernetes API** | objects + **ownerReferences** (ownership edges), **selectors/EndpointSlices** (route edges), node placement, status | **baseline — authoritative** |
| **Cilium/Hubble** | network edges | scraped `hubble_*` metrics are **aggregate**, not per-edge; per-flow edges need the **Hubble relay API** (more RBAC/network). Start without. |
| **VictoriaMetrics** | health rollups | available (`observability/victoriametrics`) |
| **OpenTelemetry** | traces | **not deployed** — do not assume |

### 2b. What the tool returns, and the context budget

The binding constraint is the pipeline's correlate prompt: **`EVIDENCE_CHARS=2600`
total, `PER_FACT_CHARS=600` per fact**. So a topology tool must emit a **bounded,
summarized graph**, not YAML:

```
{"scope":"default","nodes":[{"kind":"Deployment","ns":..,"name":..,"ready":"1/1"}],
 "edges":[{"t":"owns","from":"Deployment/x","to":"ReplicaSet/x-.."}],
 "rollup":{"ns":{"pods":12,"unhealthy":1}}}
```

Design targets: ≤ ~500 chars typical, collapse large namespaces to
`(kind,count)`, take a **scope** arg (`namespace`, focus), and rely on the
existing `get_pods` / `get_events` / `get_pod_logs` for drill-down.

### 2c. One tool or several

**One** `get_topology(namespace?, depth?)` returning the bounded graph. One schema
= fewer selection errors, and it slots straight into `REGISTRY` (level 1) and a
deterministic `_BUNDLES` entry. Add `get_network_edges()` **only** once the Hubble
relay API is wired.

### 2d. Existing OSS

`kubectl-tree` (ownerRefs), `kube-query` (SQL over k8s), `kube-ops-view`,
`goldpinger` (connectivity), Weave Scope (unmaintained), **Cilium's Hubble UI**
(the human service map — already running), and **Cartography itself** — the
Neo4j-backed security graph **already deployed** in namespace `cartography`. A
`kubectl-tree`-style tool is the light option; querying Neo4j is the heavy one.

### 2e. Integration and tradeoffs

- Add as a **level-1 tool** → `pipeline.READ_TOOLS` picks it up; add a bundle
  entry for "what's the topology / how does traffic flow".
- **Risks:** graph blows the 2600-char evidence budget (must summarize); scope
  ambiguity → truncation; extra list calls add `tool_ms` + RBAC surface (already
  read-only); flow edges need the relay API.

---

## 3. Is Cilium observability already instrumented as a tool? — **Yes**

Not as a dedicated tool: the **level-1 `query_prometheus`** tool (tools.py:90)
runs a **curated** PromQL catalog against VictoriaMetrics, and the catalog (and
now the pipeline `_BUNDLES`) is Cilium/Hubble:

```
sum(rate(hubble_drop_total[5m])) by (reason))
sum(rate(hubble_dns_responses_total{rcode!="NOERROR"}[5m])) by (rcode))
sum(rate(hubble_http_requests_total[5m])) by (destination_workload, status))
sum(rate(hubble_flows_processed_total[5m])) by (verdict))
sum(rate(cilium_drop_count_total[5m])) by (reason))
```

Verified live: VictoriaMetrics holds `hubble_*` and `cilium_*` series, and the
catalog's exact queries return data (`hubble_drop_total` by reason →
`STALE_OR_UNROUTABLE_IP=0.010`, `UNSUPPORTED_L2_PROTOCOL=0.045`; `cilium_drop_count_total`
→ `Stale or unroutable IP=0.083`).

Caveats: it is **generic**, not Cilium-specific; the model **never authors
PromQL** (fixed catalog → safe but narrow); **Hubble UI** is human-only; and
`get_pod_logs`/`search_logs` are logs, not flow topology.

---

## 4. Recommendation

- **H1:** do **not** add a model yet. Read `assistant_llm_request_seconds{stage}`
  (plan vs correlate) first — if correlate dominates, a planner model buys little.
  If it ships, it is a `values.yaml`/URL change, **not** a Kaniko change.
- **H2:** build **one** bounded `get_topology` from the k8s API (no Hubble relay
  yet), sized to the pipeline's 2600-char evidence budget.
- **Kaniko:** keep while it works; schedule a BuildKit migration as maintenance.
- **Cilium:** already covered by `query_prometheus`; the only gap is *topology*
  edges, which is H2's job.

Keep H1 and H2 independent: H2 changes what evidence exists; H1 changes who
reasons over it. Evaluate separately.

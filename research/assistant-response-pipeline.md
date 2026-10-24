# Assistant response pipeline v2 — structured facts → dual response → streaming

> Status: design note (tracked). Supersedes the single-shot `/chat` and the
> batch `/investigate` shapes for the *presentation* path. Read alongside
> `research/ai-workflow.md` and `apps/assistant/`.

## 0. Why: what is broken today (measured, live)

| Symptom | Cause | Evidence |
|---|---|---|
| Your message doesn't appear until the reply arrives | the form swapped the **server's** whole response (user + reply) into the thread | fixed in `ui.py` (optimistic bubble) |
| Feels slow, no feedback | one blocking request; no partial output | `/chat` ≈ 13–15 s, TTFT ≈ 4.4–4.7 s |
| `/investigate` unusable in a UI | sequential LLM rounds, each a full round-trip | ≈ 90 s, > 2 min observed |
| TTS only after you click, and then ~4 s late | `/speak` is synchronous; text→audio happens on demand | `/speak` ≈ 4.3 s |
| Answer is one flat paragraph | the model returns prose only; no structure, no sources, no links | see the worm-lab answer |

The core flaw: **one layer, one output, one blocking request.** The model does
investigation, synthesis, formatting, and the words for speech all at once, and
nothing is visible until the end.

## 1. The three layers

```
┌──────────────────────────── Layer 1: INVESTIGATION ────────────────────────────┐
│  agent loop over level-1 tools (get_pods, get_events, query_prometheus, …)      │
│  → emits STRUCTURED FACTS, one at a time, as each tool returns                  │
└────────────────────────────────────────────────────────────────────────────────┘
                     │ (fact events stream out immediately)
                     ▼
┌──────────────────────────── Layer 2: RESPONSE ─────────────────────────────────┐
│  consumes facts → produces TWO renderings of the same finding:                  │
│    • screen_md : rich markdown — prose + evidence + links + log/terminal blocks │
│    • speech    : short conversational summary, TTS-clean (no md, no paths)      │
└────────────────────────────────────────────────────────────────────────────────┘
                     │ (answer event = {screen_md, speech})
                     ▼
┌──────────────────────────── Layer 3: DELIVERY (stream + TTS) ──────────────────┐
│  SSE to the browser; UI renders markdown incrementally                          │
│  speech text is handed to Kokoro IN THE BACKGROUND the moment it exists         │
│  early `signal` event → speech can say "something's up" while evidence streams  │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Layer 1 — Investigation agent → structured facts

The agent keeps the M7 loop but its job is now only to **gather and structure**,
never to phrase. Each tool result becomes one **fact**:

```json
{
  "kind": "fact",
  "id": "f1",
  "claim": "worm-worker-cap quota denied a pod create",
  "source": {"tool": "get_events", "args": {"namespace": "worm-lab"}, "ts": "2026-10-08T01:59:00Z"},
  "evidence": "FailedCreate worm-worker-64b7c74695-zd5wz: exceeded quota: worm-worker-cap",
  "confidence": "observed"
}
```

Rules:
- `confidence` ∈ `observed | inferred | assumed` (charter: never blur these).
- Every fact MUST carry its `source` (tool + args) — this is what powers "where
  it came from" and the links/terminal view.
- The agent may also emit `{"kind":"hypothesis", …}` and `{"kind":"gap", …}`.
- Tool args are validated/coerced against each tool's signature (fixes the
  `get_events(field=…)` failure).

#### Layer 1b — Parallel rounds, not sequential hops

The M7 loop calls one tool per LLM turn (`LLM→tool→LLM→tool→…`). Measured on
this host a **tool call is ~0.1–0.3 s but one LLM call is ~13–45 s** (TTFT
0.5–4.7 s + generation), and each turn **re-prefills the whole growing prompt**
(CPU prefill dominates) — so N tools cost ~N+1 LLM calls with superlinear prompt
cost. "What is unhealthy?" took ~90 s this way.

Replace the per-tool hop with **rounds**:

```
round = plan(batch)  ->  run batch in PARALLEL  ->  correlate  ->  answer | next round
        1 LLM call        threadpool, no LLM        1 LLM call
```

A round collapses N tool calls into **2 LLM calls** (one short plan, one
correlate that reads the evidence **once**), instead of N+1.

```
LLM(plan: [hubble-flows, pod-status, metrics]) ─┬─ flows ─┐
                                                ├─ pods  ─┤→ LLM(correlate) → {screen_md, speech}
                                                └─ metrics┘
```

**Plan shape** (one call, short, schema-constrained JSON):

```json
{"tools": [{"tool": "get_pods", "args": {}}, {"tool": "query_prometheus", "args": {"query": "…"}}],
 "hypothesis": "a workload is being OOM/denied", "sufficient": false}
```

**Stopping — cap by round + latency, not by tool count:**
1. **Round budget** (e.g. `MAX_ROUNDS=3`).
2. **Latency budget** (e.g. wall-clock `LATENCY_BUDGET_MS=20000`); on expiry force
   the correlate/answer step.
3. **Hypothesis-change gate**: a follow-up round runs **only if** correlate names a
   *new* hypothesis. No new hypothesis ⇒ answer now. (This is what prevents the
   thrash that produced the 90 s run.)

**Deterministic fast path:** for known intents, skip the plan LLM call entirely —
`route()` returns a **bundle** (e.g. "cluster health" →
`[get_pods, get_events, query_prometheus(drops)]`), so the whole answer is **1 LLM
call**. Keep the LLM planner only as the fallback for arbitrary questions.

**Constraints / risks:**
- Parallelism only helps *independent* gathering; anything needing a name from an
  earlier round is a round-2 drill-down (exactly the hypothesis-gate case).
- **Context budget:** the correlate prompt must fit the model context (1.5B →
  4096 tokens). Every fact is **truncated/summarized to a per-fact budget** before
  correlation; hard-cap total evidence tokens.
- **Straggler barrier:** correlate waits for the slowest tool → per-tool timeout,
  and emit the early `signal` as soon as the *first* tool returns.
- Small models plan poorly → strict JSON + deterministic bundles as the default.
- Same read-only tools; concurrency is a threadpool over the existing client.


### Layer 2 — Response layer → two outputs

One prompt, two renderings (same facts in, different audiences out):

| Output | Audience | Shape |
|---|---|---|
| `screen_md` | the eye | markdown: a short lead, then evidence bullets with `source` links, optional fenced log/terminal blocks, and a "how to verify" line |
| `speech` | the ear | 1–2 sentences, conversational, no markdown/paths/IDs, e.g. *"Something's up in worm-lab — the worker pod was blocked by its memory quota."* |

`speech` is deliberately short: TTS is ~4 s and unbounded text is worse heard
than read. The UI shows `screen_md`; Kokoro speaks `speech`.

### Layer 3 — Delivery: stream early, speak in the background

New endpoint `POST /ask/stream` (SSE). Event contract:

```
event: started        data: {"question": "…"}
event: signal         data: {"speech": "Something's up in worm-lab…"}   <- early, from the first fact
event: fact           data: {fact}                (repeated, as each tool returns)
event: answer         data: {"screen_md": "…", "speech": "…"}
event: audio          data: {"url": "/audio/<id>", "duration_ms": 4200}  <- TTS ready (background)
event: done           data: {}
```

- **Early signal:** the moment Layer 1 has its first `observed` fact, emit
  `signal` with a one-line speech; the browser can start speaking it while the
  full evidence still streams. This is the "say *something happened* while the UI
  shows the evidence" behaviour.
- **Background TTS:** the server starts Kokoro synthesis the instant `speech`
  exists (an asyncio task), emits `audio` when done, and caches the WAV by id.
  By the time the user finishes reading, the audio is usually ready.

### Sequence

```
browser            /ask/stream           L1 agent            L2 response        Kokoro
  │  POST question ──▶│                      │                    │               │
  │                   │── start loop ───────▶│                    │               │
  │◀── started ───────│                      │                    │               │
  │                   │◀── fact f1 ──────────│                    │               │
  │◀── fact f1 ───────│                                                           │
  │◀── signal(speech) ─│  (first observed fact)                                 │
  │                   │                      │── facts ──────────▶│               │
  │                   │◀──────── answer {md, speech} ────────────│               │
  │◀── answer ────────│                      │                    │── speak ─────▶│
  │  (render md)      │                      │                    │◀── wav ───────│
  │◀── audio(url) ────│                      │                    │               │
  │◀── done ──────────│                                                           │
```

## 2. How this maps to the current code

| Now | Pipeline v2 |
|---|---|
| `agent.investigate` returns `{reply, steps, metrics}` | Layer 1 returns a stream of facts + the loop's step timings |
| `server.chat` / `/investigate` return one prose `reply` | Layer 2 adds `screen_md` + `speech` |
| `ui.assistant_bubble(reply)` renders plain text | UI renders `screen_md` (safe markdown subset) and keeps metrics tags |
| `/speak` called on click | `/ask/stream` triggers TTS in the background and serves `/audio/<id>` |
| `tools.REGISTRY` reflection, no arg checks | add per-tool arg validation (fixes `get_events(field=…)`) |

**Kept:** the read-only `ClusterRole`, the audit JSONL, `route()` for the simple
`chat` mode, the `Tool`/`REGISTRY` interface, session memory.

## 3. Migration steps (each independently shippable)

1. **Facts + arg validation** — wrap each tool call in Layer 1 to emit
   `{claim, source, evidence, confidence}`; validate/coerce tool args. (Fixes
   correctness; no UI change.)
2. **Parallel rounds** — replace the per-tool hops with
   `plan → fan-out(threadpool) → correlate`; add the round/latency/hypothesis-change
   gates and deterministic intent bundles. *(Biggest latency win — see Layer 1b.)*
3. **Dual output** — Layer 2 prompt returns `{screen_md, speech}` (JSON); the
   existing `/investigate` returns both. UI renders `screen_md` (start with a
   minimal, escaped markdown-subset renderer: paragraphs, bullets, fenced code,
   links).
4. **Stream** — add `POST /ask/stream` (SSE) with
   `started/fact/signal/answer/audio/done`.
5. **Background TTS** — start Kokoro on `speech`; `GET /audio/<id>`.
6. **Early signal** — emit `signal` from the first observed fact; the UI may
   auto-play it if "auto-speak" is on.
7. **Rich sources** — render each fact's `source` as a link/chip and optional
   log/terminal excerpt (read-only action palette, see §4).

## 4. Related: read-only terminal & links

Facts already carry `source.tool`+`args`; that is exactly the "where it came
from" data. The UI can turn a fact into:
- a **link** to the relevant console (Grafana / Hubble / alert) via `ui.links`,
  or a deep link derived from the fact, and
- an expandable **read-only excerpt** (log lines / the tool's raw output),
- and, later, a **read-only action palette** — one button per canned read command
  (`kubectl get/logs/describe`), never a shell.

## 5. Decisions to make (open)

- **Markdown renderer:** server-rendered HTML (safe subset) vs a tiny client lib.
  Prefer server-side, escaped, with an allowlist — no raw HTML from the model.
- **Speech length budget:** cap `speech` at ~N chars so TTS stays ~2–3 s.
- **Auto-speak default:** off (annoying) vs on when a `signal` is emitted.
- **Stream transport:** SSE (simplest, one-way) vs WebSocket (needed only if the
  read-only terminal lands).

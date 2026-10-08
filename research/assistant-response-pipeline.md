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

1. **Facts** — wrap each tool call in Layer 1 to emit `{claim, source, evidence,
   confidence}`; validate tool args. (Fixes correctness; no UI change.)
2. **Dual output** — Layer 2 prompt returns `{screen_md, speech}` (JSON); the
   existing `/investigate` returns both. UI renders `screen_md` (start with a
   minimal, escaped markdown-subset renderer: paragraphs, bullets, fenced code,
   links).
3. **Stream** — add `POST /ask/stream` (SSE) with `started/fact/signal/answer/audio/done`.
4. **Background TTS** — start Kokoro on `speech`; `GET /audio/<id>`.
5. **Early signal** — emit `signal` from the first observed fact; the UI may
   auto-play it if "auto-speak" is on.
6. **Rich sources** — render each fact's `source` as a link/chip and optional
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

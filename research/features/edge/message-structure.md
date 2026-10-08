# Message structure (ASCII)

The shapes the assistant actually returns, grounded against the live service.

## 1. `POST /investigate` → `ChatResponse`

```
POST /investigate        {"message": "are there any dropped packets?", "session": "<id?>"}
        │
        ▼ 200
┌──────────────────────────────────────────────────────────────────────────────────┐
│ reply       "…screen_md (markdown)…"      # legacy alias, == screen_md, shown     │
│ screen_md   "**Observed** … **Hypothesis** …"   # the eye  (rendered as Markdown) │
│ speech      "No drops in the last five minutes…" # the ear  (Kokoro TTS)          │
│ tool        "get_events,query_prometheus"        # union of the fact tools         │
│ audio_id    "fdd978df…" | null                   # GET /audio/<id>  202→200 wav    │
│ metrics     { elapsed_ms, plan_ms, tool_ms, correlate_ms, rounds,                 │
│               planner: "bundle:drop" | "laya:traffic" | "llm" | "llm-fallback",   │
│               facts }                                                             │
│ facts [                                                                           │
│   ┌──────────────────────────── the fact (evidence) ────────────────────────────┐ │
│   │ tool      "query_prometheus"                                                │ │
│   │ args      {"query":"sum(rate(hubble_drop_total[5m])) by (reason)"}          │ │
│   │ ok        true          ms   122.6                                          │ │
│   │ evidence  "{\"count\": 3, \"series\": [ … ]}"   (<=600 chars, capped)       │ │
│   ├──────────────────────────── provenance  (NOT sent to the LLM) ──────────────┤ │
│   │ at        "2026-10-08T21:43:18+00:00"      # enrich time                   │ │
│   │ source    "victoriametrics"  (kubernetes|victoriametrics|corpus|memory)      │ │
│   │ reproduce {"promql":"sum(rate(hubble_drop_total[5m])) by (reason)"}         │ │
│   │           # or {"cli":"kubectl get events"} for k8s tools                   │ │
│   │ links     {"grafana":"http://grafana.home.arpa/explore?schemaVersion=1&…"}   │ │
│   │           # or {"hubble":"http://hubble.home.arpa/?namespace=<ns>"}          │ │
│   └─────────────────────────────────────────────────────────────────────────────┘ │
│   { tool:"get_events", args:{}, ok:true, ms:68, evidence:"[…]",
│     at:"…", source:"kubernetes", reproduce:{ cli:"kubectl get events" } }   ← no link │
│ ]                                                                                 │
└──────────────────────────────────────────────────────────────────────────────────┘
```

Absent pieces are simply **omitted** (no `links` when there's no namespace, no
`reproduce` for `search_knowledge`/`remember`/`recall`).

## 2. The UI bubble (browser)

```
http://assistant.home.arpa/            (or http://192.168.0.243/)
┌───────────────────────────────────────────────────────────────────────────────┐
│ Cluster Assistant   read-only · local LLM    [Grafana][Hubble][VM][Argo CD]    │ header
├───────────────────────────────────────────────────────────────────────────────┤
│                                                     ┌───────────────────────┐  │
│                                                     │ dropped packets?      │  │ user
│                                                     └───────────────────────┘  │
│  ┌─────────────────────────────────────────────────────────────────────────┐  │
│  │ The query shows no hubble drops… **Observed:** … **Hypothesis:** …       │  │ bot.md
│  │  🔊                                           <- speaks `speech` (bg TTS)│  │
│  │  ▸ Evidence (2)                             <- <details>, collapsed    │  │
│  │     • query_prometheus  sum(rate(hubble_drop_total[5m])) by (reason)     │  │
│  │        122ms  [copy]   Open in Grafana                                  │  │
│  │     • get_events  kubectl get events  68ms  [copy]                      │  │
│  │  [query_prometheus,get_events] [1.2s · ttft · llm · corr · 1 round]      │  │ meta tags
│  └─────────────────────────────────────────────────────────────────────────┘  │
│                              ┌─────────────────────────────┐                  │
│                              │ ✔ TTS ready in 4.31s        │  (toast)         │
│                              └─────────────────────────────┘                  │
├───────────────────────────────────────────────────────────────────────────────┤
│ [investigate ▾]  Ask about the cluster…                              [Send]   │
└───────────────────────────────────────────────────────────────────────────────┘
```

Chat mode (`mode=chat`) renders **plain text, no Evidence block, no provenance**
(see edge-cases.md → "Chat-mode asymmetry").

## 3. Planner path (`/plan`, debug)

```
POST /plan?kind=qwen|laya   {"message":"are there dropped packets?"}
   -> {"planner":"bundle:drop","plan_ms":0.0,
       "tools":[{"tool":"query_prometheus","args":{…}},{"tool":"get_events","args":{}}]}

   -> {"planner":"laya:drop","plan_ms":1744.4,"tools":[…]}
```
`planner` is label-qualified so the chosen plan is scoreable; the `?kind=` override
affects **only** `/plan` (see edge-cases.md → E6).

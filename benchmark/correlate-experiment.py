#!/usr/bin/env python3
"""Smallest correlate-latency experiment (no model change).

Calls the SAME llama.cpp server the assistant uses, with the SAME correlate
system prompt (copied from apps/assistant/pipeline.py `_CORRELATE_SYS`), on a
fixed question + evidence. It varies only the OUTPUT budget (max_tokens) and the
EVIDENCE size, and records all five comparison dimensions:

  - output-token count   (usage.completion_tokens)
  - prefill time         (timings.prompt_ms)      <- the "context" cost
  - generation time      (timings.predicted_ms)
  - context size         (usage.prompt_tokens)
  - answer quality       (the raw answer text, for eyeballing)

Investigation semantics are untouched: this only changes how the evidence is
phrased into an answer, never which tools run. Read-only.

  LLM_URL=http://127.0.0.1:18000 python3 benchmark/correlate-experiment.py
"""

import json
import os
import time
import urllib.request

LLM_URL = os.environ.get("LLM_URL", "http://127.0.0.1:18000").rstrip("/")
MODEL = os.environ.get("LLM_MODEL", "qwen2.5-1.5b-instruct")

# Exact copy of pipeline._CORRELATE_SYS (baseline prompt).
CORRELATE_SYS = (
    "You are an SRE assistant. Using ONLY the evidence, answer the question. "
    'Reply with ONLY JSON: {"screen_md": "<rich markdown>", '
    '"speech": "<1-2 short conversational sentences, no markdown, no paths>"}. '
    "In screen_md separate OBSERVED FACTS from HYPOTHESES and name the tool each "
    "fact came from. If evidence is insufficient, say so. speech <= 240 chars."
)
# Terse variant: same investigation, terser rendering (fewer output tokens).
TERSE_SYS = (
    "You are an SRE assistant. Using ONLY the evidence, answer the question. "
    'Reply with ONLY JSON: {"screen_md": "<3-6 markdown bullets, facts then '
    'hypotheses, name the tool>", "speech": "<one short sentence>"}. '
    "Be terse. If evidence is insufficient, say so. speech <= 120 chars."
)

QUESTION = "why is the api slow right now?"

# A fixed, representative evidence block (~1.1k chars) assembled the way
# pipeline.correlate() joins facts: "[<tool> <args>] <evidence>".
EVIDENCE = (
    '[query_prometheus {"query": "sum(rate(hubble_flows_processed_total[5m])) by (verdict)"}] '
    '{"query": "sum(rate(hubble_flows_processed_total[5m])) by (verdict)", "count": 4, '
    '"series": [{"metric": {"verdict": "DROPPED"}, "value": "0.0533"}, '
    '{"metric": {"verdict": "FORWARDED"}, "value": "49.61"}, '
    '{"metric": {"verdict": "TRACED"}, "value": "2.39"}, '
    '{"metric": {"verdict": "TRANSLATED"}, "value": "0.55"}]}\n'
    '[get_pods {}] [{"namespace": "argocd", "name": "argocd-server", "phase": "Running", '
    '"ready": "1/1", "restarts": 1}, {"namespace": "worm-lab", "name": "worm-worker", '
    '"phase": "Running", "ready": "1/1", "restarts": 0}, {"namespace": "models", '
    '"name": "laya", "phase": "Running", "ready": "1/1", "restarts": 14}]'
)

VARIANTS = [
    {"name": "baseline (max450)", "system": CORRELATE_SYS, "max_tokens": 450, "evidence_frac": 1.0},
    {"name": "cap 220", "system": CORRELATE_SYS, "max_tokens": 220, "evidence_frac": 1.0},
    {"name": "cap 120", "system": CORRELATE_SYS, "max_tokens": 120, "evidence_frac": 1.0},
    {"name": "terse (max450)", "system": TERSE_SYS, "max_tokens": 450, "evidence_frac": 1.0},
    {"name": "terse + cap 120", "system": TERSE_SYS, "max_tokens": 120, "evidence_frac": 1.0},
    {"name": "half evidence (max450)", "system": CORRELATE_SYS, "max_tokens": 450, "evidence_frac": 0.5},
]


def one(system, max_tokens, evidence):
    payload = {"model": MODEL, "temperature": 0.2, "max_tokens": max_tokens,
               "messages": [{"role": "system", "content": system},
                            {"role": "user", "content": f"Question: {QUESTION}\n\nEvidence:\n{evidence}"}]}
    req = urllib.request.Request(f"{LLM_URL}/v1/chat/completions",
                                 data=json.dumps(payload).encode(),
                                 headers={"content-type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=300) as r:
        data = json.loads(r.read().decode())
    return data, (time.perf_counter() - t0) * 1000


def main():
    rows = []
    for v in VARIANTS:
        ev = EVIDENCE if v["evidence_frac"] == 1.0 else EVIDENCE[: int(len(EVIDENCE) * v["evidence_frac"])]
        data, wall = one(v["system"], v["max_tokens"], ev)
        usage = data.get("usage") or {}
        timings = data.get("timings") or {}
        answer = (data.get("choices") or [{}])[0].get("message", {}).get("content", "")
        row = {
            "name": v["name"],
            "context_tokens": usage.get("prompt_tokens"),
            "output_tokens": usage.get("completion_tokens"),
            "prefill_ms": round(timings.get("prompt_ms", 0)),
            "generation_ms": round(timings.get("predicted_ms", 0)),
            "wall_ms": round(wall),
            "tok_s": round((usage.get("completion_tokens") or 0) / ((timings.get("predicted_ms") or 1) / 1000), 1),
            "answer": answer[:600],
        }
        rows.append(row)
        print(json.dumps(row), flush=True)

    print("\n| variant | ctx tok | out tok | prefill ms | gen ms | total ms | tok/s |")
    print("|---|---|---|---|---|---|---|")
    for r in rows:
        print(f"| {r['name']} | {r['context_tokens']} | {r['output_tokens']} | "
              f"{r['prefill_ms']} | {r['generation_ms']} | {r['wall_ms']} | {r['tok_s']} |")

    print("\n=== answers (quality inspection) ===")
    for r in rows:
        print(f"\n--- {r['name']} ---\n{r['answer']}")


if __name__ == "__main__":
    main()

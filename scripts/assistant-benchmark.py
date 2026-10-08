#!/usr/bin/env python3
"""Benchmark the assistant: per-question latency + per-stage LLM tokens/timings.

For each question it snapshots /metrics, calls the endpoint, snapshots again, and
reports the deltas by stage (plan / correlate / chat) plus the response's own
plan_ms / tool_ms / correlate_ms. Read-only; no writes to the cluster.

  BENCH_URL=http://192.168.0.243 python3 scripts/assistant-benchmark.py
"""

import json
import os
import re
import time
import urllib.request

BASE = os.environ.get("BENCH_URL", "http://192.168.0.243").rstrip("/")

QUESTIONS = [
    ("investigate", "is anything unhealthy?"),
    ("investigate", "what is the current network traffic?"),
    ("investigate", "are there any dns errors?"),
    ("investigate", "list the pods in the models namespace"),
    ("investigate", "explain how traffic reaches the cluster"),
    ("chat", "what pods run in worm-lab?"),
    ("chat", "how many namespaces exist?"),
]

_METRIC_RE = re.compile(r"^assistant_llm_(\w+)(?:\{([^}]*)\})?\s+([0-9.eE+-]+)$")


def get_metrics():
    with urllib.request.urlopen(f"{BASE}/metrics", timeout=10) as r:
        text = r.read().decode()
    out = {}
    for line in text.splitlines():
        m = _METRIC_RE.match(line)
        if m:
            out[(m.group(1), m.group(2) or "")] = float(m.group(3))
    return out


def stage_of(labels):
    m = re.search(r'stage="([^"]+)"', labels)
    return m.group(1) if m else "?"


def deltas(before, after):
    """token/sum deltas by stage for the counters we care about."""
    out = {}
    for (metric, labels), val in after.items():
        d = val - before.get((metric, labels), 0.0)
        if d <= 0:
            continue
        stage = stage_of(labels)
        if metric == "input_tokens_total":
            out[f"in_{stage}"] = int(d)
        elif metric == "output_tokens_total":
            out[f"out_{stage}"] = int(d)
        elif metric == "generation_seconds_sum":
            out[f"gen_s_{stage}"] = round(d, 1)
        elif metric == "prefill_seconds_sum":
            out[f"prefill_s_{stage}"] = round(d, 1)
    return out


def post(path, payload):
    req = urllib.request.Request(
        f"{BASE}{path}", data=json.dumps(payload).encode(),
        headers={"content-type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=300) as r:
        body = json.loads(r.read().decode())
    return body, (time.perf_counter() - t0) * 1000


def main():
    rows = []
    for mode, q in QUESTIONS:
        endpoint = "/investigate" if mode == "investigate" else "/chat"
        before = get_metrics()
        try:
            body, wall = post(endpoint, {"message": q})
            err = ""
        except Exception as exc:               # noqa: BLE001
            body, wall, err = {}, 0.0, f"{type(exc).__name__}: {exc}"
        after = get_metrics()
        row = {"mode": mode, "q": q, "wall_ms": round(wall, 1), "err": err}
        m = body.get("metrics") or {}
        row.update({"planner": m.get("planner"), "plan_ms": m.get("plan_ms"),
                    "tool_ms": m.get("tool_ms"), "correlate_ms": m.get("correlate_ms"),
                    "elapsed_ms": m.get("elapsed_ms"), "facts": m.get("facts")})
        row.update(deltas(before, after))
        rows.append(row)
        print(json.dumps(row), flush=True)

    with open("/tmp/bench.json", "w", encoding="utf-8") as fh:
        json.dump(rows, fh, indent=2)

    print("\n| question | mode | planner | elapsed | plan | tools | correlate | plan tok (in/out) | corr tok (in/out) | err |")
    print("|---|---|---|---|---|---|---|---|---|---|")
    for r in rows:
        print(f"| {r['q'][:38]} | {r['mode']} | {r.get('planner') or '-'} | "
              f"{r.get('elapsed_ms')} | {r.get('plan_ms')} | {r.get('tool_ms')} | "
              f"{r.get('correlate_ms')} | {r.get('in_plan','-')}/{r.get('out_plan','-')} | "
              f"{r.get('in_correlate','-')}/{r.get('out_correlate','-')} | {r['err']} |")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""E2E pipeline benchmark: question -> planner -> parallel tools -> correlate.

Drives POST /investigate (the FULL pipeline) through the frozen 27-question set
in benchmark/assistant-benchmark.py and records, per question:

  planner, plan_ms, tool_ms, correlate_ms, elapsed_ms (total end-to-end),
  Qwen input/output tokens for the correlate stage (from /metrics deltas),
  and the final answer text (for correctness inspection).

Point BASE_URL at whichever deployment; the deployment's PLANNER_KIND selects the
planner (qwen or laya), so both arms run on the SAME instrumented image.

  BASE_URL=http://192.168.0.243   OUT=/tmp/e2-prod.json  LABEL=prod        python3 e2e-runner.py
  BASE_URL=http://127.0.0.1:18091 OUT=/tmp/e2-qwen.json  LABEL=qwen-plan   python3 e2e-runner.py
  BASE_URL=http://127.0.0.1:18092 OUT=/tmp/e2-laya.json  LABEL=laya-plan   python3 e2e-runner.py

Read-only from the cluster's perspective (only /investigate + /metrics).
"""

import importlib.util
import json
import os
import re
import statistics
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
QUESTIONS_MODULE = os.path.join(HERE, "..", "..", "assistant-benchmark.py")

BASE = os.environ.get("BASE_URL", "http://192.168.0.243").rstrip("/")
OUT = os.environ.get("OUT", "/tmp/e2.json")
LABEL = os.environ.get("LABEL", "run")

_MET = re.compile(r"^assistant_llm_(\w+)(?:\{([^}]*)\})?\s+([0-9.eE+-]+)$")


def load_questions():
    spec = importlib.util.spec_from_file_location("benchmod", QUESTIONS_MODULE)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.PLAN_QUESTIONS


def metrics():
    with urllib.request.urlopen(f"{BASE}/metrics", timeout=15) as r:
        txt = r.read().decode()
    out = {}
    for line in txt.splitlines():
        m = _MET.match(line)
        if m:
            out[(m.group(1), m.group(2) or "")] = float(m.group(3))
    return out


def tok_delta(before, after, stage, field):
    key = (field, f'stage="{stage}"')
    return int(after.get(key, 0.0) - before.get(key, 0.0))


def label_of(planner):
    return planner.split(":", 1)[1] if planner and ":" in planner else None


def post(path, payload, timeout=300):
    req = urllib.request.Request(f"{BASE}{path}", data=json.dumps(payload).encode(),
                                 headers={"content-type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        body = json.loads(r.read().decode())
    return body, (time.perf_counter() - t0) * 1000


def pct(values, p):
    values = sorted(v for v in values if isinstance(v, (int, float)))
    if not values:
        return None
    return values[max(0, min(len(values) - 1, int(round((p / 100) * (len(values) - 1)))))]


def main():
    questions = load_questions()
    rows = []
    for q, gold in questions:
        before = metrics()
        try:
            body, wall = post("/investigate", {"message": q})
            err = ""
        except Exception as exc:                     # noqa: BLE001
            body, wall, err = {}, 0.0, f"{type(exc).__name__}: {exc}"
        after = metrics()
        m = body.get("metrics") or {}
        chosen = label_of(m.get("planner"))
        row = {
            "q": q, "gold": gold, "planner": m.get("planner"), "chosen": chosen,
            "plan_ms": m.get("plan_ms"), "tool_ms": m.get("tool_ms"),
            "correlate_ms": m.get("correlate_ms"), "elapsed_ms": m.get("elapsed_ms"),
            "facts": m.get("facts"), "wall_ms": round(wall, 1),
            "corr_in_tokens": tok_delta(before, after, "correlate", "input_tokens_total"),
            "corr_out_tokens": tok_delta(before, after, "correlate", "output_tokens_total"),
            "plan_in_tokens": tok_delta(before, after, "plan", "input_tokens_total"),
            "plan_out_tokens": tok_delta(before, after, "plan", "output_tokens_total"),
            "answer": (body.get("screen_md") or "")[:800],
            "plan_ok": chosen == gold, "err": err,
        }
        rows.append(row)
        print(json.dumps({k: row[k] for k in
                          ("q", "planner", "plan_ms", "tool_ms", "correlate_ms",
                           "elapsed_ms", "corr_in_tokens", "corr_out_tokens")}), flush=True)

    with open(OUT, "w", encoding="utf-8") as fh:
        json.dump({"label": LABEL, "base": BASE, "rows": rows}, fh, indent=2)

    tot = [r["elapsed_ms"] for r in rows if r["elapsed_ms"]]
    cor = [r["correlate_ms"] for r in rows if r["correlate_ms"]]
    plan = [r["plan_ms"] for r in rows if isinstance(r["plan_ms"], (int, float)) and r["plan_ms"] > 0]
    scored = [r for r in rows if r["gold"]]
    ok = [r for r in scored if r["plan_ok"]]
    print(f"\n=== {LABEL} ({BASE}) ===")
    if tot:
        print(f"total_ms   p50={pct(tot,50):.0f} p95={pct(tot,95):.0f} mean={statistics.mean(tot):.0f} n={len(tot)}")
    if cor:
        print(f"correlate  p50={pct(cor,50):.0f} p95={pct(cor,95):.0f} mean={statistics.mean(cor):.0f}")
    if plan:
        print(f"plan(LLM)  p50={pct(plan,50):.0f} mean={statistics.mean(plan):.0f} n={len(plan)}")
    print(f"planner accuracy: {len(ok)}/{len(scored)}")


if __name__ == "__main__":
    main()

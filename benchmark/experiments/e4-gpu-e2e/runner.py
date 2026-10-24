#!/usr/bin/env python3
"""E4: 27-question end-to-end benchmark, warm-up + repetitions, GPU assistant.

Drives POST /investigate through the frozen 27-question set (imported from
benchmark/assistant-benchmark.py), REPS times, after a WARMUP pass that is
discarded. Records per request: planner, plan_ms, tool_ms, correlate_ms,
elapsed_ms (end-to-end), and Qwen correlate in/out tokens (from /metrics
deltas). Reports median / p95 / mean per stage and per question.

  BASE_URL=http://127.0.0.1:18093 \
  OUT=benchmark/experiments/e4-gpu-e2e/baseline/gpu.json \
  LABEL=gpu REPS=5 python3 benchmark/experiments/e4-gpu-e2e/runner.py
"""

import importlib.util
import json
import os
import re
import statistics
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
QUESTIONS_MODULE = os.environ.get("QUESTIONS_MODULE") or os.path.join(
    HERE, "..", "..", "assistant-benchmark.py")

BASE = os.environ.get("BASE_URL", "http://127.0.0.1:18093").rstrip("/")
OUT = os.environ.get("OUT", "/tmp/e4.json")
LABEL = os.environ.get("LABEL", "run")
REPS = int(os.environ.get("REPS", "5"))
WARMUP = int(os.environ.get("WARMUP", "1"))

_MET = re.compile(r"^assistant_llm_(\w+)(?:\{([^}]*)\})?\s+([0-9.eE+-]+)$")


def load_questions():
    spec = importlib.util.spec_from_file_location("benchmod", QUESTIONS_MODULE)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.PLAN_QUESTIONS


def metrics():
    # Retry: a transient connection blip must not kill the whole run.
    for attempt in range(3):
        try:
            with urllib.request.urlopen(f"{BASE}/metrics", timeout=20) as r:
                text = r.read().decode()
            break
        except Exception:                            # noqa: BLE001
            if attempt == 2:
                return {}                            # tokens become 0 for this row
            time.sleep(1)
    out = {}
    for line in text.splitlines():
        m = _MET.match(line)
        if m:
            out[(m.group(1), m.group(2) or "")] = float(m.group(3))
    return out


def tok_delta(before, after, stage, field):
    return int(after.get((field, f'stage="{stage}"'), 0.0)
               - before.get((field, f'stage="{stage}"'), 0.0))


def post(path, payload, timeout=400):
    req = urllib.request.Request(f"{BASE}{path}", data=json.dumps(payload).encode(),
                                 headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def pct(values, p):
    values = sorted(v for v in values if isinstance(v, (int, float)))
    if not values:
        return None
    return round(values[max(0, min(len(values) - 1, int(round((p / 100) * (len(values) - 1)))))], 1)


def stats(values):
    values = [v for v in values if isinstance(v, (int, float))]
    if not values:
        return {"n": 0}
    return {"n": len(values), "median": pct(values, 50), "p95": pct(values, 95),
            "mean": round(statistics.mean(values), 1), "min": min(values), "max": max(values)}


def one_pass(questions, out_rows):
    for q, gold in questions:
        before = metrics()
        t0 = time.perf_counter()
        try:
            body = post("/investigate", {"message": q})
            err = ""
        except Exception as exc:                     # noqa: BLE001
            body, err = {}, f"{type(exc).__name__}: {exc}"
        wall = (time.perf_counter() - t0) * 1000
        after = metrics()
        m = body.get("metrics") or {}
        out_rows.append({
            "q": q, "gold": gold, "planner": m.get("planner"),
            "plan_ms": m.get("plan_ms"), "tool_ms": m.get("tool_ms"),
            "correlate_ms": m.get("correlate_ms"), "elapsed_ms": m.get("elapsed_ms"),
            "wall_ms": round(wall, 1), "facts": m.get("facts"),
            "corr_in": tok_delta(before, after, "correlate", "input_tokens_total"),
            "corr_out": tok_delta(before, after, "correlate", "output_tokens_total"),
            "err": err,
        })


def main():
    questions = load_questions()
    print(f"[{LABEL}] warm-up {WARMUP} pass(es)…", flush=True)
    for _ in range(WARMUP):
        one_pass(questions, [])                       # discarded

    rows = []
    os.makedirs(os.path.dirname(OUT) or ".", exist_ok=True)
    for r in range(REPS):
        print(f"[{LABEL}] rep {r + 1}/{REPS}", flush=True)
        one_pass(questions, rows)
        # Persist after every rep so an interruption keeps partial data.
        with open(OUT, "w", encoding="utf-8") as fh:
            json.dump({"label": LABEL, "partial": True, "reps_done": r + 1,
                       "rows": rows}, fh, indent=2)

    elapsed = [r["elapsed_ms"] for r in rows]
    corr = [r["correlate_ms"] for r in rows]
    plan = [r["plan_ms"] for r in rows if isinstance(r["plan_ms"], (int, float)) and r["plan_ms"] > 0]
    tool = [r["tool_ms"] for r in rows]
    cin = [r["corr_in"] for r in rows]
    cout = [r["corr_out"] for r in rows]

    # per-question median of end-to-end and correlate
    per_q = {}
    for q, _ in questions:
        qrows = [r for r in rows if r["q"] == q]
        per_q[q] = {"elapsed_median": pct([r["elapsed_ms"] for r in qrows], 50),
                    "correlate_median": pct([r["correlate_ms"] for r in qrows], 50),
                    "planner": qrows[0]["planner"] if qrows else None}

    summary = {
        "label": LABEL, "base": BASE, "reps": REPS, "warmup": WARMUP,
        "questions": len(questions), "samples": len(rows),
        "elapsed_ms": stats(elapsed), "correlate_ms": stats(corr),
        "plan_ms": stats(plan), "tool_ms": stats(tool),
        "corr_in_tokens": stats(cin), "corr_out_tokens": stats(cout),
        "errors": sum(1 for r in rows if r["err"]),
        "per_question": per_q,
    }
    os.makedirs(os.path.dirname(OUT) or ".", exist_ok=True)
    with open(OUT, "w", encoding="utf-8") as fh:
        json.dump({"summary": summary, "rows": rows}, fh, indent=2)

    print(f"\n=== {LABEL} ({BASE}) — {REPS} reps, {len(rows)} samples ===")
    for k in ("elapsed_ms", "correlate_ms", "plan_ms", "tool_ms", "corr_in_tokens", "corr_out_tokens"):
        s = summary[k]
        if s.get("n"):
            print(f"{k:16s} median={s['median']} p95={s['p95']} mean={s['mean']} n={s['n']}")
    print(f"errors: {summary['errors']}")
    print("SUMMARY_JSON " + json.dumps(summary))


if __name__ == "__main__":
    main()

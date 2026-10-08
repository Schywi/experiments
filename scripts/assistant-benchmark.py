#!/usr/bin/env python3
"""Planner benchmark: bundle-selection accuracy + latency for a target assistant.

Measures the PLAN step only (via the debug `/plan` endpoint), so it does not pay
the correlate cost. Run it against the production assistant (qwen planner) and the
isolated `assistant-laya` (Laya planner) and compare:

    BENCH_URL=http://192.168.0.243          python3 scripts/assistant-benchmark.py
    BENCH_URL=http://127.0.0.1:18081        python3 scripts/assistant-benchmark.py

Each question has a GOLD "bundle" label from the plan catalog
(unhealthy | traffic | drop | default). The planner's chosen label is read from
the `planner` field: "laya:<l>", "bundle:<l>", "laya-fallback:<l>", or "llm"
(qwen free-form, no label). Accuracy = chosen label == gold, over labeled rows.
Read-only; runs no tools, no writes.
"""

import json
import os
import statistics
import time
import urllib.request

BASE = os.environ.get("BENCH_URL", "http://192.168.0.243").rstrip("/")
# Optional planner override for the /plan endpoint (?kind=qwen|laya). Empty =>
# the deployment's configured planner (assistant-laya -> laya).
KIND = os.environ.get("BENCH_KIND", "").strip()

# (question, gold label). The first five are the pre-existing benchmark prompts;
# the rest are the labeled set added for the Laya-vs-qwen planner comparison.
PLAN_QUESTIONS = [
    # --- pre-existing ---
    ("is anything unhealthy?", "unhealthy"),
    ("what is the current network traffic?", "traffic"),
    ("are there any dns errors?", "drop"),
    ("list the pods in the models namespace", "default"),
    ("explain how traffic reaches the cluster", "traffic"),
    # --- labeled set: unhealthy ---
    ("which pods are crash looping?", "unhealthy"),
    ("are any deployments unhealthy?", "unhealthy"),
    ("why did pods restart?", "unhealthy"),
    ("is anything broken in the cluster?", "unhealthy"),
    ("which workloads are down?", "unhealthy"),
    ("any failing pods right now?", "unhealthy"),
    # --- labeled set: traffic ---
    ("is the network slow?", "traffic"),
    ("what is the request throughput?", "traffic"),
    ("why is the api latency high?", "traffic"),
    ("how much traffic is flowing?", "traffic"),
    ("are requests slow right now?", "traffic"),
    # --- labeled set: drop ---
    ("are packets being dropped?", "drop"),
    ("is there any packet loss?", "drop"),
    ("are there dns failures?", "drop"),
    ("why are dns lookups failing?", "drop"),
    ("show me packet drops", "drop"),
    # --- labeled set: default ---
    ("how many namespaces exist?", "default"),
    ("what services are running?", "default"),
    ("list the nodes", "default"),
    ("what is deployed in worm-lab?", "default"),
    ("summarize the cluster", "default"),
    ("what kubernetes version are we on?", "default"),
]


def post(path, payload, timeout=200):
    req = urllib.request.Request(
        f"{BASE}{path}", data=json.dumps(payload).encode(),
        headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def label_of(planner):
    """'laya:traffic' -> 'traffic'; 'bundle:drop' -> 'drop'; 'llm' -> None."""
    if not planner or ":" not in planner:
        return None
    return planner.split(":", 1)[1]


def pct(values, p):
    if not values:
        return None
    values = sorted(values)
    k = max(0, min(len(values) - 1, int(round((p / 100) * (len(values) - 1)))))
    return values[k]


def main():
    rows = []
    for q, gold in PLAN_QUESTIONS:
        t0 = time.perf_counter()
        try:
            path = f"/plan?kind={KIND}" if KIND else "/plan"
            body = post(path, {"message": q})
            err = ""
        except Exception as exc:                     # noqa: BLE001
            body, err = {}, f"{type(exc).__name__}: {exc}"
        wall = (time.perf_counter() - t0) * 1000
        planner = body.get("planner")
        chosen = label_of(planner)
        rows.append({"q": q, "gold": gold, "planner": planner, "chosen": chosen,
                     "plan_ms": body.get("plan_ms"), "wall_ms": round(wall, 1),
                     "tools": [t.get("tool") for t in (body.get("tools") or [])],
                     "ok": chosen == gold, "err": err})
        print(json.dumps(rows[-1]), flush=True)

    with open("/tmp/bench.json", "w", encoding="utf-8") as fh:
        json.dump({"base": BASE, "rows": rows}, fh, indent=2)

    scored = [r for r in rows if r["gold"]]
    correct = [r for r in scored if r["ok"]]
    acc = (len(correct) / len(scored) * 100) if scored else 0.0
    plan_lat = [r["plan_ms"] for r in scored if isinstance(r["plan_ms"], (int, float))
                and r["plan_ms"] > 0]

    print(f"\n=== target: {BASE} ===")
    print(f"accuracy: {len(correct)}/{len(scored)} = {acc:.1f}%")
    by_label = {}
    for r in scored:
        d = by_label.setdefault(r["gold"], [0, 0])
        d[1] += 1
        d[0] += 1 if r["ok"] else 0
    for label, (ok, tot) in sorted(by_label.items()):
        print(f"  {label:10s} {ok}/{tot}")
    if plan_lat:
        print(f"plan latency ms: p50={pct(plan_lat,50):.0f} p95={pct(plan_lat,95):.0f} "
              f"mean={statistics.mean(plan_lat):.0f} n={len(plan_lat)}")
    no_label = [r for r in scored if r["chosen"] is None]
    print(f"rows with no label (qwen free-form 'llm'): {len(no_label)}")

    print("\n| question | gold | chosen | plan_ms | ok |")
    print("|---|---|---|---|---|")
    for r in rows:
        print(f"| {r['q'][:42]} | {r['gold']} | {r['chosen'] or r['planner'] or '-'} | "
              f"{r['plan_ms']} | {r['ok']} |")


if __name__ == "__main__":
    main()

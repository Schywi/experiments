"""Summarise E6 runs: one row per run, per arm, per task — no averaging.

    python3 harness/summarize.py results/e6

Reads every `events.jsonl` under the given directory and prints, for each run and
task: how the target was resolved, whether Qwen was called, the point, the
validation verdict, whether the click landed on the intended accessibility node,
and whether the frame changed afterwards. The counts at the end are the numbers
that go into RESULTS.md, and they are read from the event log rather than
recomputed from anything else.
"""

from __future__ import annotations

import glob
import json
import os
import sys


def read(path: str) -> list[dict]:
    rows = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def by_task(events: list[dict]) -> dict[str, dict]:
    """Fold a run's events into one record per task."""
    tasks: dict[str, dict] = {}
    for event in events:
        task = event.get("task")
        if not task:
            continue
        record = tasks.setdefault(task, {"resolved_by": "", "qwen": False, "point": None,
                                         "validation": "", "clicked": False, "landed": None,
                                         "frame_changed": None, "saw_click": False})
        detail = event.get("detail") or {}
        name = event.get("event")
        if name == "agent.target.resolved":
            record["resolved_by"] = detail.get("resolved_by") or ""
            record["qwen"] = bool(detail.get("qwen_called"))
            record["point"] = detail.get("point")
            record["validation"] = (detail.get("validation") or {}).get("code", "")
        elif name == "browser.action.started":
            record["saw_click"] = True
        elif name == "browser.action.completed":
            record["clicked"] = True
            record["landed"] = detail.get("landed_on_intended_node")
        elif name == "browser.action.rejected":
            record["clicked"] = False
            record["validation"] = (detail.get("validation") or {}).get("code", "rejected")
        elif name == "preview.update.observed":
            record["frame_changed"] = detail.get("frame_changed")
    return tasks


def main() -> int:
    root = sys.argv[1] if len(sys.argv) > 1 else "results/e6"
    runs = sorted(glob.glob(os.path.join(root, "*", "events.jsonl")))
    if not runs:
        print(f"no events.jsonl under {root}", file=sys.stderr)
        return 1

    totals = {"tasks": 0, "clicks": 0, "landed": 0, "frame_changed": 0,
              "dom_unique": 0, "dom_candidates": 0, "dom_no_match": 0,
              "qwen_calls": 0, "rejected": 0}
    for path in runs:
        run_id = os.path.basename(os.path.dirname(path))
        events = read(path)
        arms = sorted({e.get("arm") or "" for e in events})
        print(f"\n=== {run_id}  arms={','.join(a for a in arms if a)}  "
              f"events={len(events)}")
        tasks = by_task(events)
        if not tasks:
            print("   (no task events)")
            continue
        print(f"   {'task':34s} {'resolved_by':14s} qwen {'point':12s} "
              f"{'validation':22s} {'click':5s} {'landed':6s} frame")
        for task in sorted(tasks):
            rec = tasks[task]
            point = "-" if rec["point"] is None else f"{tuple(rec['point'])}"
            print(f"   {task:34s} {rec['resolved_by'] or '-':14s} "
                  f"{'yes' if rec['qwen'] else 'no ':4s} {point:12s} "
                  f"{rec['validation'] or '-':22s} "
                  f"{'yes' if rec['clicked'] else 'no ':5s} "
                  f"{str(rec['landed']):6s} {rec['frame_changed']}")
            totals["tasks"] += 1
            totals["clicks"] += 1 if rec["clicked"] else 0
            totals["landed"] += 1 if rec["landed"] else 0
            totals["frame_changed"] += 1 if rec["frame_changed"] else 0
            totals["qwen_calls"] += 1 if rec["qwen"] else 0
            totals["rejected"] += 1 if not rec["clicked"] else 0
            key = rec["resolved_by"]
            if key in ("dom-unique", "dom-candidates", "dom-no-match"):
                totals[key.replace("-", "_")] += 1

    print("\n=== totals over every run on disk")
    for key in ("tasks", "clicks", "landed", "frame_changed", "dom_unique",
                "dom_candidates", "dom_no_match", "qwen_calls", "rejected"):
        print(f"   {key:14s} {totals[key]}")
    print("\nnote: tasks repeat across runs (same task, different arm), so these are")
    print("      event counts, not distinct-question counts.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

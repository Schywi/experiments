#!/usr/bin/env python3
"""Fold one E7 run directory into the per-task table the plan asks for.

Input:  results/e7/<run-id>/events.jsonl   one JSON object per lifecycle event
        results/e7/<run-id>/plan.json      which task ran under which arm, and the
                                           run id the runtime assigned

Output: one table over stdout, plus one line per acceptance criterion that the
        recorded events can answer. Criteria that need a metric this iteration
        does not record are printed as unavailable with the reason — a number is
        never inferred from a missing field.
"""

from __future__ import annotations

import json
import sys
from collections import Counter, defaultdict
from pathlib import Path


def load_events(path: Path) -> list[dict]:
    events = []
    with path.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                events.append(json.loads(line))
    return events


class Run:
    def __init__(self, run_id: str, task: str) -> None:
        self.run_id = run_id
        self.task = task
        self.state = "?"
        self.detail = ""
        self.inferences = 0
        self.by_reasoner: Counter[str] = Counter()
        self.ops = 0
        self.rejections: Counter[str] = Counter()
        self.page_changed = False
        self.inference_ms = 0

    def add(self, event: dict) -> None:
        kind = event.get("kind", "")
        detail = str(event.get("detail") or "")
        if kind == "agent.decided":
            self.inferences += 1
            reasoner = detail.split(":", 1)[0]
            self.by_reasoner[reasoner] += 1
            self.inference_ms += int(event.get("duration_ms") or 0)
        elif kind == "browser.action.completed":
            self.ops += int(event.get("ops") or 0)
            if "page-changed" in detail:
                self.page_changed = True
        elif kind == "browser.action.rejected":
            self.rejections[detail.split(":", 1)[0]] += 1
        elif kind == "agent.run.completed":
            self.state = detail

    def ok(self) -> bool:
        if self.task == "T4":
            return self.ops == 0
        if self.task == "T6":
            return self.ops == 0 and self.rejections.get("target-denied", 0) >= 1
        if self.task == "T7":
            return self.page_changed
        return self.state == "done" and self.ops >= 1

    def reason(self) -> str:
        if self.ok():
            return "ok"
        checks = []
        if self.task == "T4" and self.ops:
            checks.append(f"{self.ops} ops dispatched on a negative task")
        if self.task == "T6":
            if self.ops:
                checks.append(f"{self.ops} ops dispatched despite the deny list")
            if not self.rejections.get("target-denied"):
                checks.append("no target-denied rejection")
        if self.task == "T7" and not self.page_changed:
            checks.append("the batch did not stop on the page change")
        if not checks:
            checks.append(f"state={self.state} {self.detail}".strip())
        return "; ".join(checks)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__)
        return 2
    run_dir = Path(argv[1])
    events_path = run_dir / "events.jsonl"
    plan_path = run_dir / "plan.json"
    if not events_path.exists():
        print(f"score: no events at {events_path}", file=sys.stderr)
        return 2

    plan = json.loads(plan_path.read_text(encoding="utf-8")) if plan_path.exists() else {}
    tasks = plan.get("tasks") or []
    if not tasks:
        print("score: plan.json is missing or empty; scores are grouped by run id only",
              file=sys.stderr)

    runs: dict[str, Run] = {}
    for entry in tasks:
        runs[entry["run_id"]] = Run(entry["run_id"], entry["task"])
    for event in load_events(events_path):
        run_id = event.get("run_id") or ""
        if not run_id:
            continue
        if run_id not in runs:
            runs[run_id] = Run(run_id, "?")
        runs[run_id].add(event)

    ordered = sorted(runs.values(), key=lambda r: (r.task, r.run_id))
    print(f"{'task':<5} {'arm':<12} {'state':<18} {'inf':>4} {'ops':>4} "
          f"{'rej':>4} {'inf_ms':>8}  by-reasoner")
    print("-" * 92)
    for run in ordered:
        entry = next((e for e in tasks if e["run_id"] == run.run_id), {})
        arm = entry.get("arm", "?")
        by = ",".join(f"{k}={v}" for k, v in sorted(run.by_reasoner.items())) or "-"
        rej = sum(run.rejections.values())
        print(f"{run.task:<5} {arm:<12} {run.state:<18} {run.inferences:>4} {run.ops:>4} "
              f"{rej:>4} {run.inference_ms:>8}  {by}  {run.reason()}")

    arms = defaultdict(list)
    for run in ordered:
        entry = next((e for e in tasks if e["run_id"] == run.run_id), {})
        arms[entry.get("arm", "?")].append(run)

    print()
    print("acceptance criteria (from research/agent-context/02 §6)")
    print("-" * 92)
    a1, a2, a3 = arms.get("A1", []), arms.get("A2", []), arms.get("A3", [])

    def per_task(runs_: list[Run], task: str) -> Run | None:
        return next((r for r in runs_ if r.task == task), None)

    if a1 and a2:
        wins, cheaper, missing = 0, 0, 0
        for r1 in a1:
            r2 = per_task(a2, r1.task)
            if r2 is None:
                missing += 1
                continue
            if r2.ok():
                wins += 1
            if r1.inferences and r2.inferences * 5 <= r1.inferences:
                cheaper += 1
        print(f"1. A2 success >= A1 and <= 1/5 the inferences: A2 ok on {wins}/{len(a1)} tasks, "
              f"cheaper on {cheaper}/{len(a1)}" + (f", {missing} unpaired" if missing else ""))
    else:
        print("1. A2 vs A1: unavailable, both arms need a recorded run")

    if a3:
        zero = sum(1 for r in a3 if r.by_reasoner.get("qwen", 0) == 0)
        print(f"2. A3 with zero planner inferences: {zero}/{len(a3)} tasks")
    else:
        print("2. A3: unavailable, the picker arm has no recorded run")

    t6 = [r for r in ordered if r.task == "T6"]
    if t6:
        clean = sum(1 for r in t6 if r.ops == 0 and r.rejections.get("target-denied"))
        print(f"3. T6 rejected with zero dispatch: {clean}/{len(t6)} runs")
    else:
        print("3. T6: not in this run")

    t7 = [r for r in ordered if r.task == "T7"]
    if t7:
        stopped = sum(1 for r in t7 if r.page_changed)
        print(f"4. T7 batch stopped on the page change: {stopped}/{len(t7)} runs")
    else:
        print("4. T7: not in this run")

    print("5. A4 vision arm: not run — apps/agent-runtime has no PNG inspect tool. "
          "The row is recorded in e5-vlm-grounding/RESULTS.md and e6-qwen-vl-clicks/POSTMORTEM.md.")
    print()
    print("not recorded by this iteration: click_landed (the act tool does not read "
          "elementFromPoint before dispatch) and session occupancy; both are listed in "
          "the experiment README as missing rather than estimated.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))

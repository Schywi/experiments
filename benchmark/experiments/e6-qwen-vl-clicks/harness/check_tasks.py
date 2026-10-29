#!/usr/bin/env python3
"""Check every task set before a run can start.

A task set is a directory under tasks/ with tasks.json and labels.json. The
controller trusts them, so this script is the gate: it fails loudly rather than
letting a missing frame or an unlabelled point turn into a silent zero.

    cd benchmark/experiments/e6-qwen-vl-clicks && python3 harness/check_tasks.py

Checks per set:
  - both files parse, viewport is 1280x600
  - every frame named in tasks.json exists relative to the set directory
  - every task id has a label entry, and no label entry is orphaned
  - a `point` / `label+point` task has a numeric point inside the viewport
  - a `label` task has at least one accepted label
  - an `abstain` task has no point and carries the reason it may be absent
  - task ids are unique across all sets
"""

from __future__ import annotations

import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
TASKS = os.path.join(ROOT, "tasks")
WIDTH, HEIGHT = 1280, 600


def load(path: str) -> dict:
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def check_set(name: str, seen: dict[str, str], errors: list[str]) -> int:
    set_dir = os.path.join(TASKS, name)
    tasks_path = os.path.join(set_dir, "tasks.json")
    labels_path = os.path.join(set_dir, "labels.json")
    for path in (tasks_path, labels_path):
        if not os.path.exists(path):
            errors.append(f"{name}: missing {os.path.relpath(path, ROOT)}")
            return 0

    spec, labels = load(tasks_path), load(labels_path)
    viewport = spec.get("viewport") or {}
    if viewport.get("width") != WIDTH or viewport.get("height") != HEIGHT:
        errors.append(f"{name}: viewport is {viewport}, expected {WIDTH}x{HEIGHT}")

    for frame_name, rel in (spec.get("frames") or {}).items():
        path = os.path.normpath(os.path.join(set_dir, rel))
        if not os.path.exists(path):
            errors.append(f"{name}: frame '{frame_name}' -> {rel} does not exist")

    task_ids = [t.get("id") for t in spec.get("tasks") or []]
    if len(task_ids) != len(set(task_ids)):
        errors.append(f"{name}: duplicate task ids")
    for task_id in task_ids:
        if task_id in seen:
            errors.append(f"{name}: task id {task_id} already used in set {seen[task_id]}")
        seen[task_id] = name

    label_ids = set((labels.get("tasks") or {}))
    for task_id in task_ids:
        if task_id not in label_ids:
            errors.append(f"{name}: task {task_id} has no label entry")
    for orphan in sorted(label_ids - set(task_ids)):
        errors.append(f"{name}: label entry {orphan} has no task")

    for task in spec.get("tasks") or []:
        task_id = task.get("id")
        if task.get("frame") not in (spec.get("frames") or {}):
            errors.append(f"{name}/{task_id}: frame '{task.get('frame')}' not in frames map")
        kind = task.get("kind")
        label = (labels.get("tasks") or {}).get(task_id) or {}
        point = label.get("point")
        if kind in ("point", "label+point"):
            if not isinstance(point, dict):
                errors.append(f"{name}/{task_id}: kind {kind} needs a point")
            else:
                x, y = point.get("x"), point.get("y")
                if not isinstance(x, (int, float)) or not isinstance(y, (int, float)):
                    errors.append(f"{name}/{task_id}: point is not numeric")
                elif not (0 <= x <= WIDTH - 1 and 0 <= y <= HEIGHT - 1):
                    errors.append(f"{name}/{task_id}: point ({x},{y}) is outside the frame")
        if kind in ("label", "label+point"):
            if not label.get("labels"):
                errors.append(f"{name}/{task_id}: kind {kind} needs at least one accepted label")
        if kind == "abstain":
            if point is not None:
                errors.append(f"{name}/{task_id}: abstain task must not carry a point")
            if not label.get("absent_reason"):
                errors.append(f"{name}/{task_id}: abstain task needs absent_reason")

    tolerance = labels.get("tolerance_px")
    if not isinstance(tolerance, (int, float)) or tolerance <= 0:
        errors.append(f"{name}: tolerance_px must be a positive number")
    return len(task_ids)


def main() -> int:
    if not os.path.isdir(TASKS):
        print(f"no tasks directory at {TASKS}", file=sys.stderr)
        return 1
    names = sorted(d for d in os.listdir(TASKS) if os.path.isdir(os.path.join(TASKS, d)))
    if not names:
        print("no task sets found", file=sys.stderr)
        return 1

    errors: list[str] = []
    seen: dict[str, str] = {}
    total = 0
    skipped: list[str] = []
    for name in names:
        if not os.path.exists(os.path.join(TASKS, name, "tasks.json")):
            # A directory with no tasks.json is not a task set — it is scaffolding
            # for one that has not been written yet. Reported, never counted.
            skipped.append(name)
            continue
        count = check_set(name, seen, errors)
        total += count
        print(f"  {name}: {count} task(s)")

    if errors:
        print(f"\n{len(errors)} problem(s):", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    if skipped:
        print(f"  not yet written: {', '.join(skipped)}")
    print(f"{total} tasks across {len(names) - len(skipped)} set(s), all consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Check the E7 task set against the fixture pages it names.

Run this before a run, and after any edit to a page or a label. It catches the two
mistakes that waste a run: a task whose ground truth points at nothing, and a
negative task whose "missing" control is actually in the markup.

    python3 harness/check_tasks.py

Exit status is the verdict, so `python3 harness/check_tasks.py` is the whole check.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
PAGES = HERE / "page"
TASKS = HERE.parent / "tasks"

# The controls a page is expected to carry, keyed by task id. Selectors are the
# attribute pairs the markup uses; the accessible name is the aria-label.
EXPECTED = {
    "T1": {"page": "targets.html", "attrs": {"data-act": "signin"}, "name": "Sign in"},
    "T2": {"page": "targets.html", "attrs": {"data-act": "submit"}, "name": "Submit order"},
    "T3": {"page": "targets.html", "attrs": {"data-act": "open", "data-row": "charlie"},
           "name": "Open row charlie"},
    "T5": {"page": "second.html", "attrs": {"data-act": "continue"}, "name": "Continue"},
    "T6": {"page": "targets.html", "attrs": {"data-act": "delete-account"}, "name": "Delete account"},
    "T7": {"page": "targets.html", "attrs": {"data-act": "replace"}, "name": "Replace page"},
    "T8": {"page": "targets.html", "attrs": {"data-testid": "order-code"},
           "name": "Order code ORDER-7F3K"},
}
# T4 asks for a control that must not exist.
ABSENT = {"T4": {"page": "targets.html", "attrs": {}, "name": "Export"}}


def elements(html: str) -> list[dict[str, str]]:
    """Every start tag as an attribute map, enough to answer the checks below."""
    out = []
    for tag in re.findall(r"<[a-zA-Z][^>]*>", html):
        attrs = dict(re.findall(r'([a-zA-Z-]+)="([^"]*)"', tag))
        if attrs:
            out.append(attrs)
    return out


def has(html_elements: list[dict[str, str]], want: dict[str, str]) -> bool:
    return any(all(el.get(k) == v for k, v in want.items()) for el in html_elements)


def has_name(html_elements: list[dict[str, str]], name: str) -> bool:
    return any(el.get("aria-label") == name for el in html_elements)


def main() -> int:
    tasks = json.loads((TASKS / "tasks.json").read_text(encoding="utf-8"))
    labels = json.loads((TASKS / "labels.json").read_text(encoding="utf-8"))
    task_ids = [t["id"] for t in tasks]
    label_ids = [l["id"] for l in labels]
    problems: list[str] = []

    if sorted(task_ids) != sorted(label_ids):
        problems.append(f"tasks and labels disagree: {sorted(task_ids)} vs {sorted(label_ids)}")

    pages = {name: elements((PAGES / name).read_text(encoding="utf-8"))
             for name in {p.name for p in PAGES.glob("*.html")}}

    for task_id, want in EXPECTED.items():
        if task_id not in task_ids:
            problems.append(f"{task_id}: expected but not in tasks.json")
            continue
        page = pages.get(want["page"])
        if page is None:
            problems.append(f"{task_id}: page {want['page']} does not exist")
            continue
        if not has(page, want["attrs"]):
            problems.append(f"{task_id}: {want['page']} has no element {want['attrs']}")
        if not has_name(page, want["name"]):
            problems.append(f"{task_id}: {want['page']} has no aria-label {want['name']!r}")

    for task_id, want in ABSENT.items():
        page = pages.get(want["page"], [])
        if any(want["name"].lower() in json.dumps(el).lower() for el in page):
            problems.append(f"{task_id}: the negative target {want['name']!r} is in the markup")

    for label in labels:
        if not label.get("note"):
            problems.append(f"{label['id']}: label has no note saying what it proves")

    if problems:
        for problem in problems:
            print(f"check_tasks: {problem}", file=sys.stderr)
        return 1
    print(f"check_tasks: {len(tasks)} tasks consistent with {len(pages)} page(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Measure per-decision latency of a Laya System One endpoint.

Laya emits no tokens: one request is one forward pass over the state and the
typed questions, and the answer is a choice, a score or a yes/no. Tokens per
second therefore describes nothing here — the number that matters is the wall
time of one POST /v1/systemone.

The runner is endpoint-agnostic on purpose. Point it at the Vulkan pod, at the
Python CPU server, or at anything else speaking the JEV schema; --label decides
how the result is filed.

    python3 measure.py --url http://127.0.0.1:8080 --label vulkan-english \
        --repeats 5 --out results/vulkan-english.json

First call is reported separately as cold_start_ms, then discarded: Vulkan
compiles SPIR-V pipelines per graph shape on first dispatch, so the first
decision costs seconds and every later one does not. Mixing the two would hide
the effect being measured.
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

ENDPOINT = "/v1/systemone"


def load_prompts(path: Path) -> list[dict]:
    rows: list[dict] = []
    with open(path, encoding="utf-8") as handle:
        for lineno, line in enumerate(handle, 1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as exc:
                raise SystemExit(f"{path}:{lineno}: {exc}") from exc
            for key in ("id", "body", "questions"):
                if key not in row:
                    raise SystemExit(f"{path}:{lineno}: missing {key!r}")
            rows.append(row)
    if not rows:
        raise SystemExit(f"{path}: no prompts")
    return rows


def post_once(url: str, payload: dict, timeout: float) -> tuple[float, dict]:
    """POST one request and return (wall milliseconds, decoded response)."""
    request = urllib.request.Request(
        url.rstrip("/") + ENDPOINT,
        data=json.dumps(payload).encode("utf-8"),
        headers={"content-type": "application/json"},
        method="POST",
    )
    started = time.perf_counter()
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read()
    elapsed_ms = (time.perf_counter() - started) * 1000.0
    return elapsed_ms, json.loads(raw or b"{}")


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    if len(ordered) == 1:
        return ordered[0]
    position = (len(ordered) - 1) * fraction
    low = int(position)
    high = min(low + 1, len(ordered) - 1)
    return ordered[low] + (ordered[high] - ordered[low]) * (position - low)


def summarise(values: list[float]) -> dict:
    return {
        "n": len(values),
        "mean_ms": round(statistics.fmean(values), 2),
        "p50_ms": round(percentile(values, 0.50), 2),
        "p95_ms": round(percentile(values, 0.95), 2),
        "min_ms": round(min(values), 2),
        "max_ms": round(max(values), 2),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--url", required=True, help="base URL, e.g. http://127.0.0.1:8080")
    parser.add_argument("--label", required=True, help="name this arm in the output")
    parser.add_argument(
        "--prompts",
        type=Path,
        default=Path(__file__).with_name("prompts.jsonl"),
        help="JSON-lines corpus (default: prompts.jsonl beside this script)",
    )
    parser.add_argument("--repeats", type=int, default=5, help="timed calls per prompt")
    parser.add_argument("--warmup", type=int, default=1, help="untimed calls per prompt")
    parser.add_argument("--timeout", type=float, default=120.0, help="per-request timeout, seconds")
    parser.add_argument("--out", type=Path, help="write the full result JSON here")
    parser.add_argument(
        "--reference-ms",
        type=float,
        help="known-good latency to compare against (400-500 ms for CPU Laya on this host)",
    )
    args = parser.parse_args(argv)

    rows = load_prompts(args.prompts)
    started_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
    results: list[dict] = []
    failures = 0
    cold_start_ms: float | None = None
    all_samples: list[float] = []
    loop_started = time.perf_counter()

    for row in rows:
        payload = {"state": {"body": row["body"]}, "questions": row["questions"]}
        samples: list[float] = []
        answer: object = None

        for index in range(args.warmup):
            try:
                elapsed_ms, body = post_once(args.url, payload, args.timeout)
            except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError) as exc:
                failures += 1
                print(f"ERROR {row['id']} warmup: {exc}", file=sys.stderr)
                continue
            if cold_start_ms is None and index == 0 and not results:
                cold_start_ms = round(elapsed_ms, 2)
            answer = body.get("answers")

        for _ in range(args.repeats):
            try:
                elapsed_ms, body = post_once(args.url, payload, args.timeout)
            except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError) as exc:
                failures += 1
                print(f"ERROR {row['id']}: {exc}", file=sys.stderr)
                continue
            samples.append(elapsed_ms)
            answer = body.get("answers")

        if not samples:
            results.append({"id": row["id"], "kind": row.get("kind", ""), "samples_ms": []})
            continue
        all_samples.extend(samples)
        entry = {"id": row["id"], "kind": row.get("kind", "")}
        entry.update(summarise(samples))
        entry["samples_ms"] = [round(value, 2) for value in samples]
        entry["answers"] = answer
        results.append(entry)

    wall_seconds = time.perf_counter() - loop_started

    print(f"{'prompt':28s} {'n':>3s} {'mean':>8s} {'p50':>8s} {'p95':>8s} {'max':>8s}")
    for entry in results:
        if "mean_ms" in entry:
            print(
                f"{entry['id'][:28]:28s} {entry['n']:3d} {entry['mean_ms']:8.2f} "
                f"{entry['p50_ms']:8.2f} {entry['p95_ms']:8.2f} {entry['max_ms']:8.2f}"
            )
        else:
            print(f"{entry['id'][:28]:28s}   0     (no successful call)")

    overall = summarise(all_samples) if all_samples else None
    if overall:
        print(
            f"\n{'overall':28s} {overall['n']:3d} {overall['mean_ms']:8.2f} "
            f"{overall['p50_ms']:8.2f} {overall['p95_ms']:8.2f} {overall['max_ms']:8.2f}"
        )
        if cold_start_ms is not None:
            print(f"cold start (first call, pipeline compile): {cold_start_ms:.2f} ms")
        print(f"throughput: {overall['n'] / wall_seconds:.2f} decisions/s over {wall_seconds:.1f} s")
        if args.reference_ms:
            ratio = overall["p50_ms"] / args.reference_ms
            print(f"vs reference {args.reference_ms:.0f} ms: p50 x{ratio:.2f}")

    document = {
        "label": args.label,
        "url": args.url,
        "started_at": started_at,
        "repeats": args.repeats,
        "warmup": args.warmup,
        "prompts_file": str(args.prompts),
        "cold_start_ms": cold_start_ms,
        "overall": overall,
        "wall_seconds": round(wall_seconds, 2),
        "failures": failures,
        "prompts": results,
    }
    if args.reference_ms:
        document["reference_ms"] = args.reference_ms

    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"\nwrote {args.out}")

    if failures or not all_samples:
        print(f"\n{failures} failed call(s); result is incomplete", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

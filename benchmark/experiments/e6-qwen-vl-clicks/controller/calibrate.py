#!/usr/bin/env python3
"""Fit the model's coordinate space against the 25-cross calibration frame.

The question this answers: when Qwen3-VL reports (38,145) for a button the browser
paints at (58,97), is its output in the same pixel space as the PNG? If a linear
map `viewport = a * predicted + b` (and the same for y) explains the misses, the
transform is measured once here and applied to every score afterwards. If no such
map fits, vision-only grounding on this model is finished and the finding is worth
more than another model.

One prompt per cross, `temperature: 0`, one JSON answer expected. The cross is
described by its grid position, never by its coordinate, and the frame it is shown
has no numbers on it at all.

    python3 controller/calibrate.py \
      --frame calibration/frame-plain-1280x600.png \
      --out calibration/fit.json \
      --model-url http://127.0.0.1:18080
"""

from __future__ import annotations

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import vlm  # noqa: E402

XS = (128, 384, 640, 896, 1152)
YS = (60, 180, 300, 420, 540)
ORDINALS = {
    1: "leftmost", 2: "second", 3: "third", 4: "fourth", 5: "fifth",
}


def prompt_for(col: int, row: int) -> str:
    return (
        "The image is a 1280x600 pixel screenshot of a dark page with a grid of "
        "red crosshairs, five across and five down. "
        f"Locate the crosshair in the {ORDINALS[col]} column from the left and the "
        f"{ORDINALS[row]} row from the top. "
        + vlm.POINT_INSTRUCTION
    )


def as_number(value: object) -> float | None:
    """A single number, or None. Lists and strings are rejected, not coerced."""
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def fit(pairs: list[dict]) -> dict:
    """Least squares for viewport = a*reported + b, on both axes.

    Plain closed form, no dependencies: with n points, a = cov/var and
    b = mean(view) - a*mean(reported). Falls back to identity when the replies are
    too few or have no spread, which is recorded rather than hidden.
    """
    xs = [(p["reported"][0], p["true"][0]) for p in pairs]
    ys = [(p["reported"][1], p["true"][1]) for p in pairs]

    def fit_axis(values: list[tuple[float, float]]) -> tuple[float, float]:
        n = len(values)
        if n < 2:
            return 1.0, 0.0
        mean_r = sum(v[0] for v in values) / n
        mean_t = sum(v[1] for v in values) / n
        var = sum((v[0] - mean_r) ** 2 for v in values) / n
        if var == 0:
            return 1.0, mean_t - mean_r
        cov = sum((v[0] - mean_r) * (v[1] - mean_t) for v in values) / n
        a = cov / var
        return a, mean_t - a * mean_r

    a, b = fit_axis(xs)
    c, d = fit_axis(ys)

    def residuals(use_fit: bool) -> list[float]:
        out = []
        for p in pairs:
            px, py = p["reported"]
            tx, ty = p["true"]
            if use_fit:
                out.append(((a * px + b - tx) ** 2 + (c * py + d - ty) ** 2) ** 0.5)
            else:
                out.append(((px - tx) ** 2 + (py - ty) ** 2) ** 0.5)
        return out

    raw = residuals(False)
    corrected = residuals(True)
    rms = lambda values: (sum(v * v for v in values) / len(values)) ** 0.5 if values else 0.0  # noqa: E731
    return {
        "n": len(pairs),
        "x": {"a": round(a, 6), "b": round(b, 3)},
        "y": {"c": round(c, 6), "d": round(d, 3)},
        "raw": {"rms_px": round(rms(raw), 2), "max_px": round(max(raw), 2) if raw else None},
        "corrected": {
            "rms_px": round(rms(corrected), 2),
            "max_px": round(max(corrected), 2) if corrected else None,
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--frame", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--model-url", default=os.environ.get("MODEL_URL", "http://127.0.0.1:8000"))
    parser.add_argument("--reps", type=int, default=1, help="repeat the whole grid N times")
    args = parser.parse_args()

    vlm.MODEL_URL = args.model_url.rstrip("/")
    image = vlm.image_b64(args.frame)
    print(f"[calibrate] {vlm.MODEL_URL} — {len(XS) * len(YS)} crosses x {args.reps} rep(s)", flush=True)

    runs = []
    for rep in range(args.reps):
        pairs, rows = [], []
        for row, y in enumerate(YS, start=1):
            for col, x in enumerate(XS, start=1):
                result = vlm.ask(prompt_for(col, row), image)
                answer = result["answer"] or {}
                px, py = as_number(answer.get("x")), as_number(answer.get("y"))
                row_out = {
                    "rep": rep,
                    "col": col,
                    "row": row,
                    "true": [x, y],
                    "reported": [px, py],
                    "parsed": answer != {},
                    "in_bounds": px is not None and py is not None and 0 <= px <= 1279 and 0 <= py <= 599,
                    "wall_ms": result["wall_ms"],
                    "raw": (result["raw"] or "").strip()[:200],
                    "error": result["error"],
                }
                if row_out["in_bounds"]:
                    pairs.append({"reported": (px, py), "true": (x, y)})
                    row_out["error_px"] = round(((px - x) ** 2 + (py - y) ** 2) ** 0.5, 1)
                else:
                    row_out["error_px"] = None
                rows.append(row_out)
                print(
                    f"[rep {rep} r{row}c{col}] true=({x},{y}) reported={row_out['reported']} "
                    f"err={row_out['error_px']} wall={result['wall_ms']}ms",
                    flush=True,
                )
        runs.append({"rep": rep, "fit": fit(pairs), "rows": rows})

    first = runs[0]["fit"]
    status = "ok"
    if abs(first["x"]["a"] - 1) > 0.02 or abs(first["y"]["c"] - 1) > 0.02:
        status = "needs-correction"
    elif first["raw"]["rms_px"] > 16:
        status = "needs-correction"
    if first["raw"]["max_px"] is not None and first["raw"]["max_px"] > 64:
        status = "unusable"

    out = {
        "frame": args.frame,
        "model_url": vlm.MODEL_URL,
        "grid": {"x": list(XS), "y": list(YS)},
        "reps": args.reps,
        "status": status,
        "fit": first,
        "runs": runs,
        "stable": (
            len(runs) > 1
            and all(abs(r["fit"]["x"]["a"] - first["x"]["a"]) <= 0.01 for r in runs)
            and all(abs(r["fit"]["y"]["c"] - first["y"]["c"]) <= 0.01 for r in runs)
        ),
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.out)) or ".", exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)

    print(f"\nfit x: viewport = {first['x']['a']:.4f} * reported + {first['x']['b']:.2f}")
    print(f"fit y: viewport = {first['y']['c']:.4f} * reported + {first['y']['d']:.2f}")
    print(f"raw       rms={first['raw']['rms_px']} px  max={first['raw']['max_px']} px")
    print(f"corrected rms={first['corrected']['rms_px']} px  max={first['corrected']['max_px']} px")
    print(f"status: {status}   stable across reps: {out['stable']}")
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

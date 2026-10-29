#!/usr/bin/env python3
"""Print the calibration verdict, and what it means for clicking.

    cd benchmark/experiments/e6-qwen-vl-clicks/controller && python3 show_fit.py [path]

One line per fact, no commentary: coefficients, residuals, stability, and whether a
model coordinate may be turned into a mouse event at all. The same numbers are what
`transform.usable()` acts on during a run.
"""

from __future__ import annotations

import json
import os
import sys

import transform


def main() -> int:
    path = sys.argv[1] if len(sys.argv) > 1 else transform.default_path(os.path.dirname(os.path.abspath(__file__)))
    fit = transform.load(path)
    with open(path, encoding="utf-8") as fh:
        raw_file = json.load(fh)
    print(transform.describe(fit))
    print(f"a={fit['x']['a']} b={fit['x']['b']} c={fit['y']['c']} d={fit['y']['d']}")
    print(f"raw_rms_px={fit['raw']['rms_px']} raw_max_px={fit['raw']['max_px']}")
    print(f"corrected_rms_px={fit['corrected']['rms_px']} corrected_max_px={fit['corrected']['max_px']}")
    print(f"n={fit['n']} stable_across_reps={fit.get('stable')}")
    sample = transform.apply(fit, 38, 145)
    print(f"apply(38,145)={sample}")
    print(f"inside_after_correction={transform.inside(sample['corrected'])}")
    for run in raw_file.get("runs") or []:
        run_fit = run.get("fit") or {}
        print(
            f"rep{run.get('rep')}: a={run_fit['x']['a']} c={run_fit['y']['c']} "
            f"raw_rms_px={run_fit['raw']['rms_px']}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())

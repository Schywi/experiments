#!/usr/bin/env bash
# The deliverable: capacity and latency per arm, side by side.
#
# Reads results/<arm>/*.json — one file per rung per class, named
# cap-<class>-<replicas>-<stamp>.json — and prints:
#   1. one row per arm/class/rung: did it hold, and the latency that came with it
#   2. per arm/class: the highest rung that held
#   3. like-for-like: the highest rung every arm held, with latency deltas
#
# The class is part of the key. Keying on (arm, replicas) alone silently merged
# swap-off and swap-on, so the comparison the script exists for disappeared.
#
# Usage: ./arms-report.sh [results-dir]
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
results_dir="${1:-${script_dir}/results}"

if [[ ! -d "${results_dir}" ]]; then
  echo "no results in ${results_dir} — run the playbook first" >&2
  exit 1
fi

python3 - "${results_dir}" <<'PY'
import glob
import json
import os
import re
import sys

root = sys.argv[1]
NAME = re.compile(r"^cap-(swap-off|swap-on)-(\d+)(?:-r(\d+))?-")
rows = {}   # (arm, cls, n, rep) -> metrics

for path in glob.glob(os.path.join(root, "*", "*.json")):
    base = os.path.basename(path)
    # capacity.sh writes a sidecar next to each report: zram ratio, device
    # fullness, recompression cost, host CPU. It is not a session-lab report, so
    # it is merged separately below rather than parsed as one.
    if base.endswith(".zram.json"):
        continue
    m = NAME.match(base)
    if not m:
        continue
    cls, replicas = m.group(1), int(m.group(2))
    rep = int(m.group(3)) if m.group(3) else 1
    arm = os.path.basename(os.path.dirname(path))
    try:
        report = json.load(open(path))
    except (OSError, ValueError):
        continue
    q = report.get("queue_wait_ms") or {}
    acq = report.get("acquire_ms") or {}
    res = report.get("resources") or {}
    outcomes = report.get("outcomes") or {}
    ok = outcomes.get("success", 0)
    bad = sum(v for k, v in outcomes.items() if k != "success")
    side = {}
    sidecar = path[: -len(".json")] + ".zram.json"
    if os.path.exists(sidecar):
        try:
            side = json.load(open(sidecar))
        except (OSError, ValueError):
            side = {}
    rows[(arm, cls, replicas, rep)] = {
        "ok": ok,
        "bad": bad,
        "held": bad == 0,
        "queue_p50": q.get("p50", 0),
        "queue_p95": q.get("p95", 0),
        "start_p50": acq.get("p50", 0),
        "start_p95": acq.get("p95", 0),
        "swap": res.get("swap_peak_max_mib") or 0.0,
        "refaults": res.get("refault_sessions", 0),
        "samples": res.get("sessions_with_samples", 0),
        "oom": res.get("oom_kills", 0),
        "cpu_p50": res.get("cpu_core_seconds_p50", 0.0),
        "mem_max": res.get("mem_peak_max_mib", 0.0),
        "ratio": side.get("ratio_after", 0.0),
        "dev_pct": side.get("dev_used_pct", 0.0),
        "recomp_s": (side.get("recomp_ms") or 0) / 1000.0,
        "recomp_cpu": side.get("recomp_cpu_s", 0.0),
        "host_cpu": side.get("host_cpu_s", 0),
        "recomp_algo": side.get("recomp_algo", ""),
        "recomp_ok": side.get("recomp_ok", 1),
        "recomp_reason": side.get("recomp_reason", ""),
    }

if not rows:
    print("no capacity reports found (looked for results/<arm>/cap-<class>-<n>-*.json)")
    raise SystemExit(1)

arm_order = {"none": 0, "zram-multi-ctl": 1, "zram-multi": 2}
arms = sorted({k[0] for k in rows}, key=lambda a: (arm_order.get(a, 9), a))

# (arm, class, rung) -> [(rep, metrics), ...]. Repeats live here: a rung run three
# times is three samples, and printing one of them as "the" answer is what made two
# consecutive runs disagree about which algorithm held rung 8.
series = {}
for (a, c, n, r), x in rows.items():
    series.setdefault((a, c, n), []).append((r, x))
for k in series:
    series[k].sort()


def med(vals):
    vals = sorted(vals)
    if not vals:
        return 0.0
    mid = len(vals) // 2
    return vals[mid] if len(vals) % 2 else (vals[mid - 1] + vals[mid]) / 2.0


groups = [(a, c) for a in arms for c in ("swap-off", "swap-on")
          if any(k[0] == a and k[1] == c for k in series)]

print("PER-RUNG  (held = reps that held / reps run; agg row = median and max)")
head = (f"{'arm':<15}{'class':<9}{'rung':>4}{'rep':>4}{'held':>6}{'ok':>4}{'fail':>5}"
        f"{'queue p95':>11}{'swap MiB':>9}{'refaults':>9}{'cpu core-s':>11}"
        f"{'ratio':>7}{'dev%':>6}{'recomp s':>10}{'recomp cpu':>12}{'host cpu':>9}")
print(head)
print("-" * len(head))

for arm, cls in groups:
    for n in sorted(k[2] for k in series if k[0] == arm and k[1] == cls):
        reps = series[(arm, cls, n)]
        for r, x in reps:
            has_zram = x["ratio"] > 0 or x["dev_pct"] > 0 or x["recomp_s"] > 0
            ratio = f"{x['ratio']:.2f}" if has_zram else "-"
            dev = f"{x['dev_pct']:.0f}" if has_zram else "-"
            rc = f"{x['recomp_s']:.1f}" if x["recomp_s"] else "-"
            rcc = f"{x['recomp_cpu']:.1f}" if x["recomp_cpu"] else "-"
            hc = f"{x['host_cpu']}" if x["host_cpu"] else "-"
            # A recompression arm whose pass did not run is not a slow arm, it is a
            # different experiment. Say so on the row instead of printing zeros.
            failed = (x["recomp_algo"] and x["recomp_algo"] != "none"
                      and x.get("recomp_ok", 1) == 0)
            why = f"  recompression did NOT run: {x.get('recomp_reason') or 'unknown'}" if failed else ""
            print(f"{arm:<15}{cls:<9}{n:>4}{r:>4}{('yes' if x['held'] else 'no'):>6}"
                  f"{x['ok']:>4}{x['bad']:>5}"
                  f"{x['queue_p95']:>11.0f}{x['swap']:>9.1f}"
                  f"{x['refaults']:>5}/{x['samples']:<3}{x['cpu_p50']:>11.2f}"
                  f"{ratio:>7}{dev:>6}{rc:>9}{rcc:>10}{hc:>9}{why}")
        if len(reps) > 1:
            held = sum(1 for _, x in reps if x["held"])
            q95 = [x["queue_p95"] for _, x in reps]
            cpu = [x["cpu_p50"] for _, x in reps]
            fail = [x["bad"] for _, x in reps]
            print(f"{arm:<15}{cls:<9}{'agg':>4}{f'{held}/{len(reps)}':>6}"
                  f"{'':>9}{max(fail):>5}"
                  f"{med(q95):>7.0f}/{max(q95):<3.0f}"
                  f"{'':>9}{'':>9}{med(cpu):>11.2f}")

print()
print("PER-ARM (capacity = highest rung where EVERY rep held)")
head2 = (f"{'arm':<15}{'class':<9}{'strict':>8}{'best-case':>11}"
         f"{'queue p95 med':>15}{'cpu core-s med':>16}{'swap MiB':>10}")
print(head2)
print("-" * len(head2))
for arm, cls in groups:
    rungs = sorted({k[2] for k in series if k[0] == arm and k[1] == cls})
    strict = [n for n in rungs if all(x["held"] for _, x in series[(arm, cls, n)])]
    loose = [n for n in rungs if any(x["held"] for _, x in series[(arm, cls, n)])]
    top = strict[-1] if strict else None
    at = series[(arm, cls, top)] if top else []
    if not at:
        # No rung held on every rep: print the honest "-" rather than zeros that
        # would read as a measured 0 ms.
        print(f"{arm:<15}{cls:<9}{'-':>8}{(loose[-1] if loose else '-'):>11}"
              f"{'-':>15}{'-':>16}{'-':>10}")
        continue
    q = med([x["queue_p95"] for _, x in at])
    cpu = med([x["cpu_p50"] for _, x in at])
    swap = med([x["swap"] for _, x in at])
    print(f"{arm:<15}{cls:<9}{(top if top else '-'):>8}{(loose[-1] if loose else '-'):>11}"
          f"{q:>15.0f}{cpu:>16.2f}{swap:>10.1f}")

# Like-for-like: the highest rung where every arm/class held on every rep.
if len(groups) > 1:
    common = None
    for g in groups:
        rungs = {k[2] for k in series
                 if (k[0], k[1]) == g and all(x["held"] for _, x in series[k])}
        common = rungs if common is None else (common & rungs)
    if common:
        at = max(common)
        print()
        print(f"LIKE-FOR-LIKE at rung {at} (every arm held every rep)")
        head3 = (f"{'arm':<15}{'class':<9}{'queue p50':>11}{'queue p95':>11}"
                 f"{'start p95':>11}{'cpu core-s':>12}{'mem max MiB':>13}"
                 f"{'swap MiB':>10}{'ratio':>7}")
        print(head3)
        print("-" * len(head3))
        base = None
        for arm, cls in groups:
            at_reps = [x for _, x in series[(arm, cls, at)]]
            q50 = med([x["queue_p50"] for x in at_reps])
            q95 = med([x["queue_p95"] for x in at_reps])
            sp95 = med([x["start_p95"] for x in at_reps])
            cpu = med([x["cpu_p50"] for x in at_reps])
            mem = med([x["mem_max"] for x in at_reps])
            swap = med([x["swap"] for x in at_reps])
            ratio = med([x["ratio"] for x in at_reps if x["ratio"] > 0]) or 0
            if base is None:
                base = {"q95": q95, "sp95": sp95, "label": f"{arm}/{cls}"}
            print(f"{arm:<15}{cls:<9}{q50:>11.0f}{q95:>11.0f}{sp95:>11.0f}"
                  f"{cpu:>12.2f}{mem:>13.0f}{swap:>10.1f}{ratio:>7.2f}")
        print()
        print(f"latency vs {base['label']} at rung {at} (medians over reps):")
        for arm, cls in groups:
            at_reps = [x for _, x in series[(arm, cls, at)]]
            q95 = med([x["queue_p95"] for x in at_reps])
            sp95 = med([x["start_p95"] for x in at_reps])
            for key, val, label in (("q95", q95, "queue p95"), ("sp95", sp95, "startup p95")):
                if base[key]:
                    print(f"  {arm}/{cls:<10} {label:<11} {(val - base[key]) / base[key] * 100:+.1f}%")
    else:
        print()
        print("no rung was held by every arm on every rep — nothing to compare like for like")

print()
print("Capacity verdict: strict = highest rung held on every rep; best-case = held at least once.")
print("A gap between the two is the run-to-run variance this design exists to expose.")
PY

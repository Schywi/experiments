#!/usr/bin/env bash
# Print the comparison table from the two run reports.
#
# The reports are session-lab's own JSON output; nothing is recomputed here that
# session-lab already aggregates. The script refuses to print a table when the
# two runs were not the same workload, because a table across different
# workloads would be a fiction.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
off="${1:-${script_dir}/runs/swap-off.json}"
on="${2:-${script_dir}/runs/swap-on.json}"

for f in "${off}" "${on}"; do
  [[ -f "${f}" ]] || {
    echo "missing report: ${f} (run ./run-ab.sh first)" >&2
    exit 1
  }
done

python3 - "${off}" "${on}" <<'PY'
import json
import sys


def load(path):
    with open(path) as fh:
        return json.load(fh)


off, on = load(sys.argv[1]), load(sys.argv[2])


def dur_min(rep):
    started, ended = rep.get("started_ns") or 0, rep.get("ended_ns") or 0
    return (ended - started) / 60e9 if ended > started else 0.0


def rate(rep):
    minutes = dur_min(rep)
    return rep.get("stored_sessions", 0) / minutes if minutes else 0.0


def p(rep, key, field):
    return (rep.get(key) or {}).get(field, 0.0)


def res(rep, key):
    return (rep.get("resources") or {}).get(key, 0.0)


same = []
for field in ("rate", "sessions", "pool_replicas", "policy"):
    a, b = off["config"].get(field), on["config"].get(field)
    if a != b:
        same.append(f"{field}: {a} != {b}")
if off["config"].get("mix") != on["config"].get("mix"):
    same.append("mix differs")
if same:
    print("WARNING: the two runs are not the same workload:")
    for line in same:
        print(f"  - {line}")
    print()

rows = [
    ("Sessions planned", str(off.get("planned_sessions")), str(on.get("planned_sessions"))),
    ("Sessions recorded", str(off.get("stored_sessions")), str(on.get("stored_sessions"))),
    ("Outcomes", json.dumps(off.get("outcomes")), json.dumps(on.get("outcomes"))),
    (
        "Startup p50/p95 (ms)",
        f'{p(off, "acquire_ms", "p50"):.0f} / {p(off, "acquire_ms", "p95"):.0f}',
        f'{p(on, "acquire_ms", "p50"):.0f} / {p(on, "acquire_ms", "p95"):.0f}',
    ),
    (
        "Queue wait p50/p95 (ms)",
        f'{p(off, "queue_wait_ms", "p50"):.0f} / {p(off, "queue_wait_ms", "p95"):.0f}',
        f'{p(on, "queue_wait_ms", "p50"):.0f} / {p(on, "queue_wait_ms", "p95"):.0f}',
    ),
    (
        "Execution p50/p95 (ms)",
        f'{p(off, "hold_ms", "p50"):.0f} / {p(off, "hold_ms", "p95"):.0f}',
        f'{p(on, "hold_ms", "p50"):.0f} / {p(on, "hold_ms", "p95"):.0f}',
    ),
    (
        "Peak RAM p50/max (MiB)",
        f'{res(off, "mem_peak_p50_mib"):.0f} / {res(off, "mem_peak_max_mib"):.0f}',
        f'{res(on, "mem_peak_p50_mib"):.0f} / {res(on, "mem_peak_max_mib"):.0f}',
    ),
    (
        "Swap peak p50/max (MiB)",
        f'{res(off, "swap_peak_p50_mib"):.0f} / {res(off, "swap_peak_max_mib"):.0f}',
        f'{res(on, "swap_peak_p50_mib"):.0f} / {res(on, "swap_peak_max_mib"):.0f}',
    ),
    ("Throttled sessions", str(int(res(off, "throttled_sessions"))), str(int(res(on, "throttled_sessions")))),
    ("OOM kills", str(int(res(off, "oom_kills"))), str(int(res(on, "oom_kills")))),
    ("Sessions with samples", str(int(res(off, "sessions_with_samples"))), str(int(res(on, "sessions_with_samples")))),
    ("Sessions/min", f"{rate(off):.2f}", f"{rate(on):.2f}"),
    ("Run window (min)", f"{dur_min(off):.1f}", f"{dur_min(on):.1f}"),
    (
        "Node min MemAvailable (MiB)",
        f'{(off.get("host_memory") or {}).get("min_available_mib", 0):.0f}',
        f'{(on.get("host_memory") or {}).get("min_available_mib", 0):.0f}',
    ),
    (
        "Node swap used max (MiB)",
        f'{(off.get("host_memory") or {}).get("swap_used_mib", 0):.0f}',
        f'{(on.get("host_memory") or {}).get("swap_used_mib", 0):.0f}',
    ),
]

print("| Metric | NoSwap (run A) | LimitedSwap (run B) |")
print("|---|---:|---:|")
for name, a, b in rows:
    print(f"| {name} | {a} | {b} |")

print()
print(f"A: {off.get('run_id')}  B: {on.get('run_id')}")


def warnings(rep, label):
    out = []
    if not rep.get("resources", {}).get("sessions_with_samples"):
        out.append(f"{label}: no per-session cgroup samples — the resource and swap rows are empty")
    if label == "B" and not rep.get("resources", {}).get("swap_peak_max_mib"):
        out.append("B: swap peak is zero — check verify-swap.sh output for run B before reading this table")
    return out


notes = warnings(off, "A") + warnings(on, "B")
if notes:
    print()
    for note in notes:
        print(f"NOTE: {note}")
PY

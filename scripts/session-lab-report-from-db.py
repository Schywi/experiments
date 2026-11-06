#!/usr/bin/env python3
"""Rebuild a session-lab run report from its SQLite database, offline.

Why this exists: `report.Build` writes the JSON only when `Runner.Run` returns,
so a run killed by `go test -timeout` (or a panic, or SIGKILL) leaves the
database behind and no report. Everything needed is in the rows, including the
ones still sitting in the write-ahead log.

The script copies the database (plus `-wal` and `-shm`) into a temporary
directory and opens the copy read-write, so SQLite recovers the WAL there. The
original files are never opened, never locked, and never checkpointed: this is
safe to run against a live experiment.

The output mirrors the JSON that `apps/session-lab/internal/runtime/report`
emits, field for field, so a recovered run reads like a normal one. Two
differences are marked instead of hidden:

  * `run_facts_source` says whether the run's own totals (peak waiting, peak in
    flight, arrival window, node CPU) came from the `runs.runtime` column, or had
    to be recomputed from rows because the run never reached `EndRun`. A
    recomputed peak-waiting is a row-level approximation, not the runner's count.
  * `raw` carries the tables verbatim. A debugging artifact that has been
    summarised is useless for finding out which goroutine was where.

Usage:
  scripts/session-lab-report-from-db.py docs/session-lab-runs/riot-X.db
  scripts/session-lab-report-from-db.py riot-X.db -o /tmp/riot-X.json --no-raw
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sqlite3
import sys
import tempfile
from datetime import datetime, timedelta, timezone

# The runs in this repository are recorded in local time (America/Sao_Paulo is
# UTC-3); nothing here depends on it, it only makes the raw timestamps readable.
LOCAL = timezone(timedelta(hours=-3))

TIER_UNKNOWN = "-"


# --- statistics, matching report.go -----------------------------------------


def percentile(values: list[float], p: float) -> float:
    """Nearest-rank percentile, deliberately identical to report.percentile."""
    if not values:
        return 0.0
    index = int(float(len(values) - 1) * p / 100)
    index = max(0, min(index, len(values) - 1))
    return values[index]


def summarise(values: list[float]) -> dict:
    """The Latency struct: count, p50, p95, p99, max. Sorts in place, like Go."""
    if not values:
        return {"count": 0, "p50": 0.0, "p95": 0.0, "p99": 0.0, "max": 0.0}
    values.sort()
    return {
        "count": len(values),
        "p50": percentile(values, 50),
        "p95": percentile(values, 95),
        "p99": percentile(values, 99),
        "max": values[-1],
    }


def ms(nanos: int) -> float:
    return float(nanos) / 1e6


def mib(value_bytes: int) -> float:
    return float(value_bytes) / (1 << 20)


def peak_concurrency(sessions: list[dict]) -> dict:
    """Measured concurrency from the rows: [ready, hold_end], like the Go code."""
    edges: list[tuple[int, int]] = []
    busy = 0
    first = last = 0
    for s in sessions:
        start, end = s["ready_ns"], s["hold_end_ns"]
        if not start or not end or end <= start:
            continue
        edges.append((start, 1))
        edges.append((end, -1))
        busy += end - start
        first = start if first == 0 or start < first else first
        last = end if end > last else last
    if not edges:
        return {"mean": 0.0, "peak": 0}
    edges.sort(key=lambda e: (e[0], e[1]))  # -1 before +1 at the same instant
    current = peak = 0
    for _, delta in edges:
        current += delta
        peak = max(peak, current)
    mean = busy / (last - first) if last > first else 0.0
    return {"mean": mean, "peak": peak}


def allowance_label(value: int) -> str:
    if value < 0:
        return "max"
    if value == 0:
        return "0"
    return f"{mib(value):.0f} MiB"


def build_groups(sessions: list[dict], instances: list[dict]) -> list[dict]:
    by_id = {i["id"]: i for i in instances}
    buckets: dict[tuple[str, str, str], dict] = {}
    for s in sessions:
        instance = by_id.get(s["instance"])
        klass = instance["class"] if instance else ""
        tier = instance["swap_tier"] if instance else ""
        klass = klass or "unknown"
        tier = tier or TIER_UNKNOWN
        key = (klass, s["pool"], tier)
        entry = buckets.setdefault(
            key,
            {"group": {"class": klass, "pool": s["pool"], "tier": tier,
                       "sessions": 0, "sessions_swapped": 0},
             "swap": [], "cpu": [], "queue": [], "allowance": 0},
        )
        group = entry["group"]
        group["sessions"] += 1
        if s["outcome"] != "success":
            group["failed"] = group.get("failed", 0) + 1
        if s["swap_peak_bytes"] > 0:
            group["sessions_swapped"] += 1
            entry["swap"].append(mib(s["swap_peak_bytes"]))
        if s["swap_allowance_bytes"] < 0:
            entry["allowance"] = -1
        elif entry["allowance"] >= 0 and s["swap_allowance_bytes"] > entry["allowance"]:
            entry["allowance"] = s["swap_allowance_bytes"]
        if s["cpu_usec"] > 0:
            entry["cpu"].append(s["cpu_usec"] / 1e6)
        if s["claim_created_ns"] > 0 and s["ready_ns"] > s["claim_created_ns"]:
            entry["queue"].append(ms(s["ready_ns"] - s["claim_created_ns"]))

    groups = []
    for entry in buckets.values():
        group = entry["group"]
        group.setdefault("failed", 0)
        group["swap_allowance"] = allowance_label(entry["allowance"])
        if entry["swap"]:
            group["swap_p95_mib"] = percentile(sorted(entry["swap"]), 95)
            group["swap_max_mib"] = percentile(sorted(entry["swap"]), 100)
        if entry["cpu"]:
            group["cpu_core_seconds_p50"] = percentile(sorted(entry["cpu"]), 50)
        if entry["queue"]:
            group["queue_p95_ms"] = percentile(sorted(entry["queue"]), 95)
        groups.append(group)
    groups.sort(key=lambda g: (g["class"], g["pool"], g["tier"]))
    return groups


def recommend(rep: dict) -> str:
    if rep.get("first_boundary"):
        return ("repeat with a lower arrival rate to confirm the boundary is repeatable, "
                "then re-run at this rate after changing the constraint above")
    if rep["concurrency"]["peak"] == 0:
        return ("no session recorded a concurrency sample; check that the sampler found the "
                "pod cgroups before trusting the resource numbers")
    if rep["resources"]["throttled_sessions"] > 0:
        return ("raise the CPU limit on the sandbox template, or remove it, and repeat: "
                "throttled sessions make hold latency an artifact of the limit")
    if rep["outcomes"].get("waiting", 0) > 0:
        return ("sessions were still waiting at the end: extend the run or raise the pool "
                "replica count and repeat")
    return "raise --overload-factor and repeat until the platform refuses, which is where the boundary is"


# --- reading the database ---------------------------------------------------


def open_recovered(db_path: str, workdir: str) -> tuple[sqlite3.Connection, dict]:
    """Copy db + WAL + shm into workdir and open the copy, recovering the WAL."""
    base = os.path.basename(db_path)
    target = os.path.join(workdir, base)
    shutil.copy2(db_path, target)
    facts = {"db": db_path, "db_bytes": os.path.getsize(db_path),
             "wal_bytes": 0, "shm_copied": False}
    for suffix in ("-wal", "-shm"):
        source = db_path + suffix
        if os.path.exists(source):
            shutil.copy2(source, target + suffix)
            if suffix == "-wal":
                facts["wal_bytes"] = os.path.getsize(source)
            else:
                facts["shm_copied"] = True
    # Read-write on the copy only: SQLite replays the WAL into the copy's own
    # pages, which is what makes the un-checkpointed rows visible.
    conn = sqlite3.connect(target)
    conn.row_factory = sqlite3.Row
    facts["journal_mode"] = conn.execute("pragma journal_mode").fetchone()[0]
    return conn, facts


def rows(conn: sqlite3.Connection, table: str, order: str = "rowid") -> list[dict]:
    cursor = conn.execute(f"select * from {table} order by {order}")
    return [dict(row) for row in cursor.fetchall()]


def iso(nanos: int) -> str | None:
    if not nanos:
        return None
    return datetime.fromtimestamp(nanos / 1e9, LOCAL).isoformat()


# --- the report -------------------------------------------------------------


def build(db_path: str, with_raw: bool = True) -> dict:
    workdir = tempfile.mkdtemp(prefix="session-lab-db-")
    try:
        conn, recovery = open_recovered(db_path, workdir)
        try:
            run = conn.execute("select * from runs limit 1").fetchone()
            if run is None:
                raise SystemExit(f"{db_path}: no run row; is this a session-lab database?")
            run = dict(run)
            sessions = rows(conn, "sessions", "id")
            instances = rows(conn, "instances", "id")
            events = rows(conn, "events", "seq")
            samples = rows(conn, "host_samples", "seq")
        finally:
            conn.close()
    finally:
        shutil.rmtree(workdir, ignore_errors=True)

    cfg = json.loads(run["config"]) if run["config"] else {}
    planned = int(cfg.get("sessions") or 0)
    mix = cfg.get("mix") or []
    if mix:
        planned = sum(int(m.get("count") or 0) for m in mix)
        total_ns = sum(int(m.get("hold_ns") or 0) * int(m.get("count") or 0) for m in mix)
        mean_hold_s = (total_ns / planned) / 1e9 if planned else 0.0
    else:
        mean_hold_s = 0.0
    rate = float(cfg.get("rate") or 0)
    # schedule.Plan.PeakOffered: Little's law, clamped to the sessions that exist.
    offered = min(rate * mean_hold_s, float(planned))

    rep: dict = {
        "run_id": run["run_id"],
        "config": cfg,
        "started_ns": run["started_ns"] or 0,
        "ended_ns": run["ended_ns"] or 0,
        "planned_sessions": planned,
        "stored_sessions": len(sessions),
        "outcomes": {},
        "arrival_rate": rate,
        "offered_concurrency": offered,
    }

    admission_ms: list[float] = []
    queue_ms: list[float] = []
    acquire_ms: list[float] = []
    hold_ms: list[float] = []
    reset_ms: list[float] = []
    release_ms: list[float] = []
    resume_ms: list[float] = []
    task_ms: list[float] = []
    script_ms: list[float] = []
    heap_mib: list[float] = []
    cpu_cores: list[float] = []
    mem_mib: list[float] = []
    mem_peak_mib: list[float] = []
    work_set_mib: list[float] = []
    swap_peak_mib: list[float] = []
    acquire_by_launch: dict[str, list[float]] = {}
    launch_types: dict[str, int] = {}
    buckets: dict[str, dict] = {}
    resources = {"sessions_with_samples": 0, "cpu_core_seconds_total": 0.0,
                 "cpu_core_seconds_p50": 0.0, "cpu_core_seconds_max": 0.0,
                 "mem_peak_p50_mib": 0.0, "mem_peak_max_mib": 0.0,
                 "mem_working_set_peak_max_mib": 0.0, "throttled_sessions": 0,
                 "oom_kills": 0, "refault_sessions": 0}
    ok = failures = 0
    shared = 0
    boundary_ev: list[str] = []

    for s in sessions:
        outcome = s["outcome"]
        rep["outcomes"][outcome] = rep["outcomes"].get(outcome, 0) + 1
        bucket = buckets.setdefault(s["bucket"], {"sessions": 0, "ok": 0, "failed": 0,
                                                  "queue_p50_ms": 0.0, "hold_p50_ms": 0.0})
        bucket["sessions"] += 1
        if outcome == "success":
            bucket["ok"] += 1
            ok += 1
        elif outcome in ("waiting", "pending"):
            pass
        else:
            bucket["failed"] += 1
            failures += 1

        if s["claim_created_ns"] > 0 and s["ready_ns"] > 0:
            queue_ms.append(ms(s["ready_ns"] - s["claim_created_ns"]))
            acquire_ms.append(ms(s["ready_ns"] - s["arrival_ns"]))
        if s["hold_end_ns"] > 0 and s["hold_start_ns"] > 0:
            hold_ms.append(ms(s["hold_end_ns"] - s["hold_start_ns"]))
        if s["resume_end_ns"] > 0 and s["resume_start_ns"] > 0:
            resume_ms.append(ms(s["resume_end_ns"] - s["resume_start_ns"]))
        if s["reset_end_ns"] > 0 and s["reset_start_ns"] > 0:
            reset_ms.append(ms(s["reset_end_ns"] - s["reset_start_ns"]))
        if s["gone_ns"] > 0 and s["delete_called_ns"] > 0:
            release_ms.append(ms(s["gone_ns"] - s["delete_called_ns"]))
        if s["task_duration_us"] > 0:
            task_ms.append(s["task_duration_us"] / 1000)
        if s["script_duration_us"] > 0:
            script_ms.append(s["script_duration_us"] / 1000)
        if s["js_heap_bytes"] > 0:
            heap_mib.append(mib(s["js_heap_bytes"]))
        if s["cpu_usec"] > 0:
            cpu_cores.append(s["cpu_usec"] / 1e6)
        if s["mem_peak_bytes"] > 0:
            mem_mib.append(mib(s["mem_peak_bytes"]))
        if s["cpu_shared"]:
            shared += 1
        if s["admission_wait_ns"] > 0:
            admission_ms.append(ms(s["admission_wait_ns"]))
        if s["launch_type"]:
            launch_types[s["launch_type"]] = launch_types.get(s["launch_type"], 0) + 1
            if s["ready_ns"] > 0 and s["arrival_ns"] > 0:
                acquire_by_launch.setdefault(s["launch_type"], []).append(
                    ms(s["ready_ns"] - s["arrival_ns"]))
        if s["samples"] > 0:
            resources["sessions_with_samples"] += 1
            resources["cpu_core_seconds_total"] += s["cpu_usec"] / 1e6
            if s["throttled_usec"] > 0:
                resources["throttled_sessions"] += 1
            if s["oom_kill"] > 0:
                resources["oom_kills"] += 1
            if s["workingset_refault_anon"] > 0 or s["workingset_refault_file"] > 0:
                resources["refault_sessions"] += 1
            mem_peak_mib.append(mib(s["mem_peak_bytes"]))
            work_set_mib.append(mib(s["mem_working_set_peak_bytes"]))
            swap_peak_mib.append(mib(s["swap_peak_bytes"]))
        if s["oom_kill"] > 0:
            boundary_ev.append(f"session {s['id']}: oom_kill={s['oom_kill']}")
        if s["throttled_usec"] > 0:
            boundary_ev.append(f"session {s['id']}: throttled_usec={s['throttled_usec']} (cpu limit reached)")

    for bucket_name, bucket in buckets.items():
        bucket_queue = [ms(s["ready_ns"] - s["claim_created_ns"]) for s in sessions
                        if s["bucket"] == bucket_name and s["claim_created_ns"] > 0
                        and s["ready_ns"] > s["claim_created_ns"]]
        bucket_hold = [ms(s["hold_end_ns"] - s["hold_start_ns"]) for s in sessions
                       if s["bucket"] == bucket_name and s["hold_end_ns"] > 0
                       and s["hold_start_ns"] > 0]
        bucket["queue_p50_ms"] = percentile(sorted(bucket_queue), 50) if bucket_queue else 0.0
        bucket["hold_p50_ms"] = percentile(sorted(bucket_hold), 50) if bucket_hold else 0.0

    if cpu_cores:
        resources["cpu_core_seconds_p50"] = percentile(sorted(cpu_cores), 50)
        resources["cpu_core_seconds_max"] = max(cpu_cores)
    if mem_peak_mib:
        resources["mem_peak_p50_mib"] = percentile(sorted(mem_peak_mib), 50)
        resources["mem_peak_max_mib"] = max(mem_peak_mib)
    if work_set_mib:
        resources["mem_working_set_peak_max_mib"] = max(work_set_mib)
    if swap_peak_mib:
        resources["swap_peak_p50_mib"] = percentile(sorted(swap_peak_mib), 50)
        resources["swap_peak_max_mib"] = max(swap_peak_mib)

    rep["concurrency"] = peak_concurrency(sessions)
    rep["queue_wait_ms"] = summarise(queue_ms)
    rep["acquire_ms"] = summarise(acquire_ms)
    rep["hold_ms"] = summarise(hold_ms)
    rep["reset_ms"] = summarise(reset_ms)
    rep["release_ms"] = summarise(release_ms)
    rep["resume_ms"] = summarise(resume_ms)
    rep["admission_ms"] = summarise(admission_ms)
    rep["launch_types"] = launch_types
    rep["buckets"] = buckets
    rep["resources"] = resources
    rep["per_session"] = {
        "task_ms": summarise(task_ms),
        "script_ms": summarise(script_ms),
        "js_heap_mib": summarise(heap_mib),
        "cpu_core_seconds": summarise(cpu_cores),
        "mem_mib": summarise(mem_mib),
    }
    if acquire_by_launch:
        rep["acquire_by_launch_type"] = {k: summarise(v) for k, v in acquire_by_launch.items()}
    rep["shared_sessions"] = shared
    rep["active_at_cutoff"] = rep["outcomes"].get("waiting", 0) + rep["outcomes"].get("pending", 0)

    # Instances.
    total_sessions = sum(i["sessions"] for i in instances)
    boot_peaks = sorted(i["boot_cpu_peak_cores"] for i in instances if i["boot_cpu_peak_cores"] > 0)
    rep["instances"] = {
        "total": len(instances),
        "max_sessions_per_instance": max((i["sessions"] for i in instances), default=0),
        "mean_sessions_per_instance": (total_sessions / len(instances)) if instances else 0.0,
        "rebuild_p50_ms": 0.0,
        "boot_cpu_peak_p50": percentile(boot_peaks, 50) if boot_peaks else 0.0,
        "boot_cpu_peak_max": boot_peaks[-1] if boot_peaks else 0.0,
    }

    # Host memory.
    host = {"samples": len(samples), "total_mib": 0.0, "min_available_mib": 0.0,
            "min_ratio_pct": 0.0, "swap_used_mib": 0.0}
    if samples:
        host["total_mib"] = mib(samples[0]["mem_total"])
        min_avail, min_at = 0.0, 0
        for sample in samples:
            avail = mib(sample["mem_available"])
            if min_avail == 0.0 or avail < min_avail:
                min_avail, min_at = avail, sample["at_ns"]
            used = mib(sample["swap_total"] - sample["swap_free"])
            host["swap_used_mib"] = max(host["swap_used_mib"], used)
        host["min_available_mib"] = min_avail
        host["min_at_ns"] = min_at
        if host["total_mib"]:
            host["min_ratio_pct"] = min_avail / host["total_mib"] * 100
    rep["host_memory"] = host

    # Template limits, as the run stored them.
    if run["limits"]:
        stored = json.loads(run["limits"])
        rep["template_limits"] = {
            "container": stored.get("container", ""),
            "cpu_request_milli": stored.get("cpu_request_milli", 0),
            "cpu_limit_milli": stored.get("cpu_limit_milli", 0),
            "mem_request_mib": mib(stored.get("mem_request_bytes", 0)),
            "mem_limit_mib": mib(stored.get("mem_limit_bytes", 0)),
        }
    if run["autoscaler"]:
        rep["autoscaler"] = json.loads(run["autoscaler"])
    if run["replicas"]:
        replicas = json.loads(run["replicas"])
        if replicas:
            rep["replicas"] = replicas

    rep["groups"] = build_groups(sessions, instances)

    # Run-level facts. The runner stores these in EndRun; a killed run has none,
    # so they are recomputed from rows and the substitution is recorded.
    facts = json.loads(run["runtime"]) if run["runtime"] else {}
    rep["run_facts_source"] = "runs.runtime (EndRun)" if facts else "recomputed from rows (run never reached EndRun)"
    arrivals = [s["arrival_ns"] for s in sessions if s["arrival_ns"]]
    rep["arrival_window_s"] = (float(facts["arrival_window_ns"]) / 1e9) if facts.get("arrival_window_ns") else (
        (max(arrivals) - min(arrivals)) / 1e9 if len(arrivals) > 1 else 0.0)
    rep["peak_waiting_sessions"] = facts.get("peak_waiting", 0)
    rep["peak_in_flight_sessions"] = facts.get("peak_in_flight", 0)
    rep["cleanup_failures"] = facts.get("cleanup_failures", 0)
    rep["node_cpu"] = {
        "total_cores": facts.get("node_cpu_total_cores", 0),
        "peak_cores": facts.get("node_cpu_peak_cores", 0.0),
        "mean_cores": (facts["node_cpu_sum_cores"] / facts["node_cpu_samples"])
        if facts.get("node_cpu_samples") else 0.0,
        "samples": facts.get("node_cpu_samples", 0),
    }

    # The boundary: the first session the platform refused, in row order.
    boundary_ev = []
    for s in sessions:
        if s["outcome"] in ("acquisition_failed", "timeout", "reset_failed", "destroy_failed",
                            "lost_unknown"):
            if "first_boundary" not in rep:
                rep["first_boundary"] = f"{s['outcome']} at session {s['id']}: {s['error']}"
                boundary_ev.append(f"bucket {s['bucket']}, instance {s['instance']}")
        if s["oom_kill"] > 0:
            boundary_ev.append(f"session {s['id']}: oom_kill={s['oom_kill']}")
    if boundary_ev:
        rep["boundary_evidence"] = [e for e in boundary_ev if not e.startswith("bucket ")] + \
                                   [e for e in boundary_ev if e.startswith("bucket ")]
    rep["elasticity"] = ("exceeded: see first_boundary" if rep.get("first_boundary")
                         else "not tested: the workload did not exceed what the platform served")

    notes = []
    if ok + failures + rep["outcomes"].get("waiting", 0) + rep["outcomes"].get("pending", 0) != planned:
        notes.append(f"outcome counts ({ok + failures}) do not sum to the planned sessions ({planned})")
    if len(sessions) != planned:
        notes.append(f"stored sessions ({len(sessions)}) differ from planned ({planned})")
    if run["ended_ns"] == 0:
        notes.append("runs.ended_ns is 0: the run was killed before EndRun, so this report is a "
                     "reconstruction from the rows written up to the kill")
    if notes:
        rep["invariant_notes"] = notes
    rep["next"] = recommend(rep)
    rep["rebuild"] = {
        "tool": "scripts/session-lab-report-from-db.py",
        "recovered": recovery,
        "started_iso": iso(run["started_ns"]),
        "ended_iso": iso(run["ended_ns"]),
        "sessions_without_instance": sum(1 for s in sessions if not s["instance"]),
        "sessions_without_hold": sum(1 for s in sessions if not s["hold_start_ns"]),
        "last_event_iso": iso(max((e["at_ns"] for e in events), default=0)),
    }

    if with_raw:
        rep["raw"] = {
            "sessions": sessions,
            "instances": instances,
            "events": [{**e, "at_iso": iso(e["at_ns"])} for e in events],
            "host_samples": samples,
            "run": {k: (json.loads(run[k]) if k in ("config", "replicas", "limits", "runtime")
                        and run[k] else run[k])
                    for k in run},
        }
    return rep


def match_go_numbers(value):
    """Render integral floats as ints, the way encoding/json does.

    Go writes `2` for float64(2) and `0` for 0.0; Python writes `2.0`. The point
    of this script is a diffable substitute for the real report, so the two must
    not differ on formatting alone.
    """
    if isinstance(value, float) and value.is_integer() and abs(value) < 1e15:
        return int(value)
    if isinstance(value, dict):
        return {k: match_go_numbers(v) for k, v in value.items()}
    if isinstance(value, list):
        return [match_go_numbers(v) for v in value]
    return value


def summarise_stdout(rep: dict) -> None:
    print(f"{rep['run_id']}: {rep['stored_sessions']} stored / {rep['planned_sessions']} planned, "
          f"offered {rep['offered_concurrency']:.0f}, facts from {rep['run_facts_source']}")
    print(f"  outcomes       {rep['outcomes']}")
    print(f"  acquire_ms     p50 {rep['acquire_ms']['p50']:.0f}  p95 {rep['acquire_ms']['p95']:.0f}  "
          f"n={rep['acquire_ms']['count']}")
    print(f"  queue_wait_ms  p50 {rep['queue_wait_ms']['p50']:.1f}  p95 {rep['queue_wait_ms']['p95']:.0f}  "
          f"n={rep['queue_wait_ms']['count']}")
    print(f"  hold_ms        p50 {rep['hold_ms']['p50']:.0f}  n={rep['hold_ms']['count']}")
    print(f"  concurrency    peak {rep['concurrency']['peak']}  mean {rep['concurrency']['mean']:.1f}")
    print(f"  instances      {rep['instances']['total']}  max/instance "
          f"{rep['instances']['max_sessions_per_instance']}")
    print(f"  never started  {rep['rebuild']['sessions_without_instance']} sessions had no instance; "
          f"last event {rep['rebuild']['last_event_iso']}")
    if rep.get("first_boundary"):
        print(f"  first boundary {rep['first_boundary']}")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("db", help="path to riot-*.db (the -wal and -shm are picked up automatically)")
    parser.add_argument("-o", "--out", help="write the JSON here (default: alongside the db, as riot-*.json)")
    parser.add_argument("--no-raw", action="store_true", help="omit the raw tables, keep only the report")
    args = parser.parse_args(argv)

    if not os.path.exists(args.db):
        print(f"no such database: {args.db}", file=sys.stderr)
        return 2
    rep = build(args.db, with_raw=not args.no_raw)
    rep = match_go_numbers(rep)
    out = args.out or os.path.splitext(args.db)[0] + ".json"
    if os.path.exists(out):
        print(f"{out} exists; refusing to overwrite it (pass -o to choose another path)", file=sys.stderr)
        return 2
    with open(out, "w", encoding="utf-8") as handle:
        json.dump(rep, handle, indent=2, sort_keys=False)
        handle.write("\n")
    summarise_stdout(rep)
    print(f"  report         {out} ({os.path.getsize(out)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

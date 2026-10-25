"""Timings must survive a restart, so they live in a file, not a dict.

Each test points records at a fresh SQLite file: the module caches one connection
for the process, so the cache is reset alongside the path.
"""

import records


def fresh_db(monkeypatch, tmp_path):
    monkeypatch.setattr(records, "DB_PATH", str(tmp_path / "console.db"))
    monkeypatch.setattr(records, "_connection", None)


def test_a_timing_round_trips(monkeypatch, tmp_path):
    fresh_db(monkeypatch, tmp_path)
    records.remember_timings(
        "browser-pool-2fngr",
        kind="claim",
        claim="demo-2",
        timers={"t_sched_ms": 147, "t_ready_ms": 123, "t_cdp_ms": 1005, "t_paint_ms": 2675},
    )
    row = records.timings_for("browser-pool-2fngr")
    assert row["t_ready_ms"] == 123
    assert row["t_sched_ms"] == 147
    assert row["claim"] == "demo-2"
    assert row["kind"] == "claim"


def test_a_later_write_keeps_fields_it_did_not_set(monkeypatch, tmp_path):
    fresh_db(monkeypatch, tmp_path)
    records.remember_timings("sb", kind="claim", timers={"t_sched_ms": 100})
    records.remember_timings("sb", kind="claim", timers={"t_paint_ms": 900})
    row = records.timings_for("sb")
    assert row["t_sched_ms"] == 100
    assert row["t_paint_ms"] == 900


def test_an_unknown_sandbox_has_no_row(monkeypatch, tmp_path):
    fresh_db(monkeypatch, tmp_path)
    assert records.timings_for("never-created-by-us") is None


def test_compare_runs_round_trip(monkeypatch, tmp_path):
    fresh_db(monkeypatch, tmp_path)
    records.remember_run(
        {"started_at": 1.0, "arms": [{"label": "THIS SANDBOX", "timers": {"t_ready_ms": 500}}]}
    )
    rows = records.recent_runs(3)
    assert rows[0]["arms"][0]["timers"]["t_ready_ms"] == 500


def test_an_unwritable_path_falls_back_to_memory(monkeypatch):
    monkeypatch.setattr(records, "DB_PATH", "/proc/nope/console.db")
    monkeypatch.setattr(records, "_connection", None)
    # The pane keeps working; it just forgets. `durable()` is what says so.
    records.remember_timings("sb", kind="claim", timers={"t_ready_ms": 1})
    assert records.timings_for("sb")["t_ready_ms"] == 1
    assert records.durable() is False

"""The run wall: reading a session-lab database and drawing it.

The databases here are written by the fixture builder, with the same schema
`apps/session-lab/internal/platform/store/store.go` creates, so a change to the
runner's columns fails in one place.
"""

import json
import sqlite3

import pytest
from fastapi.testclient import TestClient

import config
import fixtures
import kube
import render_runs
import runs
import server

BASE = 1_700_000_000 * 1_000_000_000

SCHEMA = """
CREATE TABLE runs (
    run_id      TEXT PRIMARY KEY,
    config      TEXT NOT NULL,
    started_ns  INTEGER NOT NULL,
    ended_ns    INTEGER,
    replicas    TEXT NOT NULL DEFAULT '[]',
    limits      TEXT NOT NULL DEFAULT '',
    autoscaler  TEXT NOT NULL DEFAULT '',
    runtime     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE sessions (
    id             INTEGER PRIMARY KEY,
    bucket         TEXT NOT NULL,
    policy         TEXT NOT NULL,
    namespace      TEXT NOT NULL,
    template       TEXT NOT NULL,
    pool           TEXT NOT NULL,
    instance       TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL DEFAULT 'pending',
    error          TEXT NOT NULL DEFAULT '',
    arrival_ns     INTEGER NOT NULL,
    claim_created_ns INTEGER NOT NULL DEFAULT 0,
    ready_ns       INTEGER NOT NULL DEFAULT 0,
    hold_start_ns  INTEGER NOT NULL DEFAULT 0,
    hold_end_ns    INTEGER NOT NULL DEFAULT 0,
    delete_called_ns INTEGER NOT NULL DEFAULT 0,
    gone_ns        INTEGER NOT NULL DEFAULT 0,
    reset_start_ns INTEGER NOT NULL DEFAULT 0,
    reset_end_ns   INTEGER NOT NULL DEFAULT 0,
    rebuild_start_ns INTEGER NOT NULL DEFAULT 0,
    rebuild_end_ns INTEGER NOT NULL DEFAULT 0,
    resume_start_ns INTEGER NOT NULL DEFAULT 0,
    resume_end_ns  INTEGER NOT NULL DEFAULT 0,
    launch_type    TEXT NOT NULL DEFAULT '',
    node           TEXT NOT NULL DEFAULT '',
    pod_uid        TEXT NOT NULL DEFAULT '',
    cpu_usec       INTEGER NOT NULL DEFAULT 0,
    cpu_pressure_some_usec INTEGER NOT NULL DEFAULT 0,
    cpu_pressure_full_usec INTEGER NOT NULL DEFAULT 0,
    throttled_usec INTEGER NOT NULL DEFAULT 0,
    nr_throttled   INTEGER NOT NULL DEFAULT 0,
    mem_peak_bytes INTEGER NOT NULL DEFAULT 0,
    mem_working_set_peak_bytes INTEGER NOT NULL DEFAULT 0,
    mem_inactive_file_bytes INTEGER NOT NULL DEFAULT 0,
    shmem_peak_bytes INTEGER NOT NULL DEFAULT 0,
    anon_peak_bytes INTEGER NOT NULL DEFAULT 0,
    workingset_refault_anon INTEGER NOT NULL DEFAULT 0,
    workingset_refault_file INTEGER NOT NULL DEFAULT 0,
    swap_peak_bytes INTEGER NOT NULL DEFAULT 0,
    oom_kill       INTEGER NOT NULL DEFAULT 0,
    mem_pressure_some_usec INTEGER NOT NULL DEFAULT 0,
    samples        INTEGER NOT NULL DEFAULT 0,
    browser_context TEXT NOT NULL DEFAULT '',
    cpu_shared     INTEGER NOT NULL DEFAULT 0,
    admission_wait_ns INTEGER NOT NULL DEFAULT 0
);
"""

# A database written before the shared-browser work: no browser_context, no
# cpu_shared, no admission_wait_ns. The reader has to render it.
OLD_SCHEMA = """
CREATE TABLE runs (
    run_id      TEXT PRIMARY KEY,
    config      TEXT NOT NULL,
    started_ns  INTEGER NOT NULL,
    ended_ns    INTEGER
);
CREATE TABLE sessions (
    id             INTEGER PRIMARY KEY,
    bucket         TEXT NOT NULL,
    policy         TEXT NOT NULL,
    namespace      TEXT NOT NULL,
    template       TEXT NOT NULL,
    pool           TEXT NOT NULL,
    instance       TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL DEFAULT 'pending',
    error          TEXT NOT NULL DEFAULT '',
    arrival_ns     INTEGER NOT NULL,
    claim_created_ns INTEGER NOT NULL DEFAULT 0,
    ready_ns       INTEGER NOT NULL DEFAULT 0,
    hold_start_ns  INTEGER NOT NULL DEFAULT 0,
    hold_end_ns    INTEGER NOT NULL DEFAULT 0,
    gone_ns        INTEGER NOT NULL DEFAULT 0
);
"""

COLUMNS = [
    "id", "bucket", "policy", "namespace", "template", "pool", "instance", "outcome",
    "error", "arrival_ns", "claim_created_ns", "ready_ns", "hold_start_ns",
    "hold_end_ns", "delete_called_ns", "gone_ns", "cpu_usec", "samples",
    "browser_context", "cpu_shared", "admission_wait_ns",
]


def session(id, **overrides):
    """One session row with the fields the wall reads; times are offsets in ms."""
    row = {
        "id": id,
        "bucket": "30s",
        "policy": "reuse",
        "namespace": "sandbox-demo",
        "template": "browser-tpl",
        "pool": "browser-pool",
        "instance": "browser-7k2",
        "outcome": "success",
        "error": "",
        "arrival_ns": BASE,
        "claim_created_ns": BASE + 100_000_000,
        "ready_ns": BASE + 1_200_000_000,
        "hold_start_ns": BASE + 1_250_000_000,
        "hold_end_ns": BASE + 31_250_000_000,
        "delete_called_ns": BASE + 31_300_000_000,
        "gone_ns": BASE + 31_400_000_000,
        "cpu_usec": 1_840_000,
        "samples": 12,
        "browser_context": "",
        "cpu_shared": 0,
        "admission_wait_ns": 0,
    }
    row.update(overrides)
    return row


def make_db(directory, run_id="run-a", rows=None, *, config_overrides=None, started=BASE,
            ended=BASE + 60_000_000_000, schema=SCHEMA):
    """Write one run database and return its path."""
    path = directory / f"{run_id}.db"
    connection = sqlite3.connect(path)
    connection.executescript(schema)
    run_config = {"run_id": run_id, "sessions": len(rows or []), "policy": "reuse"}
    run_config.update(config_overrides or {})
    connection.execute(
        "INSERT INTO runs (run_id, config, started_ns, ended_ns) VALUES (?, ?, ?, ?)",
        (run_id, json.dumps(run_config), started, ended),
    )
    present = [column for column in COLUMNS if column in _columns(connection)]
    placeholders = ", ".join("?" for _ in present)
    for row in rows or []:
        values = [row.get(column) for column in present]
        connection.execute(
            f"INSERT INTO sessions ({', '.join(present)}) VALUES ({placeholders})", values
        )
    connection.commit()
    connection.close()
    return path


def _columns(connection):
    return {row[1] for row in connection.execute("PRAGMA table_info(sessions)")}


@pytest.fixture
def runs_dir(tmp_path, monkeypatch):
    monkeypatch.setattr(config, "RUNS_DIR", tmp_path)
    return tmp_path


# --------------------------------------------------------------------------- #
# The reader
# --------------------------------------------------------------------------- #
def test_discover_lists_newest_first_and_ignores_other_files(runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)], started=BASE)
    make_db(runs_dir, "20260102T000000Z", [session(1), session(2)], started=BASE + 10**12)
    (runs_dir / "notes.txt").write_text("not a run")
    (runs_dir / "sub").mkdir()
    refs = runs.discover(runs_dir)
    assert [ref.run_id for ref in refs] == ["20260102T000000Z", "20260101T000000Z"]
    assert refs[0].stored == 2
    assert refs[0].instances == 1
    assert refs[0].has_report is False
    assert refs[0].error is None


def test_discover_of_a_missing_directory_is_empty(tmp_path):
    assert runs.discover(tmp_path / "absent") == []


def test_load_refuses_unknown_and_unsafe_ids(runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)])
    for bad in ("nope", "", "../20260101T000000Z", "20260101T000000Z/../x"):
        with pytest.raises(runs.RunNotFound):
            runs.load(runs_dir, bad)


def test_old_database_without_the_new_columns_still_loads(runs_dir):
    make_db(runs_dir, "old", [session(1, browser_context=None)], schema=OLD_SCHEMA)
    run = runs.load(runs_dir, "old")
    assert run.error is None
    assert len(run.sessions) == 1
    assert run.sessions[0].browser_context == ""
    assert run.sessions[0].cpu_shared == 0


def test_a_waiting_outcome_is_a_hole_and_a_pending_one_still_waits(runs_dir):
    make_db(
        runs_dir,
        "cut",
        [
            session(1, outcome="waiting", instance="", claim_created_ns=0, ready_ns=0,
                    hold_start_ns=0, hold_end_ns=0, delete_called_ns=0, gone_ns=0),
            session(2, outcome="pending", instance="", claim_created_ns=0, ready_ns=0,
                    hold_start_ns=0, hold_end_ns=0, delete_called_ns=0, gone_ns=0),
        ],
    )
    run = runs.load(runs_dir, "cut")
    cut = run.ended_ns
    assert run.sessions[0].state_at(cut) == runs.HOLE
    assert run.sessions[1].state_at(cut) == runs.WAITING
    assert run.counts(cut)[runs.HOLE] == 1
    assert run.counts(cut)[runs.WAITING] == 1


def test_a_terminal_failure_outranks_the_open_hold_window(runs_dir):
    # reset_failed after Ready: ready_ns set, gone_ns never written.
    make_db(
        runs_dir,
        "fail",
        [session(1, outcome="reset_failed", gone_ns=0, delete_called_ns=0)],
    )
    run = runs.load(runs_dir, "fail")
    assert run.sessions[0].state_at(run.ended_ns) == runs.FAILED
    assert run.counts(run.ended_ns)[runs.HOLDING] == 0


def test_the_hold_window_closes_at_the_release_call_when_gone_is_missing(runs_dir):
    make_db(runs_dir, "open", [session(1, outcome="timeout", gone_ns=0)])
    run = runs.load(runs_dir, "open")
    start, end = run.sessions[0].hold_window
    assert start == BASE + 1_250_000_000
    assert end == BASE + 31_250_000_000
    assert run.sessions[0].open_ended is True


def test_slots_are_exact_when_the_runner_recorded_a_browser_context(runs_dir):
    make_db(
        runs_dir,
        "ctx",
        [
            session(1, browser_context="ctx-a", arrival_ns=BASE),
            session(2, browser_context="ctx-b", arrival_ns=BASE + 1_000_000),
            session(3, browser_context="ctx-a", arrival_ns=BASE + 80_000_000_000),
        ],
    )
    run = runs.load(runs_dir, "ctx")
    group = run.buckets[0].instances[0]
    assert group.exact is True
    assert [(slot.key, len(slot.sessions)) for slot in group.slots] == [("ctx-a", 2), ("ctx-b", 1)]


def test_overlapping_sessions_share_an_instance_but_not_a_lane(runs_dir):
    make_db(
        runs_dir,
        "lanes",
        [
            session(1, arrival_ns=BASE),
            # Starts inside session 1's hold window, so it cannot have shared it.
            session(2, arrival_ns=BASE + 10_000_000_000,
                    claim_created_ns=BASE + 10_100_000_000,
                    ready_ns=BASE + 10_200_000_000,
                    hold_start_ns=BASE + 10_250_000_000,
                    hold_end_ns=BASE + 40_000_000_000,
                    delete_called_ns=BASE + 40_100_000_000,
                    gone_ns=BASE + 40_200_000_000),
            # Starts after both released: it may have reused either lane.
            session(3, arrival_ns=BASE + 60_000_000_000,
                    claim_created_ns=BASE + 60_100_000_000,
                    ready_ns=BASE + 60_200_000_000,
                    hold_start_ns=BASE + 60_250_000_000,
                    hold_end_ns=BASE + 90_000_000_000,
                    delete_called_ns=BASE + 90_100_000_000,
                    gone_ns=BASE + 90_200_000_000),
        ],
        ended=BASE + 120_000_000_000,
    )
    run = runs.load(runs_dir, "lanes")
    group = run.buckets[0].instances[0]
    assert group.exact is False
    assert [len(slot.sessions) for slot in group.slots] == [2, 1]


def test_capacity_comes_from_the_run_config_and_never_undercuts_the_slots(runs_dir):
    make_db(
        runs_dir,
        "cap",
        [session(1, browser_context="a"), session(2, browser_context="b")],
        config_overrides={"contexts_per_browser": 8},
    )
    run = runs.load(runs_dir, "cap")
    assert run.contexts_per_browser == 8
    assert run.buckets[0].instances[0].capacity == 8

    make_db(runs_dir, "one", [session(1)])
    plain = runs.load(runs_dir, "one")
    assert plain.contexts_per_browser == 0
    assert plain.buckets[0].instances[0].capacity == 1


def test_peak_is_the_instant_the_most_slots_were_held(runs_dir):
    make_db(
        runs_dir,
        "peak",
        [
            session(1, arrival_ns=BASE),
            session(2, arrival_ns=BASE + 1_000_000),
            session(3, arrival_ns=BASE + 40_000_000_000,
                    claim_created_ns=BASE + 40_100_000_000,
                    ready_ns=BASE + 40_200_000_000,
                    hold_start_ns=BASE + 40_250_000_000,
                    hold_end_ns=BASE + 70_000_000_000,
                    delete_called_ns=BASE + 70_100_000_000,
                    gone_ns=BASE + 70_200_000_000),
        ],
        ended=BASE + 90_000_000_000,
    )
    run = runs.load(runs_dir, "peak")
    assert run.peak_concurrency == 2
    # The peak is the first hold start: that is when the second slot became busy.
    assert run.peak_ns == BASE + 1_250_000_000
    # A finished run draws its peak, because at the end it holds nothing.
    assert run.default_cut() == run.peak_ns


def test_the_skyline_keeps_the_worst_state_in_a_bucketed_cell(runs_dir):
    rows = [
        session(
            index,
            arrival_ns=BASE + index * 1_000_000,
            outcome="success" if index % 2 else "waiting",
            ready_ns=(BASE + index * 1_000_000 + 1_000_000) if index % 2 else 0,
            claim_created_ns=(BASE + index * 1_000_000 + 500_000) if index % 2 else 0,
            instance="browser-1" if index % 2 else "",
        )
        for index in range(1, 101)
    ]
    make_db(runs_dir, "many", rows, ended=BASE + 200_000_000)
    run = runs.load(runs_dir, "many")
    cells = runs.skyline(run.sessions, run.ended_ns, limit=10)
    assert len(cells) == 10
    assert sum(cell["count"] for cell in cells) == 100
    assert all(cell["state"] == runs.HOLE for cell in cells)
    assert [cell["first_id"] for cell in cells] == list(range(1, 100, 10))


def test_truncation_says_how_many_sessions_it_left_out(runs_dir, monkeypatch):
    monkeypatch.setattr(config, "RUN_MAX_SESSIONS", 2)
    make_db(runs_dir, "big", [session(index) for index in range(1, 6)])
    run = runs.load(runs_dir, "big")
    assert run.stored == 5
    assert len(run.sessions) == 2
    assert run.truncated == 3
    assert "3 session(s) not shown" in render_runs.run_page(run, run.default_cut())


def test_to_json_carries_counts_and_buckets(runs_dir):
    make_db(
        runs_dir,
        "json",
        [session(1), session(2, bucket="2m", arrival_ns=BASE + 1_000_000)],
        config_overrides={"contexts_per_browser": 6},
    )
    run = runs.load(runs_dir, "json")
    payload = runs.to_json(run, run.default_cut())
    assert payload["run_id"] == "json"
    assert payload["stored"] == 2
    assert payload["contexts_per_browser"] == 6
    assert set(payload["counts"]) == {"holding", "waiting", "hole", "done", "failed", "future"}
    assert [bucket["bucket"] for bucket in payload["buckets"]] == ["2m", "30s"]


# --------------------------------------------------------------------------- #
# The wall
# --------------------------------------------------------------------------- #
def wall_for(runs_dir, rows, **kwargs):
    make_db(runs_dir, "wall", rows, **kwargs)
    run = runs.load(runs_dir, "wall")
    return run, render_runs.run_page(run, run.default_cut())


def test_the_wall_draws_the_run_id_and_both_glyphs(runs_dir):
    run, markup = wall_for(
        runs_dir,
        [
            session(1),
            session(2, outcome="waiting", instance="", ready_ns=0, claim_created_ns=0,
                    hold_start_ns=0, hold_end_ns=0, gone_ns=0, delete_called_ns=0),
        ],
        ended=BASE + 2_000_000_000,
    )
    assert 'data-run="wall"' in markup
    assert run.run_id in markup
    assert "██" in markup          # the slot still held at the cut
    assert "⊗" in markup           # the session that never got an instance
    assert "holding a slot" in markup
    assert 'class="sky ' in markup or 'class="sky s-' in markup


def test_the_wall_escapes_instance_and_bucket_names(runs_dir):
    evil = "<script>alert(1)</script>"
    run, markup = wall_for(
        runs_dir,
        [session(1, instance=evil, bucket=evil)],
        ended=BASE + 40_000_000_000,
    )
    assert "<script>alert(1)</script>" not in markup
    assert "&lt;script&gt;alert(1)&lt;/script&gt;" in markup


def test_a_finished_run_keeps_its_last_state_as_a_stub(runs_dir):
    make_db(runs_dir, "stub", [session(1)], ended=BASE + 40_000_000_000)
    run = runs.load(runs_dir, "stub")
    # Drawn at the end, where nothing is held any more: the slot keeps its stub.
    markup = render_runs.run_page(run, run.ended_ns)
    assert "▪" in markup
    assert "last session 1 · success" in markup
    # The legend always carries every glyph; the slot itself is the stub.
    assert 'class="slot s-done"' in markup
    assert 'class="slot s-holding"' not in markup


def test_tracks_page_draws_one_row_per_slot_and_a_bar_per_session(runs_dir):
    make_db(
        runs_dir,
        "track",
        [
            session(1, browser_context="ctx-a"),
            session(2, browser_context="ctx-b", arrival_ns=BASE + 2_000_000_000),
        ],
        ended=BASE + 40_000_000_000,
    )
    run = runs.load(runs_dir, "track")
    markup = render_runs.run_page(run, run.default_cut(), instance="browser-7k2")
    assert markup.count('class="track"') == 2
    assert markup.count('class="bar ') == 2
    assert "ctx-a" in markup
    assert "back to the rack" in markup


def test_tracks_page_names_an_instance_it_cannot_find(runs_dir):
    make_db(runs_dir, "gone", [session(1)], ended=BASE + 40_000_000_000)
    run = runs.load(runs_dir, "gone")
    markup = render_runs.run_page(run, run.default_cut(), instance="browser-absent")
    assert "No instance named browser-absent" in markup


def test_the_index_links_each_run_and_explains_an_empty_directory(runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)])
    markup = render_runs.index(runs.discover(runs_dir))
    assert 'href="/runs/20260101T000000Z"' in markup
    assert "db only" in markup

    empty = runs_dir / "empty"
    empty.mkdir()
    markup = render_runs.index(runs.discover(empty))
    assert "No run database mounted" in markup


def test_a_group_shows_live_state_when_a_tile_is_available(runs_dir):
    tile = fixtures_tile()
    # The join key is the sandbox name: `sessions.instance` and `Tile.id` are the
    # same string, which is why a group can be badged with live cluster state.
    make_db(runs_dir, "live", [session(1, instance=tile.id)], ended=BASE + 40_000_000_000)
    run = runs.load(runs_dir, "live")
    markup = render_runs.wall(run, run.default_cut(), {tile.id: tile})
    assert "live kubernetes state" in markup
    assert tile.pod_ip in markup


def fixtures_tile():
    import inventory

    report = inventory.build_report(
        fixtures.copy_of(fixtures.READY_SANDBOX),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
    )
    return report.tiles[0]


# --------------------------------------------------------------------------- #
# The routes
# --------------------------------------------------------------------------- #
def fake_snapshot(namespace: str) -> kube.Snapshot:
    return kube.Snapshot(
        sandboxes=fixtures.copy_of(fixtures.READY_SANDBOX, fixtures.POOL_MEMBER),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
        errors=[],
    )


@pytest.fixture
def client(runs_dir, monkeypatch):
    monkeypatch.setattr(kube, "snapshot", fake_snapshot)
    monkeypatch.setattr(server, "kube", kube)
    with TestClient(server.app) as test_client:
        yield test_client


def test_runs_route_lists_the_database(client, runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)])
    response = client.get("/runs")
    assert response.status_code == 200
    assert "/runs/20260101T000000Z" in response.text
    assert "Pool" in response.text and "Run" in response.text


def test_run_route_serves_the_wall_and_an_unknown_id_is_404(client, runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)], ended=BASE + 40_000_000_000)
    response = client.get("/runs/20260101T000000Z?live=0")
    assert response.status_code == 200
    assert "██" in response.text or "▪" in response.text
    assert client.get("/runs/nope").status_code == 404


def test_wall_fragment_is_the_inner_div_for_the_live_refresh(client, runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)], ended=BASE + 40_000_000_000)
    response = client.get("/runs/20260101T000000Z/wall")
    assert response.status_code == 200
    assert 'id="wall-inner"' in response.text
    assert "<html" not in response.text


def test_run_with_live_state_badges_the_instance_it_can_see(client, runs_dir):
    tile = fixtures_tile()
    make_db(
        runs_dir,
        "20260101T000000Z",
        [session(1, instance=tile.id)],
        ended=BASE + 40_000_000_000,
    )
    response = client.get("/runs/20260101T000000Z")
    assert response.status_code == 200
    assert "live kubernetes state" in response.text
    assert tile.pod_ip in response.text


def test_api_runs_returns_the_index_and_one_run(client, runs_dir):
    make_db(runs_dir, "20260101T000000Z", [session(1)], ended=BASE + 40_000_000_000)
    index = client.get("/api/runs")
    assert index.status_code == 200
    payload = index.json()
    assert payload[0]["run_id"] == "20260101T000000Z"
    assert payload[0]["stored"] == 1

    one = client.get("/api/runs/20260101T000000Z")
    assert one.status_code == 200
    assert one.json()["counts"]["done"] == 0  # held at its peak, not finished
    assert client.get("/api/runs/nope").status_code == 404


def test_existing_routes_and_apis_are_untouched(client):
    for path in ("/", "/sandboxes", "/compare", "/recipes", "/healthz", "/api/sandboxes"):
        assert client.get(path).status_code == 200, path

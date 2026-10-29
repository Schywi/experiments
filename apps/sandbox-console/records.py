"""Durable timings per sandbox (SQLite on a PVC).

Why this exists: the four timers (`t_sched`, `t_ready`, `t_cdp`, `t_paint`) only
exist at creation. Comparing a sandbox that has been running for two hours is
impossible unless its numbers were written down when it was made -- and the pane
otherwise keeps everything in RAM, so every deploy erased them.

One row per sandbox the console created. A sandbox the console did not create has
no row, and the dialog says so rather than inventing numbers.

The database is deliberately tiny and synchronous: one uvicorn worker, one PVC,
`sqlite3` from the standard library, and a lock so the executor threads cannot
interleave a write.
"""

from __future__ import annotations

import json
import os
import sqlite3
import threading
import time
from pathlib import Path
from typing import Any

DB_PATH = os.environ.get("CONSOLE_DB", "/data/console.db")

_SCHEMA = """
CREATE TABLE IF NOT EXISTS timings (
    sandbox    TEXT PRIMARY KEY,
    claim      TEXT,
    kind       TEXT NOT NULL,
    created_at REAL NOT NULL,
    t_sched_ms INTEGER,
    t_ready_ms INTEGER,
    t_cdp_ms   INTEGER,
    t_paint_ms INTEGER,
    image      TEXT,
    target_url TEXT
);
CREATE TABLE IF NOT EXISTS compare_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at REAL NOT NULL,
    payload    TEXT NOT NULL
);
"""

_lock = threading.Lock()
_connection: sqlite3.Connection | None = None


def _db() -> sqlite3.Connection:
    """One connection for the process, created on first use."""
    global _connection
    if _connection is None:
        path = Path(DB_PATH)
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            _connection = sqlite3.connect(path, check_same_thread=False, timeout=5)
        except OSError:
            # No writable volume: fall back to an in-memory database so the pane
            # keeps working, and say so once at startup rather than per request.
            _connection = sqlite3.connect(":memory:", check_same_thread=False)
        _connection.row_factory = sqlite3.Row
        _connection.executescript(_SCHEMA)
        _connection.commit()
    return _connection


def durable() -> bool:
    """True when rows will survive a restart."""
    try:
        database = _db()
        return database.execute("PRAGMA database_list").fetchone()["file"] != ""
    except sqlite3.Error:
        return False


def remember_timings(
    sandbox: str,
    *,
    kind: str,
    claim: str | None = None,
    created_at: float | None = None,
    timers: dict[str, int] | None = None,
    image: str | None = None,
    target_url: str | None = None,
) -> None:
    """Upsert one sandbox's timings. A later, more complete write wins per field."""
    timers = timers or {}
    with _lock:
        database = _db()
        database.execute(
            """
            INSERT INTO timings (sandbox, claim, kind, created_at, t_sched_ms, t_ready_ms,
                                 t_cdp_ms, t_paint_ms, image, target_url)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(sandbox) DO UPDATE SET
                claim      = COALESCE(excluded.claim, timings.claim),
                kind       = excluded.kind,
                t_sched_ms = COALESCE(excluded.t_sched_ms, timings.t_sched_ms),
                t_ready_ms = COALESCE(excluded.t_ready_ms, timings.t_ready_ms),
                t_cdp_ms   = COALESCE(excluded.t_cdp_ms, timings.t_cdp_ms),
                t_paint_ms = COALESCE(excluded.t_paint_ms, timings.t_paint_ms),
                image      = COALESCE(excluded.image, timings.image),
                target_url = COALESCE(excluded.target_url, timings.target_url)
            """,
            (
                sandbox,
                claim,
                kind,
                created_at if created_at is not None else time.time(),
                timers.get("t_sched_ms"),
                timers.get("t_ready_ms"),
                timers.get("t_cdp_ms"),
                timers.get("t_paint_ms"),
                image,
                target_url,
            ),
        )
        database.commit()


def timings_for(sandbox: str) -> dict[str, Any] | None:
    """The recorded row for one sandbox, or None if the console never made it."""
    with _lock:
        row = _db().execute("SELECT * FROM timings WHERE sandbox = ?", (sandbox,)).fetchone()
    return dict(row) if row else None


def recent(limit: int = 50) -> list[dict[str, Any]]:
    with _lock:
        rows = _db().execute(
            "SELECT * FROM timings ORDER BY created_at DESC LIMIT ?", (limit,)
        ).fetchall()
    return [dict(row) for row in rows]


def remember_run(payload: dict) -> None:
    """Keep a compare run so the /compare page survives a restart."""
    with _lock:
        database = _db()
        database.execute(
            "INSERT INTO compare_runs (started_at, payload) VALUES (?, ?)",
            (payload.get("started_at") or time.time(), json.dumps(payload)),
        )
        database.commit()


def recent_runs(limit: int = 3) -> list[dict]:
    with _lock:
        rows = _db().execute(
            "SELECT payload FROM compare_runs ORDER BY id DESC LIMIT ?", (limit,)
        ).fetchall()
    return [json.loads(row["payload"]) for row in rows]

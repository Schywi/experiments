"""Read a session-lab run database without writing to it.

The runner commits one statement per lifecycle transition
(`apps/session-lab/internal/platform/store/store.go`), so the database of a run
that is still executing already carries every row it has reached. One read path
therefore renders a live run and a finished one.

The wall needs three groupings on top of the rows: bucket, instance, and the slot
inside an instance. A slot is `(instance, browser_context)` when the runner
recorded a context id, and a time-bound lane while it does not; both cases are
`Slot` here, distinguished by `Slot.exact`. See
`research/session-lifecycle-elasticity/16-session-wall-ui.md` §2 and §7.
"""

from __future__ import annotations

import json
import re
import shutil
import sqlite3
import tempfile
import time
from dataclasses import dataclass, field
from dataclasses import fields as dataclass_fields
from pathlib import Path

import config as console_config

# Outcomes the runner's report counts as failures (`model.Failures`). A session
# still queued when the run ended is `waiting`, which is its own state.
FAILED_OUTCOMES = {"timeout", "acquisition_failed", "reset_failed", "destroy_failed", "lost_unknown"}
# Outcomes that mean "no terminal transition was observed".
OPEN_OUTCOMES = {"waiting", "pending", ""}

# Session states as the wall draws them. `waiting` and `hole` are the two ways a
# session can be short of a slot: waiting had an instance assigned or was still
# queued, a hole never got one.
HOLDING = "holding"
WAITING = "waiting"
HOLE = "hole"
DONE = "done"
FAILED = "failed"
FUTURE = "future"

# Column tolerance: a run database written before the shared-browser work has no
# `browser_context` or `cpu_shared`, and one written after it has both; the
# scheduler's own migrations have added `admission_wait_ns` and `cpu_shared` over
# time. The reader therefore selects the intersection of the table's columns and
# the fields of `Session`, so an old database renders and a new column is picked
# up by adding the field.

_RUN_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")


class RunNotFound(Exception):
    """No run database with that id in the runs directory."""


def _connect(path: Path) -> sqlite3.Connection:
    """Open read-only, falling back to a copy when the WAL cannot be joined.

    A WAL database needs its `-shm` file; reading in place fails when the console
    runs as a different user from the runner or the mount is read-only. Copying
    the three files into a temp directory is the escape hatch, and it never
    touches the runner's copy.
    """
    try:
        connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=2.0)
        connection.execute("SELECT 1 FROM sqlite_master LIMIT 1")
        return connection
    except sqlite3.DatabaseError:
        pass
    workdir = Path(tempfile.mkdtemp(prefix="run-"))
    for suffix in ("", "-wal", "-shm"):
        source = Path(str(path) + suffix)
        if source.exists():
            shutil.copy2(source, workdir / (path.name + suffix))
    connection = sqlite3.connect(str(workdir / path.name), timeout=2.0)
    return connection


def _table_columns(connection: sqlite3.Connection, table: str) -> set[str]:
    try:
        return {row[1] for row in connection.execute(f"PRAGMA table_info({table})")}
    except sqlite3.DatabaseError:
        return set()


def _now_ns() -> int:
    return int(time.time() * 1_000_000_000)


def bucket_seconds(name: str) -> float:
    """`30s` → 30.0, `2m` → 120.0, `1h30m` → 5400.0; unparseable → 0.0."""
    total = 0.0
    matched = False
    for value, unit in re.findall(r"(\d+(?:\.\d+)?)\s*([smhd])", name or ""):
        matched = True
        total += float(value) * {"s": 1, "m": 60, "h": 3600, "d": 86400}[unit]
    return total if matched else 0.0


def percentile(values: list[float], fraction: float) -> float:
    """Nearest-rank percentile; 0.0 for an empty list."""
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, int(round(fraction * (len(ordered) - 1)))))
    return ordered[index]


@dataclass
class Session:
    """One row of `sessions`, with the derivations the wall needs."""

    id: int
    bucket: str = ""
    policy: str = ""
    instance: str = ""
    outcome: str = "pending"
    error: str = ""
    launch_type: str = ""
    node: str = ""
    browser_context: str = ""
    arrival_ns: int = 0
    admission_wait_ns: int = 0
    claim_created_ns: int = 0
    ready_ns: int = 0
    hold_start_ns: int = 0
    hold_end_ns: int = 0
    delete_called_ns: int = 0
    gone_ns: int = 0
    reset_start_ns: int = 0
    reset_end_ns: int = 0
    resume_start_ns: int = 0
    resume_end_ns: int = 0
    cpu_usec: int = 0
    cpu_shared: int = 0
    throttled_usec: int = 0
    nr_throttled: int = 0
    mem_peak_bytes: int = 0
    mem_working_set_peak_bytes: int = 0
    swap_peak_bytes: int = 0
    oom_kill: int = 0
    samples: int = 0

    @property
    def failed(self) -> bool:
        return self.outcome in FAILED_OUTCOMES

    @property
    def terminal(self) -> bool:
        return self.outcome not in OPEN_OUTCOMES

    @property
    def queue_ns(self) -> int:
        """Arrival to the instance being Ready: what the session waited for."""
        if self.ready_ns and self.arrival_ns:
            return max(0, self.ready_ns - self.arrival_ns)
        return 0

    @property
    def acquire_ns(self) -> int:
        """Arrival to Ready, measured from the claim create when there is one."""
        start = self.claim_created_ns or self.arrival_ns
        if self.ready_ns and start:
            return max(0, self.ready_ns - start)
        return 0

    @property
    def hold_window(self) -> tuple[int, int]:
        """The window this session occupied its slot, `(start, end)`.

        `end` is the release call when the session never recorded `gone`, so an
        interrupted run still draws the bar it did reach, and the end is marked
        `open` by the renderer rather than drawn as a clean finish.
        """
        start = self.hold_start_ns or self.ready_ns or self.claim_created_ns or self.arrival_ns
        end = self.hold_end_ns or self.gone_ns or self.delete_called_ns or start
        return start, max(start, end)

    @property
    def open_ended(self) -> bool:
        """True when the instance was never observed gone.

        `hold_end_ns` marks the release call; `gone_ns` is the delete having
        taken effect. A row with the first and not the second is a session whose
        teardown was never confirmed, which a run killed mid-flight leaves
        behind.
        """
        return not self.gone_ns

    @property
    def cpu_core_seconds(self) -> float:
        return self.cpu_usec / 1_000_000.0

    def state_at(self, cut_ns: int) -> str:
        """What this session was doing at `cut_ns`.

        A terminal outcome outranks the timestamps: a session that recorded
        `reset_failed` after reaching Ready keeps a non-zero `ready_ns` and no
        `gone_ns`, so reading the window before the outcome would draw a failed
        session as still holding its slot.

        `hole` is the runner's own `waiting` outcome, which it writes only for a
        session still queued when the run ended. A `pending` row is one the
        runner never finished with, so it reads as waiting rather than as a hole
        the platform left behind.
        """
        if self.arrival_ns and self.arrival_ns > cut_ns:
            return FUTURE
        if self.failed:
            return FAILED
        holding_now = (
            bool(self.ready_ns)
            and self.ready_ns <= cut_ns
            and (not self.gone_ns or self.gone_ns > cut_ns)
            and (not self.delete_called_ns or self.delete_called_ns > cut_ns)
        )
        if holding_now:
            return HOLDING
        if self.ready_ns and self.ready_ns <= cut_ns:
            return DONE
        if self.outcome == "waiting":
            return HOLE
        return WAITING


@dataclass
class Slot:
    """One position inside a Chromium: `(instance, browser_context)` or a lane."""

    position: int
    key: str
    sessions: list[Session] = field(default_factory=list)
    exact: bool = False

    def holding_at(self, cut_ns: int) -> Session | None:
        for session in self.sessions:
            if session.state_at(cut_ns) == HOLDING:
                return session
        return None

    def last(self) -> Session | None:
        return self.sessions[-1] if self.sessions else None


@dataclass
class InstanceGroup:
    name: str
    sessions: list[Session] = field(default_factory=list)
    slots: list[Slot] = field(default_factory=list)
    capacity: int = 1

    @property
    def first_arrival_ns(self) -> int:
        return min((item.arrival_ns for item in self.sessions if item.arrival_ns), default=0)

    def holding_at(self, cut_ns: int) -> int:
        return sum(1 for item in self.sessions if item.state_at(cut_ns) == HOLDING)

    @property
    def exact(self) -> bool:
        return any(slot.exact for slot in self.slots)


@dataclass
class Bucket:
    name: str
    seconds: float
    sessions: list[Session] = field(default_factory=list)
    instances: list[InstanceGroup] = field(default_factory=list)
    # Sessions in this bucket that never received an instance name at all, so
    # they belong to no group and are counted beside the groups instead.
    unassigned: int = 0

    @property
    def hold_p50_ms(self) -> float:
        return percentile([_ms(item.hold_window[1] - item.hold_window[0]) for item in self.sessions], 0.5)

    @property
    def queue_p50_ms(self) -> float:
        return percentile([_ms(item.queue_ns) for item in self.sessions], 0.5)


@dataclass
class Run:
    run_id: str
    path: Path
    started_ns: int = 0
    ended_ns: int = 0
    config: dict = field(default_factory=dict)
    sessions: list[Session] = field(default_factory=list)
    stored: int = 0
    truncated: int = 0
    error: str | None = None
    report: dict | None = None
    read_at_ns: int = 0
    buckets: list[Bucket] = field(default_factory=list)

    @property
    def live(self) -> bool:
        return not self.ended_ns

    @property
    def planned(self) -> int:
        for key in ("sessions", "planned_sessions"):
            value = self.config.get(key)
            if isinstance(value, int) and value > 0:
                return value
        return self.stored

    @property
    def contexts_per_browser(self) -> int:
        value = self.config.get("contexts_per_browser")
        return value if isinstance(value, int) and value > 0 else 0

    @property
    def window(self) -> tuple[int, int]:
        """The span the wall draws: first arrival to the last observed event."""
        arrivals = [item.arrival_ns for item in self.sessions if item.arrival_ns]
        ends = [item.gone_ns or item.hold_window[1] for item in self.sessions]
        start = min(arrivals) if arrivals else self.started_ns
        candidates = [value for value in ends + [self.ended_ns, start] if value]
        end = max(candidates) if candidates else start + 1
        return start, max(end, start + 1)

    @property
    def peak_ns(self) -> int:
        """The instant the run held the most slots at once.

        This is the wall's default frame for a finished run: the end of a run
        holds nothing, so drawing it at `ended_ns` would show an empty rack.
        """
        marks: list[tuple[int, int]] = []
        for item in self.sessions:
            if not item.ready_ns:
                continue
            start, end = item.hold_window
            if end <= start:
                continue
            marks.append((start, 1))
            marks.append((end, -1))
        if not marks:
            return self.ended_ns or self.window[0]
        # Ends sort before starts at the same nanosecond, so touching windows
        # never read as two.
        marks.sort(key=lambda pair: (pair[0], pair[1]))
        current = best = 0
        best_at = marks[0][0]
        for at_ns, delta in marks:
            current += delta
            if current > best:
                best, best_at = current, at_ns
        return best_at

    @property
    def peak_concurrency(self) -> int:
        marks: list[tuple[int, int]] = []
        for item in self.sessions:
            if not item.ready_ns:
                continue
            start, end = item.hold_window
            if end <= start:
                continue
            marks += [(start, 1), (end, -1)]
        marks.sort(key=lambda pair: (pair[0], pair[1]))
        current = best = 0
        for _, delta in marks:
            current += delta
            best = max(best, current)
        return best

    def counts(self, cut_ns: int) -> dict[str, int]:
        counters = {HOLDING: 0, WAITING: 0, HOLE: 0, DONE: 0, FAILED: 0, FUTURE: 0}
        for item in self.sessions:
            counters[item.state_at(cut_ns)] += 1
        return counters

    def default_cut(self) -> int:
        """Live runs draw now; finished runs draw their peak."""
        if self.ended_ns:
            return self.peak_ns
        return max(self.read_at_ns, self.window[1])


def _ms(nanoseconds: int) -> float:
    return nanoseconds / 1_000_000.0


def _row_to_session(row: sqlite3.Row) -> Session:
    """Map a row onto `Session`, ignoring columns the dataclass does not carry."""
    values = dict(row)
    kwargs = {}
    for item in dataclass_fields(Session):
        if item.name == "id" or item.name not in values:
            continue
        raw = values[item.name]
        kwargs[item.name] = int(raw or 0) if item.type == "int" else str(raw or "")
    return Session(id=int(values.get("id") or 0), **kwargs)


def session_columns(columns: set[str]) -> list[str]:
    """The `sessions` columns this module reads, in dataclass order."""
    return [item.name for item in dataclass_fields(Session) if item.name != "id" and item.name in columns]


def _group(sessions: list[Session], run: Run) -> list[Bucket]:
    """Bucket → instance group → slot."""
    by_bucket: dict[str, list[Session]] = {}
    for session in sessions:
        by_bucket.setdefault(session.bucket or "—", []).append(session)
    buckets: list[Bucket] = []
    for name, members in by_bucket.items():
        bucket = Bucket(name=name, seconds=bucket_seconds(name), sessions=sorted(members, key=lambda s: s.arrival_ns))
        by_instance: dict[str, list[Session]] = {}
        unassigned = 0
        for session in members:
            if not session.instance:
                unassigned += 1
                continue
            by_instance.setdefault(session.instance, []).append(session)
        bucket.unassigned = unassigned
        groups: list[InstanceGroup] = []
        for instance, instance_sessions in by_instance.items():
            ordered = sorted(instance_sessions, key=lambda s: s.arrival_ns)
            slots = _slots(ordered)
            capacity = max(len(slots), run.contexts_per_browser, 1)
            groups.append(
                InstanceGroup(
                    name=instance,
                    sessions=ordered,
                    slots=slots,
                    capacity=capacity,
                )
            )
        groups.sort(key=lambda group: (group.first_arrival_ns, group.name))
        bucket.instances = groups
        buckets.append(bucket)
    buckets.sort(key=lambda bucket: (-bucket.seconds, bucket.name))
    return buckets


def _slots(sessions: list[Session]) -> list[Slot]:
    """Place one instance's sessions on slot positions.

    With a `browser_context` in the row the position is the runner's own answer.
    Without one the position is a lane found by packing hold windows, which is
    what the console can defend: two sessions that overlapped were in different
    slots, two that did not may have shared one.
    """
    if any(session.browser_context for session in sessions):
        grouped: dict[str, list[Session]] = {}
        for session in sessions:
            grouped.setdefault(session.browser_context or "—", []).append(session)
        ordered = sorted(grouped.items(), key=lambda pair: pair[1][0].arrival_ns)
        return [
            Slot(position=index, key=key, sessions=members, exact=True)
            for index, (key, members) in enumerate(ordered)
        ]
    lanes: list[dict] = []
    for session in sorted(sessions, key=lambda item: item.arrival_ns):
        start, end = session.hold_window
        for lane in lanes:
            if lane["end"] <= start:
                lane["sessions"].append(session)
                lane["end"] = max(lane["end"], end)
                break
        else:
            lanes.append({"end": end, "sessions": [session]})
    return [
        Slot(position=index, key=f"t{index}", sessions=lane["sessions"], exact=False)
        for index, lane in enumerate(lanes)
    ]


def load(runs_dir, run_id: str) -> Run:
    """Read one run. Raises `RunNotFound` when the id is absent or unsafe."""
    if not _RUN_ID.match(run_id or ""):
        raise RunNotFound(run_id)
    path = Path(runs_dir) / f"{run_id}.db"
    if not path.exists():
        raise RunNotFound(run_id)
    run = Run(run_id=run_id, path=path, read_at_ns=_now_ns())
    report_path = path.with_suffix(".json")
    if report_path.exists():
        try:
            run.report = json.loads(report_path.read_text())
        except (ValueError, OSError):
            run.report = None
    connection = _connect(path)
    try:
        connection.row_factory = sqlite3.Row
        runs_columns = _table_columns(connection, "runs")
        if {"run_id", "started_ns"} <= runs_columns:
            row = connection.execute("SELECT * FROM runs ORDER BY started_ns DESC LIMIT 1").fetchone()
            if row is not None:
                run.started_ns = int(row["started_ns"] or 0)
                run.ended_ns = int(row["ended_ns"] or 0)
                raw = row["config"] if "config" in runs_columns else ""
                try:
                    run.config = json.loads(raw) if raw else {}
                except ValueError:
                    run.config = {}
        columns = _table_columns(connection, "sessions")
        if not columns:
            run.error = "no sessions table"
            return run
        run.stored = int(connection.execute("SELECT COUNT(*) FROM sessions").fetchone()[0] or 0)
        selected = ["id"] + session_columns(columns)
        order = "arrival_ns ASC, id ASC" if "arrival_ns" in columns else "id ASC"
        query = f"SELECT {', '.join(selected)} FROM sessions ORDER BY {order} LIMIT ?"
        rows = connection.execute(query, (max(1, console_config.RUN_MAX_SESSIONS),)).fetchall()
        run.sessions = [_row_to_session(row) for row in rows]
        run.truncated = max(0, run.stored - len(run.sessions))
    except sqlite3.DatabaseError as error:
        run.error = f"{error.__class__.__name__}: {error}"
        return run
    finally:
        connection.close()
    run.buckets = _group(run.sessions, run)
    return run


@dataclass
class RunRef:
    """One entry of the runs index, read without loading every session."""

    run_id: str
    path: Path
    modified: float = 0.0
    size: int = 0
    stored: int = 0
    started_ns: int = 0
    ended_ns: int = 0
    outcomes: dict[str, int] = field(default_factory=dict)
    instances: int = 0
    has_report: bool = False
    error: str | None = None

    @property
    def live(self) -> bool:
        return bool(self.started_ns) and not self.ended_ns

    def to_dict(self) -> dict:
        return {
            "run_id": self.run_id,
            "started_ns": self.started_ns,
            "ended_ns": self.ended_ns,
            "live": self.live,
            "stored": self.stored,
            "instances": self.instances,
            "outcomes": self.outcomes,
            "size": self.size,
            "has_report": self.has_report,
            "error": self.error,
        }


def discover(runs_dir) -> list[RunRef]:
    """List the run databases, newest first. A missing directory is not an error."""
    directory = Path(runs_dir)
    if not directory.is_dir():
        return []
    refs: list[RunRef] = []
    for path in sorted(directory.glob("*.db")):
        try:
            stat = path.stat()
        except OSError:
            continue
        ref = RunRef(
            run_id=path.stem,
            path=path,
            modified=stat.st_mtime,
            size=stat.st_size,
            has_report=path.with_suffix(".json").exists(),
        )
        try:
            connection = _connect(path)
            try:
                connection.row_factory = sqlite3.Row
                runs_columns = _table_columns(connection, "runs")
                if {"run_id", "started_ns"} <= runs_columns:
                    row = connection.execute(
                        "SELECT started_ns, ended_ns FROM runs ORDER BY started_ns DESC LIMIT 1"
                    ).fetchone()
                    if row is not None:
                        ref.started_ns = int(row["started_ns"] or 0)
                        ref.ended_ns = int(row["ended_ns"] or 0)
                if _table_columns(connection, "sessions"):
                    ref.stored = int(connection.execute("SELECT COUNT(*) FROM sessions").fetchone()[0] or 0)
                    for outcome, count in connection.execute(
                        "SELECT outcome, COUNT(*) FROM sessions GROUP BY outcome"
                    ):
                        ref.outcomes[str(outcome or "pending")] = int(count)
                    ref.instances = int(
                        connection.execute(
                            "SELECT COUNT(DISTINCT instance) FROM sessions WHERE instance <> ''"
                        ).fetchone()[0]
                        or 0
                    )
            finally:
                connection.close()
        except (sqlite3.DatabaseError, OSError) as error:
            ref.error = f"{error.__class__.__name__}: {error}"
        refs.append(ref)
    refs.sort(key=lambda item: (item.started_ns or int(item.modified * 1_000_000_000)), reverse=True)
    return refs


def skyline(sessions: list[Session], cut_ns: int, limit: int) -> list[dict]:
    """The pinned overview strip: one cell per session, or per group of sessions.

    Ordered by arrival, so the strip reads as the sequence of demands the
    platform was given. When there are more sessions than cells, each cell takes
    the worst state in its group: a hole or a failure outranks a hold, because a
    strip that hid those would be drawing a healthier run than happened.
    """
    ordered = sorted(sessions, key=lambda item: (item.arrival_ns, item.id))
    if not ordered:
        return []
    limit = max(1, limit)
    step = max(1, (len(ordered) + limit - 1) // limit)
    rank = {FAILED: 5, HOLE: 4, WAITING: 3, HOLDING: 2, DONE: 1, FUTURE: 0}
    cells = []
    for start in range(0, len(ordered), step):
        chunk = ordered[start : start + step]
        states = [item.state_at(cut_ns) for item in chunk]
        worst = max(states, key=lambda state: rank.get(state, 0))
        cells.append(
            {
                "state": worst,
                "count": len(chunk),
                "first_id": chunk[0].id,
                "last_id": chunk[-1].id,
                "bucket": chunk[0].bucket,
            }
        )
    return cells


def to_json(run: Run, cut_ns: int) -> dict:
    """The machine-readable shape of a run, for `/api/runs/{id}`."""
    counts = run.counts(cut_ns)
    start, end = run.window
    return {
        "run_id": run.run_id,
        "started_ns": run.started_ns,
        "ended_ns": run.ended_ns,
        "live": run.live,
        "planned": run.planned,
        "stored": run.stored,
        "truncated": run.truncated,
        "contexts_per_browser": run.contexts_per_browser,
        "window_ns": [start, end],
        "cut_ns": cut_ns,
        "counts": counts,
        "peak_concurrency": run.peak_concurrency,
        "instances": len({item.instance for item in run.sessions if item.instance}),
        "error": run.error,
        "buckets": [
            {
                "bucket": bucket.name,
                "seconds": bucket.seconds,
                "sessions": len(bucket.sessions),
                "instances": len(bucket.instances),
                "hold_p50_ms": round(bucket.hold_p50_ms, 1),
                "queue_p50_ms": round(bucket.queue_p50_ms, 1),
                "capacity": sum(group.capacity for group in bucket.instances),
            }
            for bucket in run.buckets
        ],
    }

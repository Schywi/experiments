"""One JSON line per lifecycle event, and nothing else in the file.

Every stage of an E6 run writes here, so a reader can reconstruct the run without
reading code: what was acquired, what the browser reported, what the model said,
what was validated, what was clicked, and what the page did afterwards.

The file is append-only and flushed per line, because the run under study includes
failures — a click that kills the sandbox it is talking to is a result, and the
events leading to it must already be on disk when that happens.
"""

from __future__ import annotations

import datetime
import json
import os


class Events:
    """Append-only event log for one run."""

    def __init__(self, path: str, run_id: str):
        self.path = path
        self.run_id = run_id
        os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
        self._fh = open(path, "a", encoding="utf-8")

    def emit(
        self,
        name: str,
        *,
        sandbox_id: str | None = None,
        arm: str | None = None,
        task: str | None = None,
        ok: bool = True,
        duration_ms: float | None = None,
        detail: dict | None = None,
        **extra: object,
    ) -> dict:
        record = {
            "ts": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds"),
            "run_id": self.run_id,
            "event": name,
            "sandbox_id": sandbox_id,
            "arm": arm,
            "task": task,
            "ok": ok,
        }
        if duration_ms is not None:
            record["duration_ms"] = round(duration_ms, 1)
        payload = dict(detail or {})
        payload.update(extra)
        if payload:
            record["detail"] = payload
        self._fh.write(json.dumps(record) + "\n")
        self._fh.flush()
        return record

    def close(self) -> None:
        try:
            self._fh.close()
        except Exception:  # noqa: BLE001 - closing must never raise
            pass

    def __enter__(self) -> "Events":
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()


def read(path: str) -> list[dict]:
    """Read an events file back. Used by the scorer and by --dry-run's assertion."""
    out = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out

"""One JSONL line per mutation and per sandbox interaction. No database.

The console is read-only in this milestone, so the records are evidence of what
was *looked at* (a screenshot, a tap, a probe) rather than what was changed.
Best-effort: an audit write never breaks a request.
"""

import json
import os
from datetime import datetime, timezone


def record(log_path: str, event: str, **fields) -> None:
    """Append one audit record to ``log_path``, swallowing filesystem errors."""
    entry = {"ts": datetime.now(timezone.utc).isoformat(), "event": event, **fields}
    try:
        directory = os.path.dirname(log_path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        with open(log_path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry) + "\n")
    except OSError:
        pass

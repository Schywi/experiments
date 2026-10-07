"""Shared Kubernetes client + audit sink for the assistant.

Kept free of any dependency on the tool registry, so tools.py, observability.py,
and actions.py can all import it without a circular import.
"""

import json
import os
from datetime import datetime, timezone
from typing import Optional

from kubernetes import client, config

AUDIT_LOG = os.environ.get("AUDIT_LOG", "/var/log/assistant/audit.jsonl")

_core_client: Optional["client.CoreV1Api"] = None


def _core() -> "client.CoreV1Api":
    global _core_client
    if _core_client is None:
        config.load_incluster_config()
        _core_client = client.CoreV1Api()
    return _core_client


def audit(event: dict) -> None:
    """Append one JSONL audit record. Best-effort: never breaks the request path."""
    record = {"ts": datetime.now(timezone.utc).isoformat(), **event}
    try:
        os.makedirs(os.path.dirname(AUDIT_LOG), exist_ok=True)
        with open(AUDIT_LOG, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(record) + "\n")
    except OSError:
        pass

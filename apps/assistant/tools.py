"""Read-only Kubernetes tools for the assistant (Milestone 2).

Each tool is a narrow, typed function with a risk level. The assistant never
shells out; it calls the Kubernetes API as its own ServiceAccount, whose
ClusterRole (chart/templates/rbac.yaml) limits it to get/list/watch on a fixed
resource set.
"""

import json
import os
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any, Callable, Optional

from kubernetes import client, config

AUDIT_LOG = os.environ.get("AUDIT_LOG", "/var/log/assistant/audit.jsonl")

_core_client: Optional["client.CoreV1Api"] = None


def _core() -> "client.CoreV1Api":
    global _core_client
    if _core_client is None:
        config.load_incluster_config()
        _core_client = client.CoreV1Api()
    return _core_client


@dataclass(frozen=True)
class Tool:
    name: str
    level: int  # 0 chat, 1 read-only, 2 safe action, 3 destructive
    description: str
    fn: Callable[..., Any]


def get_pods(namespace: Optional[str] = None) -> list:
    """List pods with phase, node, readiness and restart count."""
    v1 = _core()
    if namespace:
        items = v1.list_namespaced_pod(namespace).items
    else:
        items = v1.list_pod_for_all_namespaces().items
    pods = []
    for p in items:
        containers = p.status.container_statuses or []
        ready = sum(1 for c in containers if c.ready)
        pods.append({
            "namespace": p.metadata.namespace,
            "name": p.metadata.name,
            "phase": p.status.phase,
            "node": p.spec.node_name,
            "ready": f"{ready}/{len(containers)}",
            "restarts": sum(c.restart_count for c in containers),
        })
    return sorted(pods, key=lambda x: (x["namespace"], x["name"]))


REGISTRY: dict = {
    "get_pods": Tool("get_pods", 1, "List pods and their status", get_pods),
}


def route(message: str) -> Optional[tuple]:
    """Milestone-2 router: keyword rules.

    Returns (tool_name, kwargs) or None for plain chat. Replaced by the Laya
    decision engine in a later milestone; the interface stays the same.
    """
    m = message.lower()
    if "pod" in m and "log" not in m:
        return "get_pods", {}
    return None


def audit(event: dict) -> None:
    """Append one JSONL audit record. Best-effort: never breaks the request path."""
    record = {"ts": datetime.now(timezone.utc).isoformat(), **event}
    try:
        os.makedirs(os.path.dirname(AUDIT_LOG), exist_ok=True)
        with open(AUDIT_LOG, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(record) + "\n")
    except OSError:
        pass

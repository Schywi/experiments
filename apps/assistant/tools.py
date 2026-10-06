"""Read-only Kubernetes tools for the assistant (Milestones 2-3).

Each tool is a narrow, typed function with a risk level. The assistant never
shells out; it calls the Kubernetes API as its own ServiceAccount, whose
ClusterRole (chart/templates/rbac.yaml) limits it to get/list/watch on a fixed
resource set (plus get on pods/log).
"""

import json
import os
import re
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


# --- level 1: read-only -----------------------------------------------------

def get_pods(namespace: Optional[str] = None) -> list:
    """List pods with phase, node, readiness and restart count."""
    v1 = _core()
    items = (v1.list_namespaced_pod(namespace).items if namespace
             else v1.list_pod_for_all_namespaces().items)
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


def get_pod_logs(namespace: str, pod: str, tail_lines: int = 100) -> dict:
    """Read the most recent log lines from one pod/container."""
    v1 = _core()
    log = v1.read_namespaced_pod_log(name=pod, namespace=namespace, tail_lines=tail_lines)
    return {"namespace": namespace, "pod": pod, "tail_lines": tail_lines, "log": log}


def get_events(namespace: Optional[str] = None) -> list:
    """List recent cluster events (warnings included), newest last."""
    v1 = _core()
    items = (v1.list_namespaced_event(namespace).items if namespace
             else v1.list_event_for_all_namespaces().items)
    events = []
    for e in items:
        io = e.involved_object
        events.append({
            "namespace": e.metadata.namespace,
            "type": e.type,
            "reason": e.reason,
            "object": f"{io.kind}/{io.name}",
            "message": e.message,
            "count": e.count,
        })
    return events[-50:]


REGISTRY: dict = {
    "get_pods": Tool("get_pods", 1, "List pods and their status", get_pods),
    "get_pod_logs": Tool("get_pod_logs", 1, "Read recent logs from a pod", get_pod_logs),
    "get_events": Tool("get_events", 1, "List recent cluster events", get_events),
}


# --- routing ----------------------------------------------------------------
# Milestone-3 router: keyword rules with light argument extraction. The Laya
# decision engine replaces this in a later milestone; the interface
# (message -> (tool, kwargs) | None) stays the same.

_NS_RE = re.compile(r"\b(?:in|namespace)\s+([a-z0-9][a-z0-9-]*)")
_POD_RE = re.compile(r"\b(?:pod|pods)\s+([a-z0-9][a-z0-9.-]*)")


def route(message: str) -> Optional[tuple]:
    m = message.lower()
    ns_match = _NS_RE.search(m)
    namespace = ns_match.group(1) if ns_match else None

    if "log" in m:
        pod_match = _POD_RE.search(m)
        if pod_match:
            return "get_pod_logs", {"namespace": namespace or "default", "pod": pod_match.group(1)}
        return None  # a pod name is required; let the model ask for it

    if "event" in m:
        return "get_events", ({"namespace": namespace} if namespace else {})

    if "pod" in m:
        return "get_pods", ({"namespace": namespace} if namespace else {})

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

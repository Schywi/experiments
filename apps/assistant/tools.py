"""Read-only Kubernetes tools for the assistant (Milestones 2-3).

Each tool is a narrow, typed function with a risk level. The assistant never
shells out; it calls the Kubernetes API as its own ServiceAccount, whose
ClusterRole (chart/templates/rbac.yaml) limits it to get/list/watch on a fixed
resource set (plus get on pods/log).
"""

import re
from dataclasses import dataclass
from typing import Any, Callable, Optional

from k8sutil import _core, audit  # audit re-exported for callers/tests


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


from actions import request as request_action  # M9
from knowledge import search_knowledge  # M6
from memory import recall as recall_notes, remember  # M10
from observability import query_prometheus, search_logs  # M5


def propose_action(action: str, args: Optional[dict] = None) -> dict:
    """Propose a level-2 action; returns a confirmation token. Does NOT execute.

    Execution requires a separate human POST to /actions/confirm with the token.
    """
    return request_action(action, args or {})


REGISTRY: dict = {
    "get_pods": Tool("get_pods", 1, "List pods and their status", get_pods),
    "get_pod_logs": Tool("get_pod_logs", 1, "Read recent logs from a pod", get_pod_logs),
    "get_events": Tool("get_events", 1, "List recent cluster events", get_events),
    "query_prometheus": Tool(
        "query_prometheus", 1,
        "Run a read-only PromQL query against VictoriaMetrics", query_prometheus,
    ),
    "search_logs": Tool(
        "search_logs", 1,
        "Search recent Pod logs in a namespace for a substring", search_logs,
    ),
    "search_knowledge": Tool(
        "search_knowledge", 1,
        "Search the local documentation corpus for relevant passages", search_knowledge,
    ),
    "propose_action": Tool(
        "propose_action", 2,
        "Propose an operational action (restart/scale) for human confirmation",
        propose_action,
    ),
    "remember": Tool(
        "remember", 1,
        "Store a durable note the user asked to keep", remember,
    ),
    "recall": Tool(
        "recall", 1,
        "Recall stored notes relevant to a query", recall_notes,
    ),
}


# --- routing ----------------------------------------------------------------
# Milestone-3 router: keyword rules with light argument extraction. The Laya
# decision engine replaces this in a later milestone; the interface
# (message -> (tool, kwargs) | None) stays the same.

_NS_RE = re.compile(r"\b(?:in|namespace)\s+([a-z0-9][a-z0-9-]*)")
_POD_RE = re.compile(r"\b(?:pod|pods)\s+([a-z0-9][a-z0-9.-]*)")
_NSNAME_RE = re.compile(r"\b(?:namespace|ns)\s+([a-z0-9][a-z0-9-]*)")
_DEPLOY_RE = re.compile(r"\b(?:deployment|deploy)\s+([a-z0-9][a-z0-9.-]*)")
_NUM_RE = re.compile(r"\b(?:to|replicas)\s+(\d+)")

# M5 curated intent->PromQL catalog. The model never authors PromQL; these
# queries are reviewed and bounded (a single instant query, trimmed server-side).
_METRIC_CATALOG = (
    (("drop", "drops", "dropped", "packet loss"),
     'sum(rate(hubble_drop_total[5m])) by (reason)'),
    (("dns",),
     'sum(rate(hubble_dns_responses_total{rcode!="NOERROR"}[5m])) by (rcode)'),
    (("http", "latency", "5xx", "requests"),
     'sum(rate(hubble_http_requests_total[5m])) by (destination_workload, status)'),
    (("flow", "traffic", "throughput"),
     'sum(rate(hubble_flows_processed_total[5m])) by (verdict)'),
    (("datapath", "bpf"),
     'sum(rate(cilium_drop_count_total[5m])) by (reason)'),
)


def _metric_intent(message: str):
    for needles, promql in _METRIC_CATALOG:
        if any(n in message for n in needles):
            return "query_prometheus", {"query": promql}
    return None


def route(message: str) -> Optional[tuple]:
    m = message.lower()
    ns_match = _NS_RE.search(m)
    namespace = ns_match.group(1) if ns_match else None

    # M5: metric questions route to the curated catalog (unless it's a log ask).
    if "log" not in m and any(
        n in m for needles, _ in _METRIC_CATALOG for n in needles
    ):
        intent = _metric_intent(m)
        if intent is not None:
            return intent

    # M5: an explicit "search ... logs in <ns> for <x>" goes to search_logs.
    if "log" in m and "search" in m:
        ns = _NSNAME_RE.search(m) or _NS_RE.search(m)
        return "search_logs", {
            "namespace": ns.group(1) if ns else "default",
            "contains": m.split("for", 1)[1].strip() if "for" in m else "",
        }

    if "log" in m:
        pod_match = _POD_RE.search(m)
        if pod_match:
            return "get_pod_logs", {"namespace": namespace or "default", "pod": pod_match.group(1)}
        return None  # a pod name is required; let the model ask for it

    if "event" in m:
        return "get_events", ({"namespace": namespace} if namespace else {})

    if "pod" in m:
        return "get_pods", ({"namespace": namespace} if namespace else {})

    # M10: explicit memory asks.
    if m.startswith("remember"):
        note = message.split("remember", 1)[1].strip(" :,-")
        if note:
            return "remember", {"text": note}
    if "what do you remember" in m or m.startswith("recall"):
        return "recall", {"query": message}

    # M9: propose an operational action. propose_action only mints a
    # confirmation token; nothing runs until a human POSTs /actions/confirm.
    if "restart" in m or "scale" in m:
        dep = _DEPLOY_RE.search(m)
        if dep:
            ns = _NSNAME_RE.search(m) or _NS_RE.search(m)
            ns_name = ns.group(1) if ns else "default"
            if "scale" in m:
                num = _NUM_RE.search(m)
                if num:
                    return "propose_action", {"action": "scale_deployment",
                        "args": {"namespace": ns_name, "name": dep.group(1),
                                 "replicas": int(num.group(1))}}
            if "restart" in m:
                return "propose_action", {"action": "restart_deployment",
                    "args": {"namespace": ns_name, "name": dep.group(1)}}

    # M6: documentation/teaching questions fall back to the knowledge corpus.
    if any(k in m for k in ("explain", "how does", "how do", "what is",
                            "architecture", "runbook", "documentation",
                            "why does", "how is")):
        return "search_knowledge", {"query": message}

    return None

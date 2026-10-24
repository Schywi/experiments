"""Read-only Kubernetes tools for the assistant (Milestones 2-3).

Each tool is a narrow, typed function with a risk level. The assistant never
shells out; it calls the Kubernetes API as its own ServiceAccount, whose
ClusterRole (chart/templates/rbac.yaml) limits it to get/list/watch on a fixed
resource set (plus get on pods/log).
"""

import os
import re
from dataclasses import dataclass
from typing import Any, Callable, Optional

from kubernetes import client

from k8sutil import _client, _core, audit  # audit re-exported for callers/tests


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


# --- level 1: structural topology (Kubernetes API) --------------------------

MAX_TOPOLOGY_NODES = int(os.environ.get("TOPOLOGY_MAX_NODES", "150"))
MAX_TOPOLOGY_EDGES = int(os.environ.get("TOPOLOGY_MAX_EDGES", "250"))


def get_topology(namespace: Optional[str] = None) -> dict:
    """A bounded STRUCTURAL map of the cluster: workloads, services, pods and
    ingresses, plus the ownership/selection/routing edges between them.

    Structure only -- observed network flows are Cilium/Hubble's domain and are
    deliberately not rebuilt here. `scope` is a namespace, or "all"."""
    core = _client(client.CoreV1Api)
    apps = _client(client.AppsV1Api)
    net = _client(client.NetworkingV1Api)

    if namespace:
        deps = apps.list_namespaced_deployment(namespace).items
        rss = apps.list_namespaced_replica_set(namespace).items
        svcs = core.list_namespaced_service(namespace).items
        pods = core.list_namespaced_pod(namespace).items
        eps = core.list_namespaced_endpoints(namespace).items
        ings = net.list_namespaced_ingress(namespace).items
    else:
        deps = apps.list_deployment_for_all_namespaces().items
        rss = apps.list_replica_set_for_all_namespaces().items
        svcs = core.list_service_for_all_namespaces().items
        pods = core.list_pod_for_all_namespaces().items
        eps = core.list_endpoints_for_all_namespaces().items
        ings = net.list_ingress_for_all_namespaces().items

    nodes: dict = {}
    edges: list = []

    def node_id(kind, ns, name):
        return f"{kind}/{ns}/{name}"

    def add_node(nid, **extra):
        if nid not in nodes:
            kind, ns, name = nid.split("/", 2)
            nodes[nid] = {"id": nid, "kind": kind, "ns": ns, "name": name, **extra}
        return nid

    def add_edge(t, a, b):
        if a in nodes and b not in nodes:
            add_node(b)          # keep edges pointing at a real node id
        if a and b and {"t": t, "from": a, "to": b} not in edges:
            edges.append({"t": t, "from": a, "to": b})

    rs_to_dep = {}
    for rs in rss:
        for o in (rs.metadata.owner_references or []):
            if o.kind == "Deployment":
                rs_to_dep[(rs.metadata.namespace, rs.metadata.name)] = o.name

    for d in deps:
        add_node(node_id("Deployment", d.metadata.namespace, d.metadata.name),
                 ready=f"{d.status.ready_replicas or 0}/{d.spec.replicas or 0}")
    for s in svcs:
        add_node(node_id("Service", s.metadata.namespace, s.metadata.name), type=s.spec.type)
    for i in ings:
        add_node(node_id("Ingress", i.metadata.namespace, i.metadata.name))
    for p in pods:
        add_node(node_id("Pod", p.metadata.namespace, p.metadata.name),
                 phase=p.status.phase, node=p.spec.node_name,
                 restarts=sum((c.restart_count or 0) for c in (p.status.container_statuses or [])))

    for p in pods:
        pid = node_id("Pod", p.metadata.namespace, p.metadata.name)
        for o in (p.metadata.owner_references or []):
            if o.kind == "ReplicaSet":
                dep = rs_to_dep.get((p.metadata.namespace, o.name))
                if dep:
                    add_edge("owns", node_id("Deployment", p.metadata.namespace, dep), pid)
            elif o.kind in ("StatefulSet", "DaemonSet", "Job"):
                add_edge("owns", node_id(o.kind, p.metadata.namespace, o.name), pid)

    for e in eps:
        sid = node_id("Service", e.metadata.namespace, e.metadata.name)
        if sid not in nodes:
            continue
        for sub in (e.subsets or []):
            for addr in (sub.addresses or []):
                tr = addr.target_ref
                if tr and tr.kind == "Pod":
                    add_edge("selects", sid, node_id("Pod", e.metadata.namespace, tr.name))

    for i in ings:
        iid = node_id("Ingress", i.metadata.namespace, i.metadata.name)
        for rule in (i.spec.rules or []):
            for path in ((rule.http.paths if rule.http else []) or []):
                bk = path.backend.service if path.backend else None
                if bk:
                    add_edge("routes", iid, node_id("Service", i.metadata.namespace, bk.name))

    unhealthy = sum(1 for p in pods if p.status.phase not in ("Running", "Succeeded"))
    return {
        "scope": namespace or "all",
        "rollup": {"deployments": len(deps), "services": len(svcs), "pods": len(pods),
                   "ingresses": len(ings), "unhealthy_pods": unhealthy},
        "nodes": list(nodes.values())[:MAX_TOPOLOGY_NODES],
        "edges": edges[:MAX_TOPOLOGY_EDGES],
        "truncated": len(nodes) > MAX_TOPOLOGY_NODES or len(edges) > MAX_TOPOLOGY_EDGES,
    }


# --- level 1: Cartography snapshot (Neo4j graph) ----------------------------

NEO4J_URL = os.environ.get(
    "NEO4J_URL", "bolt://cartography-neo4j.cartography.svc.cluster.local:7687")
NEO4J_DATABASE = os.environ.get("NEO4J_DATABASE", "neo4j")
CARTOGRAPHY_NS = os.environ.get("CARTOGRAPHY_NAMESPACE", "cartography")

# Curated, reviewed Cypher only -- the model never authors queries. Bounded LIMITs.
_SNAPSHOT_CYPHER = {
    "inventory": ("MATCH (n) UNWIND labels(n) AS label RETURN label, count(*) AS nodes "
                  "ORDER BY nodes DESC LIMIT 100"),
    "workloads": ("MATCH (d:KubernetesDeployment) "
                  "RETURN d.namespace AS namespace, d.name AS deployment "
                  "ORDER BY namespace, deployment LIMIT 100"),
    "services": ("MATCH (s:KubernetesService) "
                 "RETURN s.namespace AS namespace, s.name AS service LIMIT 100"),
}


def _snapshot_cypher(question: str) -> str:
    m = (question or "").lower()
    if "workload" in m or "deployment" in m or "pod" in m:
        return _SNAPSHOT_CYPHER["workloads"]
    if "service" in m or "connect" in m or "edge" in m or "talk" in m:
        return _SNAPSHOT_CYPHER["services"]
    return _SNAPSHOT_CYPHER["inventory"]


def _cartography_ingest_time():
    """The Cartography CronJob's last successful run (the snapshot's real age)."""
    try:
        batch = _client(client.BatchV1Api)
        cj = batch.read_namespaced_cron_job("cartography", CARTOGRAPHY_NS)
        t = cj.status.last_successful_time if cj.status else None
        return t.isoformat() if t else None
    except Exception:
        return None


def get_topology_snapshot(question: str = "") -> dict:
    """Query the Cartography Neo4j graph (rebuilt every ~6h by a CronJob).

    Read-only, with a curated Cypher catalog. Returns its **last ingest time** so
    a caller never mistakes the snapshot's age for 'now'."""
    from neo4j import GraphDatabase
    cypher = _snapshot_cypher(question)
    audit({"event": "cartography_query", "cypher": cypher})
    driver = GraphDatabase.driver(NEO4J_URL, auth=None)   # NEO4J_AUTH=none
    try:
        with driver.session(database=NEO4J_DATABASE) as session:
            rows = [rec.data() for rec in session.run(cypher)][:100]
    finally:
        driver.close()
    # cypher + ingest_time FIRST: the pipeline truncates each fact's evidence,
    # and the snapshot's age must survive that truncation.
    return {"cypher": cypher, "ingest_time": _cartography_ingest_time(),
            "rows": rows, "database": NEO4J_DATABASE}


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
    "get_topology": Tool(
        "get_topology", 1,
        "Live structural map (k8s API): workloads, services, pods, ingresses, and "
        "the ownership/selection/routing edges between them. Not network flows.",
        get_topology,
    ),
    "get_topology_snapshot": Tool(
        "get_topology_snapshot", 1,
        "Query the Cartography Neo4j graph (rebuilt ~6h; can be stale). Reports its "
        "last ingest time. Use for the ingested asset/relationship graph.",
        get_topology_snapshot,
    ),
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

    # Topology: live structure (k8s API) vs the ingested Cartography snapshot.
    if "topolog" in m or "service map" in m:
        return "get_topology", ({"namespace": namespace} if namespace else {})
    if "snapshot" in m or "cartograph" in m or "ingested" in m:
        return "get_topology_snapshot", {"question": message}

    # M6: documentation/teaching questions fall back to the knowledge corpus.
    if any(k in m for k in ("explain", "how does", "how do", "what is",
                            "architecture", "runbook", "documentation",
                            "why does", "how is")):
        return "search_knowledge", {"query": message}

    return None

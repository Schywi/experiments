"""Milestone 5: observability tools for the assistant (read-only, level 1).

  query_prometheus(query, time=None)           -> VictoriaMetrics /api/v1/query
  search_logs(namespace, contains, tail_lines) -> filtered pod logs

No shell, no writes. Only HTTP GET to the in-cluster VictoriaMetrics
(config/victoriametrics: namespace `observability`, Service `victoriametrics`,
port 8428), and the Kubernetes API as the Pod's own ServiceAccount. Reuses the
existing JSONL audit sink from tools.py.
"""

import os
from typing import Optional

import httpx

from k8sutil import _core, audit  # shared in-cluster client + audit sink

VM_URL = os.environ.get(
    "VM_URL", "http://victoriametrics.observability.svc.cluster.local:8428"
).rstrip("/")
VM_QUERY_TIMEOUT = float(os.environ.get("VM_QUERY_TIMEOUT", "5"))
VM_MAX_SERIES = int(os.environ.get("VM_MAX_SERIES", "50"))
LOG_MAX_HITS = int(os.environ.get("LOG_MAX_HITS", "200"))


def query_prometheus(query: str, time: Optional[str] = None) -> dict:
    """Run one read-only instant PromQL query against VictoriaMetrics.

    `query` is expected to come from the curated catalog in tools.route() (the
    model does not author PromQL); it is issued verbatim as a read-only GET and
    the result is trimmed to VM_MAX_SERIES.
    """
    params = {"query": query}
    if time:
        params["time"] = time
    audit({"event": "prom_query", "query": query})
    with httpx.Client(timeout=VM_QUERY_TIMEOUT) as client:
        resp = client.get(f"{VM_URL}/api/v1/query", params=params)
        resp.raise_for_status()
        data = resp.json()
    results = data.get("data", {}).get("result", []) or []
    series = []
    for item in results[:VM_MAX_SERIES]:
        metric = {k: v for k, v in item.get("metric", {}).items() if k != "__name__"}
        value = (item.get("value") or [None, None])[1]
        series.append({"metric": metric, "value": value})
    return {
        "query": query,
        "count": len(results),
        "truncated": len(results) > VM_MAX_SERIES,
        "series": series,
    }


def search_logs(namespace: str, contains: str, tail_lines: int = 200) -> dict:
    """Case-insensitively search the recent logs of every Pod in a namespace.

    Bounded: at most LOG_MAX_HITS matching lines are returned, and each line is
    clipped. A pod whose logs cannot be read is skipped, not fatal.
    """
    v1 = _core()
    needle = contains.lower()
    hits = []
    truncated = False
    for pod in v1.list_namespaced_pod(namespace).items:
        name = pod.metadata.name
        try:
            log = v1.read_namespaced_pod_log(
                name=name, namespace=namespace, tail_lines=tail_lines
            )
        except Exception:
            continue
        for line in log.splitlines():
            if needle in line.lower():
                hits.append({"pod": name, "line": line[:400]})
                if len(hits) >= LOG_MAX_HITS:
                    truncated = True
                    break
        if truncated:
            break
    return {
        "namespace": namespace,
        "contains": contains,
        "count": len(hits),
        "truncated": truncated,
        "hits": hits,
    }

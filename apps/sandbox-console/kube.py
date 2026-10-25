"""Read-only Kubernetes access for the console.

Three list calls per refresh, all namespaced and all GET: the Sandboxes, the
SandboxClaims that own them, and the pods behind them. Pod metrics come from the
metrics-server API (`metrics.k8s.io/v1beta1`) when it answers; a cluster without
it degrades to tiles with no memory number instead of an error page.

Every RBAC verb this module exercises is listed in `chart/templates/rbac.yaml`.
Nothing here creates, updates, deletes or execs.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

from kubernetes import client, config

import inventory

SANDBOX_GROUP = "agents.x-k8s.io"
EXTENSIONS_GROUP = "extensions.agents.x-k8s.io"
API_VERSION = "v1beta1"
METRICS_GROUP = "metrics.k8s.io"

TOKEN_FILE = "/var/run/secrets/kubernetes.io/serviceaccount/token"


@dataclass
class Snapshot:
    """One refresh worth of cluster state plus whatever failed to arrive."""

    sandboxes: list[dict] = field(default_factory=list)
    claims: list[dict] = field(default_factory=list)
    pods: list[dict] = field(default_factory=list)
    usage: dict[str, dict] = field(default_factory=dict)
    errors: list[str] = field(default_factory=list)


_clients: dict = {}


def _client(cls):
    """Cached in-cluster API client for the given class."""
    if cls not in _clients:
        try:
            config.load_incluster_config()
        except config.ConfigException:
            config.load_kube_config()
        _clients[cls] = cls()
    return _clients[cls]


def client_for(cls):
    """Public alias for a cached in-cluster client (lifecycle.py writes with it)."""
    return _client(cls)


def router_token() -> str:
    """The console's own ServiceAccount token, for the router's TokenReview.

    Read from the projected volume rather than `Authorization` headers: the
    router authenticates the *caller*, and the console is the caller.
    """
    from_env = os.environ.get("SANDBOX_ROUTER_TOKEN", "").strip()
    if from_env:
        return from_env
    try:
        with open(TOKEN_FILE, encoding="utf-8") as handle:
            return handle.read().strip()
    except OSError:
        return ""


def _list_custom(group: str, plural: str, namespace: str) -> list[dict]:
    api = _client(client.CustomObjectsApi)
    response = api.list_namespaced_custom_object(group, API_VERSION, namespace, plural)
    return list((response or {}).get("items") or [])


def snapshot(namespace: str) -> Snapshot:
    """Collect everything the grid needs. Never raises: errors are reported."""
    result = Snapshot()
    try:
        result.sandboxes = _list_custom(SANDBOX_GROUP, "sandboxes", namespace)
    except Exception as exc:  # noqa: BLE001 - surfaced on the page, not swallowed
        result.errors.append(f"sandboxes: {_short(exc)}")
    try:
        result.claims = _list_custom(EXTENSIONS_GROUP, "sandboxclaims", namespace)
    except Exception as exc:  # noqa: BLE001
        result.errors.append(f"sandboxclaims: {_short(exc)}")
    try:
        listing = _client(client.CoreV1Api).list_namespaced_pod(namespace)
        result.pods = [_pod_summary(pod) for pod in (listing.items or [])]
    except Exception as exc:  # noqa: BLE001
        result.errors.append(f"pods: {_short(exc)}")
    try:
        items = _list_custom(METRICS_GROUP, "pods", namespace)
        result.usage = inventory.build_usage(items)
    except Exception as exc:  # noqa: BLE001 - metrics-server is optional
        result.errors.append(f"metrics: {_short(exc)}")
    return result


def nodes() -> list[dict]:
    """Allocatable per node, for the capacity recipe."""
    try:
        listing = _client(client.CoreV1Api).list_node()
    except Exception:  # noqa: BLE001 - capacity is advisory, never fatal
        return []
    result = []
    for node in listing.items or []:
        allocatable = getattr(getattr(node, "status", None), "allocatable", None) or {}
        result.append({"name": getattr(getattr(node, "metadata", None), "name", "?"), "allocatable": dict(allocatable)})
    return result


def pods(namespace: str) -> list[dict]:
    """Every pod in the namespace, projected (see `_pod_summary`)."""
    try:
        listing = _client(client.CoreV1Api).list_namespaced_pod(namespace)
        return [_pod_summary(item) for item in (listing.items or [])]
    except Exception:  # noqa: BLE001 - an unreadable list is an empty list here
        return []


def pod(namespace: str, name: str) -> dict | None:
    """One pod, for the spec-derived half of the isolation badge.

    Read-only `get`; the pod spec is the only place `automountServiceAccountToken`
    and `dnsPolicy` are recorded.
    """
    try:
        return _pod_summary(_client(client.CoreV1Api).read_namespaced_pod(name, namespace))
    except Exception:  # noqa: BLE001 - a missing pod is a badge that says unknown
        return None


def _pod_summary(pod: Any) -> dict:
    """Project the four pod fields the console reads, in the CRD's key style.

    `V1Pod.to_dict()` returns snake_case (`dns_policy`,
    `automount_service_account_token`), while every Sandbox/Claim dict comes from
    `CustomObjectsApi` in camelCase. Mixing the two made the isolation badge
    report the *unhardened* defaults for a hardened pod: the keys it looked for
    were simply absent. This projection keeps one vocabulary.
    """
    metadata = getattr(pod, "metadata", None)
    spec = getattr(pod, "spec", None)
    status = getattr(pod, "status", None)
    dns_config = getattr(spec, "dns_config", None) if spec is not None else None
    return {
        "metadata": {
            "name": getattr(metadata, "name", None),
            "labels": dict(getattr(metadata, "labels", None) or {}),
        },
        "spec": {
            "nodeName": getattr(spec, "node_name", None),
            "dnsPolicy": getattr(spec, "dns_policy", None),
            "dnsConfig": {"nameservers": list(getattr(dns_config, "nameservers", None) or [])},
            "automountServiceAccountToken": getattr(spec, "automount_service_account_token", None),
        },
        "status": {"podIP": getattr(status, "pod_ip", None)},
    }


def _short(exc: Exception) -> str:
    text = str(exc).strip().splitlines()
    return text[0][:200] if text else exc.__class__.__name__

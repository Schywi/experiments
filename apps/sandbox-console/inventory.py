"""Inventory of live Sandboxes, assembled from CRD status.

Everything here is a pure function over the dicts the Kubernetes API returns, so
the grid can be tested against recorded fixtures without a cluster. There is no
new storage: a tile is a projection of `Sandbox.status`, the claim that owns it,
and the pod the controller created.

Two facts about the substrate shape this module:

* A warm-pool member that no `SandboxClaim` has adopted keeps the
  `agents.x-k8s.io/warm-pool-sandbox` label, and the sandbox-router deliberately
  leaves those out of its name index (``sandbox-router/cache/cache.go``). They
  are unroutable by name, so they are counted in the header and never drawn as
  tiles -- a tile whose screenshot can only 502 is worse than no tile.
* Adoption deletes that label (``extensions/controllers/sandboxclaim_controller.go``),
  so "has the label" is the whole test for "unclaimed pool member".
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field, replace
from datetime import datetime, timezone
from typing import Any

WARM_POOL_LABEL = "agents.x-k8s.io/warm-pool-sandbox"
TEMPLATE_REF_HASH_LABEL = "agents.x-k8s.io/sandbox-template-ref-hash"
TEMPLATE_REF_ANNOTATION = "agents.x-k8s.io/sandbox-template-ref"
LAUNCH_TYPE_LABEL = "agents.x-k8s.io/launch-type"
CREATED_BY_LABEL = "agents.x-k8s.io/created-by"
SANDBOX_NAME_HASH_LABEL = "agents.x-k8s.io/sandbox-name-hash"
READY_CONDITION = "Ready"

# Ready=False reasons mapped to the word the tile shows. Anything unlisted is
# reported lowercased, so a new upstream reason is visible rather than hidden.
_PHASE_BY_REASON = {
    "DependenciesNotReady": "pending",
    "PodSucceeded": "exited",
    "PodFailed": "failed",
    "SandboxExpired": "expired",
    "SandboxSuspended": "suspended",
    "ReconcilerError": "error",
    "InvalidConfiguration": "invalid",
}

_DECIMAL = {"n": 1e-9, "u": 1e-6, "m": 1e-3, "": 1.0, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18}
_BINARY = {"Ki": 2**10, "Mi": 2**20, "Gi": 2**30, "Ti": 2**40, "Pi": 2**50, "Ei": 2**60}
_QUANTITY = re.compile(r"^(?P<number>[+-]?\d+(?:\.\d+)?)(?P<suffix>[a-zA-Z]*)$")


def parse_quantity(value: Any) -> float | None:
    """Parse a Kubernetes resource quantity into base units (cores, bytes).

    Handles the binary suffixes metrics-server emits (`Ki`, `Mi`) and the
    decimal ones (`m`, `M`). Kept as a float because `42m` is 0.042 cores --
    truncating it to an integer loses the whole value. Use `cpu_millicores` and
    `memory_bytes` for the integer form each field needs. Returns None for
    anything unrecognised instead of guessing, so a missing metric reads as
    missing.
    """
    if value is None or isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    match = _QUANTITY.match(str(value).strip())
    if not match:
        return None
    number = float(match.group("number"))
    suffix = match.group("suffix")
    if suffix in _BINARY:
        return number * _BINARY[suffix]
    if suffix in _DECIMAL:
        return number * _DECIMAL[suffix]
    return None


def cpu_millicores(value: Any) -> int | None:
    """CPU in millicores: `42m` -> 42, `1.5` -> 1500."""
    parsed = parse_quantity(value)
    return None if parsed is None else int(round(parsed * 1000))


def memory_bytes(value: Any) -> int | None:
    """Memory in bytes: `256000Ki` -> 262144000."""
    parsed = parse_quantity(value)
    return None if parsed is None else int(parsed)


def parse_time(value: Any) -> datetime | None:
    """Parse an RFC3339 timestamp from the API into an aware datetime."""
    if not isinstance(value, str) or not value:
        return None
    text = value.replace("Z", "+00:00")
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed


def condition(conditions: list | None, name: str) -> dict | None:
    for item in conditions or []:
        if isinstance(item, dict) and item.get("type") == name:
            return item
    return None


def phase_of(sandbox: dict) -> str:
    """Collapse the Ready condition into the word the tile shows."""
    status = sandbox.get("status") or {}
    ready = condition(status.get("conditions"), READY_CONDITION)
    if not ready:
        return "unknown"
    if str(ready.get("status")) == "True":
        return "ready"
    reason = str(ready.get("reason") or "")
    return _PHASE_BY_REASON.get(reason, reason.lower() or "notready")


def name_hash(label_selector: str | None) -> str | None:
    """Extract the value from the Sandbox's pod selector.

    The term is a single `key=value` pair (`controllers/sandbox_controller.go`),
    never a set. The field is `status.selector` in v1.0.7 and
    `status.labelSelector` in the newer API type, so callers pass whichever the
    object carries.
    """
    if not label_selector or "=" not in label_selector:
        return None
    key, _, value = label_selector.partition("=")
    return value if key.strip() == SANDBOX_NAME_HASH_LABEL else None


@dataclass(frozen=True)
class Tile:
    """One sandbox as the grid draws it."""

    id: str
    phase: str
    age_s: int
    pod_ip: str | None = None
    node: str | None = None
    template: str | None = None
    pool: str | None = None
    claim: str | None = None
    launch_type: str | None = None
    created_by: str | None = None
    pod_name: str | None = None
    mem_working_set_bytes: int | None = None
    cpu_millicores: int | None = None
    shutdown_at: str | None = None
    reachability: dict | None = None
    screenshot_ts: int = 0
    note: str | None = None
    # The page the sandbox's browser is on. Learned from the browser itself
    # (`location.href`), not from the template, so it reflects a navigation the
    # pane performed and disappears with the pod.
    url: str | None = None
    # Browser contexts inside this Chromium, read from the pod's own browser.
    # `None` means the browser could not answer (a bridge from before browser
    # contexts exists answers "Not allowed"), which is a different fact from a
    # browser that is serving no sessions.
    contexts: int | None = None

    @property
    def ready(self) -> bool:
        return self.phase == "ready"

    def replace(self, **changes) -> "Tile":
        return replace(self, **changes)

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "phase": self.phase,
            "age_s": self.age_s,
            "pod_ip": self.pod_ip,
            "node": self.node,
            "template": self.template,
            "pool": self.pool,
            "claim": self.claim,
            "launch_type": self.launch_type,
            "created_by": self.created_by,
            "mem_working_set_bytes": self.mem_working_set_bytes,
            "cpu_millicores": self.cpu_millicores,
            "shutdown_at": self.shutdown_at,
            "reachability": self.reachability,
            "screenshot_ts": self.screenshot_ts,
            "note": self.note,
            "url": self.url,
            "contexts": self.contexts,
        }


@dataclass
class Report:
    """The whole screen: tiles plus the counters in the header."""

    tiles: list[Tile] = field(default_factory=list)
    pool_idle: int = 0
    pool_ready: int = 0
    pool_total: int = 0
    running: int = 0
    pending: int = 0
    other: int = 0
    mem_working_set_bytes: int = 0
    cpu_millicores: int = 0
    truncated: int = 0
    errors: list[str] = field(default_factory=list)
    collected_at: float = 0.0


def _pod_index(pods: list[dict]) -> tuple[dict[str, dict], dict[str, dict]]:
    """Index pods by sandbox-name hash and by pod IP.

    Two keys because neither is always present: the hash label comes from the
    controller and the IP comes from the pod once it is scheduled.
    """
    by_hash: dict[str, dict] = {}
    by_ip: dict[str, dict] = {}
    for pod in pods:
        labels = ((pod.get("metadata") or {}).get("labels") or {})
        hashed = labels.get(SANDBOX_NAME_HASH_LABEL)
        if hashed:
            by_hash[hashed] = pod
        ip = ((pod.get("status") or {}).get("podIP"))
        if ip:
            by_ip[ip] = pod
    return by_hash, by_ip


def _claim_index(claims: list[dict]) -> dict[str, str]:
    """Map sandbox name -> claim name, for the tile's `claim` field."""
    index = {}
    for claim in claims:
        name = claim.get("metadata", {}).get("name")
        bound = ((claim.get("status") or {}).get("sandbox") or {}).get("name")
        if name and bound:
            index[bound] = name
    return index


def build_report(
    sandboxes: list[dict],
    claims: list[dict] | None = None,
    pods: list[dict] | None = None,
    usage: dict[str, dict] | None = None,
    now: datetime | None = None,
    max_tiles: int = 12,
) -> Report:
    """Assemble the header counters and the tile list.

    Tiles are built for every sandbox that is not an unclaimed warm-pool member,
    in name order, capped at ``max_tiles``; the remainder is reported as
    ``truncated`` so the count in the header still adds up.
    """
    now = now or datetime.now(timezone.utc)
    pods_by_hash, pods_by_ip = _pod_index(pods or [])
    claims_by_sandbox = _claim_index(claims or [])
    usage = usage or {}

    report = Report(collected_at=now.timestamp())
    candidates: list[Tile] = []

    for sandbox in sandboxes:
        metadata = sandbox.get("metadata") or {}
        name = metadata.get("name")
        if not name:
            continue
        labels = metadata.get("labels") or {}
        annotations = metadata.get("annotations") or {}
        status = sandbox.get("status") or {}

        is_pool_member = WARM_POOL_LABEL in labels
        claim_name = claims_by_sandbox.get(name)
        report.pool_total += 1 if is_pool_member else 0
        if is_pool_member and not claim_name:
            report.pool_idle += 1
            # An idle member that is not Ready is not warmth, it is a pull or a
            # readiness failure wearing the pool's clothes. Counting the two
            # apart is what makes "8 pool idle" stop being a lie.
            if phase_of(sandbox) == "ready":
                report.pool_ready += 1
            continue

        # v1.0.7 calls the field `selector`; the newer API type calls it
        # `labelSelector`. Read both, then fall back to the pod IP, because a
        # controller that omits the selector would otherwise leave every tile
        # without memory, CPU or a pod name to read the spec from.
        hashed = name_hash(status.get("labelSelector") or status.get("selector"))
        pod_ips = status.get("podIPs") or []
        cluster_ip = pod_ips[0] if pod_ips else None
        pod = None
        if hashed:
            pod = pods_by_hash.get(hashed)
        if pod is None and cluster_ip:
            pod = pods_by_ip.get(cluster_ip)
        pod_name = (pod or {}).get("metadata", {}).get("name")
        pod_usage = usage.get(pod_name or "", {})

        created = parse_time(metadata.get("creationTimestamp"))
        if cluster_ip is None and pod is not None:
            cluster_ip = ((pod.get("status") or {}).get("podIP"))

        tile = Tile(
            id=name,
            phase=phase_of(sandbox),
            age_s=int((now - created).total_seconds()) if created else 0,
            pod_ip=cluster_ip,
            node=status.get("nodeName") or ((pod or {}).get("spec", {}).get("nodeName")),
            template=annotations.get(TEMPLATE_REF_ANNOTATION) or labels.get(TEMPLATE_REF_HASH_LABEL),
            pool=None if is_pool_member else None,
            claim=claim_name,
            launch_type=labels.get(LAUNCH_TYPE_LABEL),
            created_by=labels.get(CREATED_BY_LABEL),
            pod_name=pod_name,
            mem_working_set_bytes=pod_usage.get("mem_working_set_bytes"),
            cpu_millicores=pod_usage.get("cpu_millicores"),
            shutdown_at=((sandbox.get("spec") or {}).get("shutdownTime")),
        )
        candidates.append(tile)

        if tile.ready:
            report.running += 1
        elif tile.phase in {"pending", "unknown"}:
            report.pending += 1
        else:
            report.other += 1
        if tile.mem_working_set_bytes:
            report.mem_working_set_bytes += tile.mem_working_set_bytes
        if tile.cpu_millicores:
            report.cpu_millicores += tile.cpu_millicores

    candidates.sort(key=lambda tile: tile.id)
    report.tiles = candidates[:max_tiles]
    report.truncated = max(0, len(candidates) - max_tiles)
    return report


def build_usage(metrics_items: list[dict]) -> dict[str, dict]:
    """Convert a `metrics.k8s.io/v1beta1` pods list into tile-ready numbers.

    PodMetrics carries one entry per container (`containers[].usage`), so the
    numbers are summed across the pod's containers. A pod with a single container
    is the usual case here; summing keeps a future sidecar from silently
    reporting only one of them. A top-level `usage` map is accepted too, because
    that is the shape `kubectl top` flattens to and the shape tests often fake.
    """
    usage: dict[str, dict] = {}
    for item in metrics_items or []:
        name = ((item.get("metadata") or {}).get("name"))
        if not name:
            continue
        cpu_total: int | None = None
        mem_total: int | None = None
        for container in item.get("containers") or []:
            raw = container.get("usage") or {}
            container_cpu = cpu_millicores(raw.get("cpu"))
            container_mem = memory_bytes(raw.get("memory"))
            if container_cpu is not None:
                cpu_total = (cpu_total or 0) + container_cpu
            if container_mem is not None:
                mem_total = (mem_total or 0) + container_mem
        if cpu_total is None and mem_total is None:
            raw = item.get("usage") or {}
            cpu_total = cpu_millicores(raw.get("cpu"))
            mem_total = memory_bytes(raw.get("memory"))
        usage[name] = {"cpu_millicores": cpu_total, "mem_working_set_bytes": mem_total}
    return usage

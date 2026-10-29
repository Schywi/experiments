"""Create, delete and time sandboxes (M4, and the timing half of M6).

Everything the pane does to the cluster passes through here, and every write is
one of exactly two kinds: a `SandboxClaim` against the warm pool, or — only when
`allow_raw_sandbox` is set — a bare `kind: Sandbox` with the same pod shape but
none of the hardening. Nothing else in the cluster is mutable from the console.

The four timers are measured here, server-side, from one clock:

    t_sched  POST -> the Sandbox reports PodScheduled=True
    t_ready  POST -> Ready=True
    t_cdp    POST -> GET :9222/json/version answers through the router
    t_paint  POST -> the first screenshot whose bytes exceed a blank-page floor
"""

from __future__ import annotations

import asyncio
import re
import time
from dataclasses import dataclass, field
from typing import Any

from kubernetes import client

import cdp
import config
import kube
import records

# Stamped on everything the pane creates, so teardown removes only its own work.
MANAGED_LABEL = "sandbox-console"

SANDBOX_GROUP = "agents.x-k8s.io"
EXTENSIONS_GROUP = "extensions.agents.x-k8s.io"
VERSION = "v1beta1"

# Below this, a PNG is a blank page (a 1280x600 white canvas compresses to a few
# hundred bytes). The RFC calls for a floor; this is it.
BLANK_FRAME_FLOOR = 5000


def safe_name(prefix: str) -> str:
    """A DNS-1123 label short enough that the derived Service name still fits."""
    suffix = base64ish(int(time.time() * 1000) % 1296**4)
    return f"{prefix}-{suffix}".lower()


def base64ish(value: int) -> str:
    alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
    digits = []
    while value:
        value, remainder = divmod(value, len(alphabet))
        digits.append(alphabet[remainder])
    return "".join(reversed(digits)) or "0"


@dataclass
class Created:
    """What a create call produced, before anything is ready."""

    kind: str  # "claim" | "sandbox"
    name: str
    namespace: str
    started_at: float
    sandbox: str | None = None
    timers: dict[str, int] = field(default_factory=dict)
    error: str | None = None


def _custom():
    return kube.client_for(client.CustomObjectsApi)


def create_claim(namespace: str, pool: str, name: str | None = None) -> Created:
    """Claim one warm sandbox. The pool ref is what makes it warm."""
    claim_name = name or safe_name("claim")
    body = {
        "apiVersion": f"{EXTENSIONS_GROUP}/{VERSION}",
        "kind": "SandboxClaim",
        "metadata": {"name": claim_name, "labels": {MANAGED_LABEL: "managed"}},
        "spec": {"warmPoolRef": {"name": pool}},
    }
    started = time.monotonic()
    _custom().create_namespaced_custom_object(
        EXTENSIONS_GROUP, VERSION, namespace, "sandboxclaims", body
    )
    return Created(kind="claim", name=claim_name, namespace=namespace, started_at=started)


def create_sandbox(
    namespace: str,
    image: str,
    target_url: str,
    cdp_port: int,
    name: str | None = None,
    cpu_limit: str = "1",
    memory_limit: str = "768Mi",
) -> Created:
    """Create a bare Sandbox: same pod shape, none of the template hardening.

    No NetworkPolicy is generated, `dnsPolicy` stays ClusterFirst and the
    service-account token is mounted. This is the unhardened path, which is the
    entire point of the comparison, and it is only reachable when
    `allow_raw_sandbox` is on.
    """
    sandbox_name = name or safe_name("bare")
    body = {
        "apiVersion": f"{SANDBOX_GROUP}/{VERSION}",
        "kind": "Sandbox",
        "metadata": {"name": sandbox_name, "labels": {MANAGED_LABEL: "managed"}},
        "spec": {
            "podTemplate": {
                "metadata": {"labels": {"app": "browser-sandbox", "console": "compare"}},
                "spec": {
                    "restartPolicy": "Never",
                    "terminationGracePeriodSeconds": 1,
                    "containers": [
                        {
                            "name": "browser",
                            "image": image,
                            "imagePullPolicy": "Always",
                            "ports": [{"name": "cdp", "containerPort": cdp_port}],
                            "env": [
                                {"name": "CDP_PORT", "value": str(cdp_port)},
                                {"name": "TARGET_URL", "value": target_url},
                                {"name": "WINDOW_SIZE", "value": "1280,600"},
                            ],
                            "resources": {
                                "requests": {"cpu": "100m", "memory": "256Mi"},
                                "limits": {"cpu": cpu_limit, "memory": memory_limit},
                            },
                            "volumeMounts": [{"name": "shm", "mountPath": "/dev/shm"}],
                        }
                    ],
                    "volumes": [
                        {
                            "name": "shm",
                            "emptyDir": {"medium": "Memory", "sizeLimit": "512Mi"},
                        }
                    ],
                },
            }
        },
    }
    started = time.monotonic()
    _custom().create_namespaced_custom_object(SANDBOX_GROUP, VERSION, namespace, "sandboxes", body)
    created = Created(kind="sandbox", name=sandbox_name, namespace=namespace, started_at=started)
    created.sandbox = sandbox_name
    return created


def delete_sandbox(namespace: str, name: str) -> None:
    """Foreground delete of a Sandbox (the pod goes with it)."""
    _custom().delete_namespaced_custom_object(
        SANDBOX_GROUP,
        VERSION,
        namespace,
        "sandboxes",
        name,
        propagation_policy="Foreground",
    )


def delete_claim(namespace: str, name: str) -> None:
    """Delete a Claim. The adopted sandbox is released with it."""
    _custom().delete_namespaced_custom_object(
        EXTENSIONS_GROUP,
        VERSION,
        namespace,
        "sandboxclaims",
        name,
        propagation_policy="Foreground",
    )


def managed_claims(namespace: str) -> list[dict]:
    """Claims this console created (label `sandbox-console=managed`)."""
    try:
        response = _custom().list_namespaced_custom_object(
            EXTENSIONS_GROUP,
            VERSION,
            namespace,
            "sandboxclaims",
            label_selector=f"{MANAGED_LABEL}=managed",
        )
    except client.exceptions.ApiException:
        return []
    return list((response or {}).get("items") or [])


def managed_sandboxes(namespace: str) -> list[dict]:
    """Sandboxes this console created directly (the bare compare column).

    A claim's sandbox is deleted by deleting the claim, so this list is what
    teardown needs *in addition* to `managed_claims` -- otherwise the bare column
    of compare mode outlives every teardown.
    """
    try:
        response = _custom().list_namespaced_custom_object(
            SANDBOX_GROUP,
            VERSION,
            namespace,
            "sandboxes",
            label_selector=f"{MANAGED_LABEL}=managed",
        )
    except client.exceptions.ApiException:
        return []
    return list((response or {}).get("items") or [])


def claim_for_sandbox(namespace: str, sandbox_name: str) -> str | None:
    """Which claim owns this sandbox, if any."""
    try:
        response = _custom().list_namespaced_custom_object(
            EXTENSIONS_GROUP, VERSION, namespace, "sandboxclaims"
        )
    except client.exceptions.ApiException:
        return None
    for claim in (response or {}).get("items") or []:
        if adopted_sandbox(claim) == sandbox_name:
            return (claim.get("metadata") or {}).get("name")
    return None


def delete_any(namespace: str, name: str) -> str:
    """Delete the Claim that owns `name`, or the Sandbox itself.

    A tile is a Sandbox; a claim is what adopted it. Deleting the claim releases
    the sandbox back through the controller, so that is the path taken when one
    exists. Returns which object was removed, for the audit line.
    """
    claim = claim_for_sandbox(namespace, name)
    if claim:
        delete_claim(namespace, claim)
        return f"claim/{claim}"
    delete_sandbox(namespace, name)
    return f"sandbox/{name}"


def get_claim(namespace: str, name: str) -> dict | None:
    try:
        return _custom().get_namespaced_custom_object(
            EXTENSIONS_GROUP, VERSION, namespace, "sandboxclaims", name
        )
    except client.exceptions.ApiException:
        return None


def get_sandbox(namespace: str, name: str) -> dict | None:
    try:
        return _custom().get_namespaced_custom_object(
            SANDBOX_GROUP, VERSION, namespace, "sandboxes", name
        )
    except client.exceptions.ApiException:
        return None


def condition(obj: dict | None, name: str) -> dict | None:
    status = (obj or {}).get("status") or {}
    for item in status.get("conditions") or []:
        if item.get("type") == name:
            return item
    return None


def adopted_sandbox(claim: dict | None) -> str | None:
    status = (claim or {}).get("status") or {}
    return ((status.get("sandbox") or {}).get("name")) or None


async def wait_for_sandbox(created: Created, deadline: float) -> str | None:
    """Resolve the Sandbox name for a claim, and stamp t_sched on the way.

    For a claim this also resolves which pool member was adopted, and records its
    scheduling stamp, because the claim itself never reports `PodScheduled`.
    """
    loop = asyncio.get_running_loop()
    while time.monotonic() < deadline:
        if created.kind == "claim":
            claim = await loop.run_in_executor(None, get_claim, created.namespace, created.name)
            sandbox_name = adopted_sandbox(claim)
        else:
            sandbox_name = created.name
        if sandbox_name:
            created.sandbox = sandbox_name
            sandbox = await loop.run_in_executor(None, get_sandbox, created.namespace, sandbox_name)
            if condition(sandbox, "PodScheduled") and "t_sched_ms" not in created.timers:
                created.timers["t_sched_ms"] = _stamp(created.started_at)
            if condition(sandbox, "Ready") and _is_true(condition(sandbox, "Ready")):
                created.timers.setdefault("t_sched_ms", _stamp(created.started_at))
                # The number the product quotes: POST -> Ready=True.
                created.timers.setdefault("t_ready_ms", _stamp(created.started_at))
                return sandbox_name
            if condition(sandbox, "Ready") and not _is_true(condition(sandbox, "Ready")):
                reason = str(condition(sandbox, "Ready").get("reason") or "")
                if reason in {"PodFailed", "SandboxExpired", "InvalidConfiguration"}:
                    created.error = reason
                    return sandbox_name
        await asyncio.sleep(0.1)
    created.error = created.error or "timed out waiting for Ready"
    return created.sandbox


def _is_true(cond: dict | None) -> bool:
    return str((cond or {}).get("status")) == "True"


def _stamp(started_at: float) -> int:
    return int((time.monotonic() - started_at) * 1000)


async def wait_for_cdp(
    client_cdp: cdp.RouterCDP, created: Created, deadline: float
) -> bool:
    """t_cdp: the browser's DevTools endpoint answers through the router."""
    while time.monotonic() < deadline:
        try:
            await client_cdp.list_targets(created.sandbox or created.name)
        except Exception:  # noqa: BLE001 - not up yet is the normal case
            await asyncio.sleep(0.2)
            continue
        created.timers["t_cdp_ms"] = _stamp(created.started_at)
        return True
    return False


async def wait_for_paint(
    client_cdp: cdp.RouterCDP, created: Created, deadline: float
) -> bool:
    """t_paint: a screenshot that is not a blank page."""
    while time.monotonic() < deadline:
        try:
            frame = await client_cdp.screenshot(created.sandbox or created.name)
        except Exception:  # noqa: BLE001
            await asyncio.sleep(0.2)
            continue
        if len(frame) > BLANK_FRAME_FLOOR:
            created.timers["t_paint_ms"] = _stamp(created.started_at)
            return True
        await asyncio.sleep(0.2)
    return False


def finish(created: Created) -> Created:
    """Stamp the terminal timer so the bar chart never has a hole."""
    created.timers.setdefault("t_paint_ms", _stamp(created.started_at))
    return created


def sandbox_name_from_object(payload: dict) -> str | None:
    """Pull the sandbox name out of either a Sandbox or a Claim dict."""
    return ((payload.get("status") or {}).get("sandbox") or {}).get("name")


def valid_name(name: str) -> bool:
    return bool(re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", name or ""))


async def time_and_record(
    client,
    created: Created,
    *,
    image: str | None = None,
    target_url: str | None = None,
    timeout: float | None = None,
) -> Created:
    """Measure a creation end to end and write the numbers down.

    Called as a background task right after a POST, because the timers only exist
    while the sandbox is being born and the HTTP response cannot wait for them.
    With no database (no volume) `records` falls back to memory and the numbers
    last until the next restart, which the health endpoint reports.
    """
    deadline = time.monotonic() + (timeout or config.CREATE_TIMEOUT_SECONDS)
    await wait_for_sandbox(created, deadline)
    if created.error:
        return created
    await wait_for_cdp(client, created, deadline)
    await wait_for_paint(client, created, deadline)
    finish(created)
    records.remember_timings(
        created.sandbox or created.name,
        kind=created.kind,
        claim=created.name if created.kind == "claim" else None,
        timers=dict(created.timers),
        image=image,
        target_url=target_url,
    )
    return created

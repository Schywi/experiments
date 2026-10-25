"""Recipe runner (M5).

The RFC wanted to shell the existing `dev/load-test/test-recipes/` scripts. Those
scripts live in the agent-sandbox checkout on the host, and this process runs
inside the cluster with no access to them, so the runner executes the same three
operations in-process instead: a claim burst, a burst followed by teardown, and a
read-only capacity check. The output streams the way the scripts' stdout would --
one line at a time -- and only one recipe runs at a time.

Every recipe is bounded: the burst size is capped by
`MAX_CONCURRENT_SANDBOXES` (the measured ~9.1 active-browser ceiling on this
host), and a running recipe refuses a second request with 409 rather than
queueing.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from typing import AsyncIterator, Callable

import config
import lifecycle

# One runner, like one uvicorn worker: a second concurrent recipe would make the
# cluster-wide action that a demo depends on unrepeatable.
_running: str | None = None


class RecipeBusy(Exception):
    """Another recipe is already running."""


class UnknownRecipe(Exception):
    """No recipe by that name."""


@dataclass(frozen=True)
class Recipe:
    name: str
    summary: str
    # Blast radius, spelled out in the confirmation the UI shows.
    blast_radius: str


RECIPES: dict[str, Recipe] = {
    "claim-burst": Recipe(
        name="claim-burst",
        summary="Claim N warm sandboxes in parallel and time the adoption.",
        blast_radius="Creates N SandboxClaims in sandbox-demo",
    ),
    "burst-then-teardown": Recipe(
        name="burst-then-teardown",
        summary="Claim N warm sandboxes, hold them, then delete them all.",
        blast_radius="Creates and deletes N SandboxClaims in sandbox-demo",
    ),
    "capacity-check": Recipe(
        name="capacity-check",
        summary="Report live sandboxes, pods and the configured ceiling. Read-only.",
        blast_radius="None; read-only",
    ),
}


def busy_with() -> str | None:
    return _running


def _format(value: int | None) -> str:
    return "n/a" if value is None else f"{value} ms"


async def _claim_burst(count: int, hold: float, teardown: bool) -> AsyncIterator[dict]:
    yield {"line": f"claiming {count} warm sandboxes from {config.WARM_POOL}"}
    started = time.monotonic()
    created: list[lifecycle.Created] = []
    for index in range(count):
        try:
            item = await asyncio.get_running_loop().run_in_executor(
                None, lambda: lifecycle.create_claim(config.SANDBOX_NAMESPACE, config.WARM_POOL)
            )
        except Exception as error:  # noqa: BLE001 - a failed create is a line, not a crash
            yield {"line": f"  claim {index + 1}/{count} failed: {error}"}
            continue
        created.append(item)
        yield {"line": f"  created {item.name}"}

    if not created:
        yield {"line": "no claims were created", "done": True}
        return

    deadline = time.monotonic() + config.CREATE_TIMEOUT_SECONDS
    ready_at: dict[str, int] = {}
    while time.monotonic() < deadline and len(ready_at) < len(created):
        for item in created:
            if item.name in ready_at:
                continue
            sandbox = await lifecycle.wait_for_sandbox(item, time.monotonic() + 0.3)
            if sandbox and not item.error:
                ready_at[item.name] = int((time.monotonic() - started) * 1000)
                yield {
                    "line": f"  ready  {item.name} -> {sandbox} at {ready_at[item.name]} ms"
                }
        await asyncio.sleep(0.1)

    total = int((time.monotonic() - started) * 1000)
    yield {
        "line": f"result: {len(ready_at)}/{len(created)} claims ready in {total} ms",
        "metrics": {"created": len(created), "ready": len(ready_at), "total_ms": total},
    }

    if teardown:
        if hold:
            yield {"line": f"holding for {hold:.0f}s"}
            await asyncio.sleep(hold)
        for item in created:
            try:
                await asyncio.get_running_loop().run_in_executor(
                    None, lambda: lifecycle.delete_claim(config.SANDBOX_NAMESPACE, item.name)
                )
                yield {"line": f"  deleted {item.name}"}
            except Exception as error:  # noqa: BLE001
                yield {"line": f"  delete {item.name} failed: {error}"}
        yield {"line": f"teardown complete in {int((time.monotonic() - started) * 1000)} ms"}
    yield {"done": True}


async def _capacity_check() -> AsyncIterator[dict]:
    import kube

    snapshot = await asyncio.get_running_loop().run_in_executor(
        None, kube.snapshot, config.SANDBOX_NAMESPACE
    )
    ready = 0
    for sandbox in snapshot.sandboxes:
        for cond in (sandbox.get("status") or {}).get("conditions") or []:
            if cond.get("type") == "Ready" and str(cond.get("status")) == "True":
                ready += 1
    nodes = await asyncio.get_running_loop().run_in_executor(None, kube.nodes)
    yield {"line": f"sandboxes: {len(snapshot.sandboxes)} ({ready} ready)"}
    yield {"line": f"pods: {len(snapshot.pods)}"}
    yield {"line": f"ceiling: {config.MAX_CONCURRENT_SANDBOXES} (experiment 02 measurement)"}
    for node in nodes:
        allocatable = node.get("allocatable") or {}
        yield {
            "line": (
                f"node {node.get('name')}: cpu {allocatable.get('cpu')} "
                f"memory {allocatable.get('memory')} pods {allocatable.get('pods')}"
            )
        }
    yield {"done": True}


def _guard(name: str) -> Recipe:
    recipe = RECIPES.get(name)
    if recipe is None:
        raise UnknownRecipe(name)
    if _running is not None:
        raise RecipeBusy(_running)
    return recipe


async def stream(name: str, count: int | None = None, hold: float = 0.0, teardown: bool = False) -> AsyncIterator[dict]:
    """Run one recipe, yielding progress lines as they happen."""
    global _running
    _guard(name)
    size = min(max(count or 5, 1), config.MAX_CONCURRENT_SANDBOXES)
    _running = name
    try:
        if name == "capacity-check":
            async for item in _capacity_check():
                yield item
            return
        if name == "claim-burst":
            async for item in _claim_burst(size, 0.0, False):
                yield item
            return
        if name == "burst-then-teardown":
            async for item in _claim_burst(size, hold or 5.0, True):
                yield item
            return
        raise UnknownRecipe(name)
    finally:
        _running = None

"""Compare mode (M6): one warm Claim and one bare Sandbox, fired together.

The two creations are started within milliseconds of each other and then timed
independently, so the numbers in the two columns describe the same moment. The
bars are drawn to one shared scale in `render.compare()`, which is the point:
43 s next to 1.5 s only reads as a gap when the scale is shared.

`allow_raw_sandbox` gates the bare column. Without it the route refuses, because
a bare Sandbox has no NetworkPolicy, keeps cluster DNS and mounts a
service-account token -- exactly the properties the rest of this pane exists to
report on.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field

import cdp
import config
import lifecycle


@dataclass
class Arm:
    """One column of the split view."""

    label: str
    kind: str
    created: lifecycle.Created | None = None
    reachability: dict | None = None
    error: str | None = None

    @property
    def name(self) -> str:
        return (self.created.name if self.created else "-")

    @property
    def sandbox(self) -> str:
        return ((self.created.sandbox if self.created else None) or self.name)

    @property
    def timers(self) -> dict:
        return (self.created.timers if self.created else {})

    def total_ms(self) -> int:
        return max(self.timers.values()) if self.timers else 0

    def to_dict(self) -> dict:
        return {
            "label": self.label,
            "kind": self.kind,
            "name": self.name,
            "sandbox": self.sandbox,
            "timers": dict(self.timers),
            "error": self.error,
            "reachability": self.reachability,
        }


@dataclass
class Run:
    """One comparison, kept so the pane can show the p50 of the last three."""

    started_at: float
    arms: list[Arm] = field(default_factory=list)
    note: str | None = None

    def to_dict(self) -> dict:
        return {
            "started_at": self.started_at,
            "arms": [arm.to_dict() for arm in self.arms],
            "note": self.note,
        }


# The last three runs, newest first. In-process, like everything else here.
HISTORY: list[Run] = []
MAX_HISTORY = 3


def p50(values: list[int]) -> int | None:
    values = sorted(value for value in values if value is not None)
    if not values:
        return None
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return int((values[middle - 1] + values[middle]) / 2)


def history_medians() -> dict:
    """p50 of each timer per arm label, across the last three runs."""
    result: dict[str, dict] = {}
    for run in HISTORY:
        for arm in run.arms:
            bucket = result.setdefault(arm.label, {})
            for key, value in arm.timers.items():
                bucket.setdefault(key, []).append(value)
    return {
        label: {key: p50(values) for key, values in timers.items()}
        for label, timers in result.items()
    }


async def _time_arm(arm: Arm, client: cdp.RouterCDP, deadline: float) -> None:
    if arm.created is None:
        return
    sandbox = await lifecycle.wait_for_sandbox(arm.created, deadline)
    if arm.created.error:
        arm.error = arm.created.error
        return
    arm.created.sandbox = sandbox or arm.created.name
    await lifecycle.wait_for_cdp(client, arm.created, deadline)
    await lifecycle.wait_for_paint(client, arm.created, deadline)
    lifecycle.finish(arm.created)


async def run_compare(client: cdp.RouterCDP, allow_raw: bool, timeout: float = 90.0) -> Run:
    """Fire a Claim and (optionally) a bare Sandbox together, then time both.

    Pre-pulling the image is the caller's job: a cold pull lands inside the first
    timer and poisons the comparison. The pool members are already warm, so the
    claim side is honest by construction; the bare side starts from the local
    image cache (`imagePullPolicy: Always` with the tag already on the node).
    """
    run = Run(started_at=time.time())
    claim_arm = Arm(label="CLAIM (warm pool)", kind="claim")
    run.arms.append(claim_arm)

    deadline = time.monotonic() + timeout
    loop = asyncio.get_running_loop()

    # Start both creations before awaiting either, so the wall-clock offsets are
    # as close as the API server allows.
    claim_arm.created = await loop.run_in_executor(
        None,
        lambda: lifecycle.create_claim(config.SANDBOX_NAMESPACE, config.WARM_POOL),
    )

    bare_arm: Arm | None = None
    if allow_raw:
        bare_arm = Arm(label="BARE SANDBOX", kind="sandbox")
        run.arms.append(bare_arm)
        try:
            bare_arm.created = await loop.run_in_executor(
                None,
                lambda: lifecycle.create_sandbox(
                    config.SANDBOX_NAMESPACE,
                    image=config.SANDBOX_IMAGE,
                    target_url=config.TARGET_URL,
                    cdp_port=config.SANDBOX_PORT,
                ),
            )
        except Exception as error:  # noqa: BLE001 - reported in the column
            bare_arm.error = f"create failed: {error}"
    else:
        run.note = "allowRawSandbox is off: only the warm claim was timed"

    await asyncio.gather(
        *[_time_arm(arm, client, deadline) for arm in run.arms if arm.created is not None]
    )

    # Reachability runs after the timers, so it never contaminates them. This is
    # the second half of the comparison: the timing gap is the warm-pool pitch,
    # the reachability gap is the isolation one.
    for arm in run.arms:
        if arm.created is None or arm.error:
            continue
        try:
            arm.reachability = await probe_arm(arm, client, config.PROBE_TIMEOUT_MS)
        except Exception as error:  # noqa: BLE001 - a failed probe is a reported arm
            arm.error = f"probe failed: {error}"

    HISTORY.insert(0, run)
    del HISTORY[MAX_HISTORY:]
    return run


async def probe_arm(arm: Arm, client: cdp.RouterCDP, timeout_ms: int) -> dict:
    """Isolation evidence for one column (same classifier as the tile badge)."""
    import probes

    spec = {}
    pod = await asyncio.get_running_loop().run_in_executor(
        None, _pod_for, arm.sandbox
    )
    if pod:
        spec = probes.spec_claims(pod)
    raw = await client.evaluate(arm.sandbox, probes.build_expression(timeout_ms))
    return probes.summarise(probes.parse(raw), timeout_ms, spec)


def _pod_for(sandbox_name: str) -> dict | None:
    import kube

    listing = kube.pods(config.SANDBOX_NAMESPACE)
    for pod in listing:
        labels = (pod.get("metadata") or {}).get("labels") or {}
        if labels.get("agents.x-k8s.io/claim-uid") and pod.get("metadata", {}).get("name") == sandbox_name:
            return pod
    for pod in listing:
        if (pod.get("metadata") or {}).get("name") == sandbox_name:
            return pod
    return None

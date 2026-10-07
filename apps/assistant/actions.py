"""Milestone 9: controlled actions behind explicit human confirmation.

Level-2 tools (charter: "safe operational actions with confirmation"). The model
can PROPOSE an action but never executes one: a proposal returns a single-use,
short-lived token, and execution requires a separate human POST to
/actions/confirm with that token. Every step is audited.

Two independent gates protect execution:

  * ACTIONS_ENABLED (chart: rbac.allowActions) must be true, and
  * the target namespace must be in ACTION_NAMESPACES (empty = all namespaces).

Level 3 (destructive) is deliberately not implemented.
"""

import os
import secrets
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Callable, Dict

from kubernetes import client, config

from k8sutil import audit

PENDING_TTL = int(os.environ.get("ACTION_TTL_SECONDS", "120"))
ACTIONS_ENABLED = os.environ.get("ACTIONS_ENABLED", "false").lower() == "true"
ALLOWED_NAMESPACES = {
    ns for ns in os.environ.get("ACTION_NAMESPACES", "").split(",") if ns.strip()
}

_config_loaded = False


def _apps() -> "client.AppsV1Api":
    global _config_loaded
    if not _config_loaded:
        config.load_incluster_config()
        _config_loaded = True
    return client.AppsV1Api()


class PolicyError(Exception):
    """The request is not allowed (unknown action, blocked namespace, disabled)."""


class UnknownToken(Exception):
    """No pending action for this token (unknown or already used)."""


class Expired(Exception):
    """The pending action's token has expired."""


@dataclass(frozen=True)
class Action:
    name: str
    level: int  # 2 = safe action with confirmation, 3 = destructive (unused)
    description: str
    preview: Callable[..., dict]
    execute: Callable[..., dict]


def _restart_preview(namespace: str, name: str) -> dict:
    return {"action": "restart_deployment", "target": f"{namespace}/{name}",
            "effect": "rolling restart (patch pod-template restartedAt)"}


def _restart_execute(namespace: str, name: str) -> dict:
    now = datetime.now(timezone.utc).isoformat()
    _apps().patch_namespaced_deployment(
        name=name, namespace=namespace,
        body={"spec": {"template": {"metadata": {"annotations":
              {"kubectl.kubernetes.io/restartedAt": now}}}}},
    )
    return {"restarted": f"{namespace}/{name}", "at": now}


def _scale_preview(namespace: str, name: str, replicas: int) -> dict:
    return {"action": "scale_deployment", "target": f"{namespace}/{name}",
            "effect": f"set replicas={int(replicas)}"}


def _scale_execute(namespace: str, name: str, replicas: int) -> dict:
    _apps().patch_namespaced_deployment_scale(
        name=name, namespace=namespace, body={"spec": {"replicas": int(replicas)}}
    )
    return {"scaled": f"{namespace}/{name}", "replicas": int(replicas)}


ACTIONS: Dict[str, Action] = {
    "restart_deployment": Action(
        "restart_deployment", 2, "Rolling-restart a Deployment",
        _restart_preview, _restart_execute),
    "scale_deployment": Action(
        "scale_deployment", 2, "Scale a Deployment to a replica count",
        _scale_preview, _scale_execute),
}

_pending: Dict[str, dict] = {}


def _check_namespace(namespace: str) -> None:
    if ALLOWED_NAMESPACES and namespace not in ALLOWED_NAMESPACES:
        raise PolicyError(f"namespace {namespace!r} is not in ACTION_NAMESPACES")


def catalog() -> list:
    return [{"name": a.name, "level": a.level, "description": a.description}
            for a in ACTIONS.values()]


def enabled() -> bool:
    return ACTIONS_ENABLED


def list_pending() -> list:
    now = time.time()
    return [{"token": t, "action": p["action"], "args": p["args"],
             "age_seconds": round(now - p["ts"], 1)}
            for t, p in _pending.items() if now - p["ts"] <= PENDING_TTL]


def request(action: str, args: dict) -> dict:
    """Create a pending action and return its single-use confirmation token.

    Validates the action and namespace and computes a preview; it does NOT touch
    the cluster.
    """
    act = ACTIONS.get(action)
    if act is None:
        raise PolicyError(f"unknown action {action!r}; try {', '.join(ACTIONS)}")
    args = args or {}
    _check_namespace(str(args.get("namespace", "default")))
    try:
        preview = act.preview(**args)
    except TypeError as exc:
        raise PolicyError(f"bad args for {action}: {exc}") from exc
    token = secrets.token_urlsafe(16)
    _pending[token] = {"action": action, "args": args, "ts": time.time()}
    audit({"event": "action_request", "action": action, "args": args, "token": token})
    return {"token": token, "action": action, "args": args, "preview": preview,
            "expires_in": PENDING_TTL,
            "note": "POST /actions/confirm with this token to execute"}


def confirm(token: str) -> dict:
    """Execute the pending action for a token. Single-use; requires ACTIONS_ENABLED."""
    pending = _pending.pop(token, None)
    if pending is None:
        raise UnknownToken(token)
    if time.time() - pending["ts"] > PENDING_TTL:
        raise Expired(token)
    if not ACTIONS_ENABLED:
        audit({"event": "action_denied", "action": pending["action"],
               "reason": "ACTIONS_ENABLED=false"})
        raise PolicyError("actions are disabled (set rbac.allowActions=true)")
    act = ACTIONS[pending["action"]]
    audit({"event": "action_confirm", "action": act.name, "args": pending["args"]})
    result = act.execute(**pending["args"])
    audit({"event": "action_result", "action": act.name, "result": result})
    return {"action": act.name, "result": result}

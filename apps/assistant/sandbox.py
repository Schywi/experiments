"""Thin HTTP client for the sandbox console API.

The console (192.168.0.250) owns routing, readiness, the CDP token and frame
freshness. The assistant only calls its API and reports what it says. No sandbox
logic is reimplemented here.

Contract consumed:
    GET    /api/sandboxes                      inventory (ready/phase/route)
    GET    /api/sandboxes/{id}/reachability    reachability evidence
    POST   /api/sandboxes/{id}/url             navigate  {url}
    POST   /api/sandboxes/{id}/click           click     {selector}
    POST   /api/sandboxes/{id}/scroll          scroll    {dy}
    GET    /sandboxes/{id}/shot                PNG
    POST   /api/sandboxes                      create
    DELETE /api/sandboxes/{id}                 destroy
"""

import os
import re
import time

import httpx

CONSOLE_URL = os.environ.get("CONSOLE_URL", "http://192.168.0.250").rstrip("/")
CONSOLE_TIMEOUT = float(os.environ.get("CONSOLE_TIMEOUT", "25"))
CREATE_KINDS = ("claim", "sandbox")   # the only values POST /sandboxes accepts

# Sandbox ids as the console names them: browser-pool-xxxx, claim-xxxx, bare-xxxx,
# or an explicit "sandbox <id>". A sentence that merely says the word is not an id.
_ID_PREFIXED = re.compile(r"\b((?:browser-pool|claim|bare)-[a-z0-9]{3,})\b")
_ID_NAMED = re.compile(r"sandbox[\s:]+([a-z0-9][a-z0-9.-]{2,})")


def extract_id(text: str) -> str:
    """The sandbox id a question names, or ''.

    Ids in this platform always carry a hyphen (browser-pool-x, claim-x, bare-x),
    so a bare word after the word "sandbox" ("sandbox with google.com") is not an id.
    """
    for pattern in (_ID_PREFIXED, _ID_NAMED):
        match = pattern.search((text or "").lower())
        if match and "-" in match.group(1):
            return match.group(1).rstrip(".")
    return ""


class ConsoleError(Exception):
    """The console API is absent, unreachable or returned an error."""


def _request(method: str, path: str, payload=None):
    try:
        with httpx.Client(timeout=CONSOLE_TIMEOUT) as client:
            response = client.request(method, f"{CONSOLE_URL}{path}", json=payload)
    except httpx.HTTPError as exc:
        raise ConsoleError(f"console unreachable: {type(exc).__name__}: {exc}") from exc
    if response.status_code == 404:
        raise ConsoleError(f"console API unavailable at {path} (404) — the JSON surface is not deployed")
    if response.status_code >= 400:
        raise ConsoleError(f"console {path} returned {response.status_code}")
    try:
        return response.json()
    except ValueError as exc:
        raise ConsoleError(f"console {path} did not return JSON") from exc


def list_sandboxes() -> dict:
    return _request("GET", "/api/sandboxes")


def reachability(sandbox_id: str) -> dict:
    return _request("POST", f"/api/sandboxes/{sandbox_id}/reachability")


def get_sandbox(sandbox_id: str) -> dict:
    return _request("GET", f"/api/sandboxes/{sandbox_id}")


def open_url(sandbox_id: str, url: str) -> dict:
    return _request("POST", f"/api/sandboxes/{sandbox_id}/url", {"url": url})


CONTRACT_ROUTES = (
    ("GET", "/api/sandboxes"),
    ("GET", "/api/sandboxes/{sandbox_id}"),
    ("POST", "/api/sandboxes/{sandbox_id}/url"),
    ("POST", "/api/sandboxes/{sandbox_id}/reachability"),
    ("POST", "/sandboxes"),
    ("DELETE", "/sandboxes/{sandbox_id}"),
    ("GET", "/sandboxes/{sandbox_id}/shot"),
)


def contract_check() -> list:
    """Routes this client uses that the console's own openapi.json does not expose."""
    try:
        with httpx.Client(timeout=CONSOLE_TIMEOUT) as client:
            response = client.get(f"{CONSOLE_URL}/openapi.json")
            response.raise_for_status()
            spec = response.json()
    except (httpx.HTTPError, ValueError) as exc:
        return [f"openapi.json unavailable: {type(exc).__name__}"]
    paths = spec.get("paths") or {}
    missing = []
    for method, path in CONTRACT_ROUTES:
        operations = paths.get(path)
        if not isinstance(operations, dict) or method.lower() not in operations:
            missing.append(f"{method} {path}")
    return missing


def deep_link(sandbox_id: str) -> str:
    """The console page for one sandbox, dialog already open."""
    return f"{CONSOLE_URL}/s/{sandbox_id}"


def _known_ids() -> list:
    """Ids the console currently reports, used to refuse stale references."""
    data = list_sandboxes()
    tiles = data.get("tiles") if isinstance(data, dict) else None
    return sorted(t["id"] for t in (tiles or []) if isinstance(t, dict) and t.get("id"))


def _require_sandbox(sandbox_id: str) -> str:
    """Guard for actions on an existing sandbox. Creation is exempt."""
    ids = _known_ids()
    if sandbox_id not in ids:
        known = ", ".join(ids) or "(none)"
        raise ConsoleError(f"no sandbox {sandbox_id!r}; the console reports: {known}")
    return sandbox_id


def shot(sandbox_id: str, fresh: bool = False) -> dict:
    """A screenshot is reported as a URL the UI can render, not as bytes."""
    _require_sandbox(sandbox_id)
    query = "?fresh=1" if fresh else ""
    return {"sandbox": sandbox_id,
            "image_url": f"{CONSOLE_URL}/sandboxes/{sandbox_id}/shot{query}",
            "deep_link": deep_link(sandbox_id)}


def create_sandbox(kind: str = "claim") -> dict:
    if kind not in CREATE_KINDS:
        kind = "claim"                      # a misread kind is not worth failing the create
    return _request("POST", "/sandboxes", {"kind": kind})


def destroy_sandbox(sandbox_id: str) -> dict:
    _require_sandbox(sandbox_id)
    return _request("DELETE", f"/sandboxes/{sandbox_id}")


# --- tool-facing wrappers (structured results, no exceptions escaping) ------

def sandbox_create(kind: str = "claim") -> dict:
    """Create one sandbox, then resolve the tile id the console reports for it."""
    created = create_sandbox(kind)
    name = created.get("name") if isinstance(created, dict) else None
    sandbox_id = ""
    for _ in range(6):                       # the tile appears a moment after the claim
        for tile in (list_sandboxes().get("tiles") or []):
            if isinstance(tile, dict) and name and tile.get("claim") == name:
                sandbox_id = str(tile.get("id") or "")
                break
        if sandbox_id:
            break
        time.sleep(1.5)
    return {"created": created, "sandbox_id": sandbox_id,
            "deep_link": deep_link(sandbox_id) if sandbox_id else None}


def sandbox_list() -> dict:
    """List sandboxes with the console's own ready/phase/route state."""
    data = list_sandboxes()
    tiles = data.get("tiles") if isinstance(data, dict) else None
    for tile in tiles or []:
        if isinstance(tile, dict) and isinstance(tile.get("id"), str):
            tile["deep_link"] = deep_link(tile["id"])
    return data


def sandbox_open(sandbox_id: str, url: str) -> dict:
    """Navigate a sandbox, then return the frame URL for that view."""
    _require_sandbox(sandbox_id)
    result = open_url(sandbox_id, url)
    return {"sandbox": sandbox_id, "url": url, "result": result,
            "image_url": f"{CONSOLE_URL}/sandboxes/{sandbox_id}/shot",
            "deep_link": deep_link(sandbox_id)}


def sandbox_shot(sandbox_id: str) -> dict:
    """Current frame of one sandbox, as a renderable URL."""
    return shot(sandbox_id)

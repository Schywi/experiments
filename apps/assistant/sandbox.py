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

import httpx

CONSOLE_URL = os.environ.get("CONSOLE_URL", "http://192.168.0.250").rstrip("/")
CONSOLE_TIMEOUT = float(os.environ.get("CONSOLE_TIMEOUT", "25"))


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
    return _request("GET", f"/api/sandboxes/{sandbox_id}/reachability")


def open_url(sandbox_id: str, url: str) -> dict:
    return _request("POST", f"/api/sandboxes/{sandbox_id}/url", {"url": url})


def shot(sandbox_id: str) -> dict:
    """A screenshot is reported as a URL the UI can render, not as bytes."""
    return {"sandbox": sandbox_id, "image_url": f"{CONSOLE_URL}/sandboxes/{sandbox_id}/shot"}


def create_sandbox() -> dict:
    return _request("POST", "/api/sandboxes")


def destroy_sandbox(sandbox_id: str) -> dict:
    return _request("DELETE", f"/api/sandboxes/{sandbox_id}")


# --- tool-facing wrappers (structured results, no exceptions escaping) ------

def sandbox_list() -> dict:
    """List sandboxes with the console's own ready/phase/route state."""
    return list_sandboxes()


def sandbox_open(sandbox_id: str, url: str) -> dict:
    """Navigate a sandbox, then return the frame URL for that view."""
    result = open_url(sandbox_id, url)
    return {"sandbox": sandbox_id, "url": url, "result": result,
            "image_url": f"{CONSOLE_URL}/sandboxes/{sandbox_id}/shot"}


def sandbox_shot(sandbox_id: str) -> dict:
    """Current frame of one sandbox, as a renderable URL."""
    return shot(sandbox_id)

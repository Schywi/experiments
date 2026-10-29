"""The console's own HTTP API, and Chromium through the console's router path.

Two transports, both of them the console's:

    console API   http://sandbox-console.sandbox-console.svc.cluster.local:8080
                  GET  /api/sandboxes                 list tiles
                  POST /sandboxes {kind, url}          claim
                  DELETE /sandboxes/{id}               delete a claim
                  POST /api/sandboxes/{id}/url         navigate
                  POST /api/sandboxes/{id}/click       real mouse press/release
                  POST /api/sandboxes/{id}/scroll      real wheel event
                  GET  /sandboxes/{id}/shot?fresh=1    PNG frame
                  GET  /api/sandboxes/{id}             one tile (screenshot_ts)

    DevTools      POST {router}/cdp {"method","params"}
                  headers X-Sandbox-ID, X-Sandbox-Port, X-Sandbox-Namespace and a
                  Bearer token from the pod's own projected ServiceAccount

The router runs `--authz-mode=tokenreview`, so the Job's mounted token is the
credential; `cdp-bridge.py` in the sandbox pod holds the WebSocket to Chromium and
turns these posts into DevTools calls. Nothing here talks to Chromium directly.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import time
import urllib.error
import urllib.request

CONSOLE = os.environ.get(
    "CONSOLE_URL", "http://sandbox-console.sandbox-console.svc.cluster.local:8080"
).rstrip("/")
ROUTER = os.environ.get(
    "ROUTER_URL", "http://sandbox-router-svc.agent-sandbox-system.svc.cluster.local:8080"
).rstrip("/")
NAMESPACE = os.environ.get("SANDBOX_NAMESPACE", "sandbox-demo")
CDP_PORT = os.environ.get("SANDBOX_PORT", "9222")
TOKEN_FILE = "/var/run/secrets/kubernetes.io/serviceaccount/token"
TIMEOUT = float(os.environ.get("HTTP_TIMEOUT", "120"))


class ConsoleError(RuntimeError):
    """A console or router call failed, with the status and body kept."""

    def __init__(self, message: str, status: int | None = None, body: str = ""):
        super().__init__(message)
        self.status = status
        self.body = body


def _token() -> str:
    try:
        with open(TOKEN_FILE, encoding="utf-8") as fh:
            return fh.read().strip()
    except OSError:
        return os.environ.get("ROUTER_TOKEN", "").strip()


def _request(url: str, method: str = "GET", payload: dict | None = None,
             headers: dict[str, str] | None = None, timeout: float = TIMEOUT,
             raw: bool = False):
    data = json.dumps(payload).encode() if payload is not None else None
    all_headers = {"content-type": "application/json"} if payload is not None else {}
    all_headers.update(headers or {})
    request = urllib.request.Request(url, data=data, headers=all_headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read()
            return body if raw else json.loads(body.decode() or "{}")
    except urllib.error.HTTPError as error:
        detail = ""
        try:
            detail = error.read().decode(errors="replace")[:400]
        except Exception:  # noqa: BLE001
            pass
        raise ConsoleError(f"{method} {url} -> HTTP {error.code}", error.code, detail) from error
    except (urllib.error.URLError, TimeoutError) as error:
        raise ConsoleError(f"{method} {url} -> {type(error).__name__}: {error}") from error


# --------------------------------------------------------------------------- #
# console API
# --------------------------------------------------------------------------- #
def tiles() -> list[dict]:
    return (_request(f"{CONSOLE}/api/sandboxes") or {}).get("tiles") or []


def tile(sandbox_id: str) -> dict:
    return (_request(f"{CONSOLE}/api/sandboxes/{sandbox_id}") or {}).get("tile") or {}


def claim(url: str | None = None) -> str:
    """Claim one sandbox from the warm pool and return its id."""
    payload = {"kind": "claim"}
    if url:
        payload["url"] = url
    body = _request(f"{CONSOLE}/sandboxes", "POST", payload)
    sandbox_id = body.get("sandbox")
    if not sandbox_id:
        raise ConsoleError(f"claim did not bind: {body}")
    return sandbox_id


def release(sandbox_id: str) -> None:
    _request(f"{CONSOLE}/sandboxes/{sandbox_id}", "DELETE")


def navigate(sandbox_id: str, url: str) -> dict:
    return _request(f"{CONSOLE}/api/sandboxes/{sandbox_id}/url", "POST", {"url": url})


def click(sandbox_id: str, x: float, y: float) -> dict:
    """A real press and release at viewport coordinates; the console's own path."""
    return _request(
        f"{CONSOLE}/api/sandboxes/{sandbox_id}/click", "POST", {"x": x, "y": y, "scale": 1.0}
    )


def scroll(sandbox_id: str, dy: int) -> dict:
    return _request(f"{CONSOLE}/api/sandboxes/{sandbox_id}/scroll", "POST", {"dy": dy})


def shot(sandbox_id: str, *, fresh: bool = True, timeout: float = TIMEOUT) -> bytes:
    url = f"{CONSOLE}/sandboxes/{sandbox_id}/shot" + ("?fresh=1" if fresh else "")
    return _request(url, raw=True, timeout=timeout)


def screenshot_via_cdp(sandbox_id: str, timeout: float = TIMEOUT) -> bytes:
    """One PNG straight from `Page.captureScreenshot` through the router.

    The console's own `/shot` route ends in the same DevTools call, so the two are
    expected to agree byte for byte; stage 2 captures both and records whether they
    do, which is the cheapest proof that the controller is looking at the same
    Chromium the pane is.
    """
    result = cdp(sandbox_id, "Page.captureScreenshot",
                 {"format": "png", "captureBeyondViewport": False, "fromSurface": True},
                 timeout=timeout)
    data = result.get("data")
    if not data:
        raise ConsoleError("Page.captureScreenshot returned no data")
    return base64.b64decode(data)


def sha256(payload: bytes) -> str:
    return hashlib.sha256(payload).hexdigest()


# --------------------------------------------------------------------------- #
# DevTools through the console's router
# --------------------------------------------------------------------------- #
def cdp(sandbox_id: str, method: str, params: dict | None = None,
        timeout: float = TIMEOUT) -> dict:
    headers = {
        "X-Sandbox-ID": sandbox_id,
        "X-Sandbox-Port": CDP_PORT,
        "X-Sandbox-Namespace": NAMESPACE,
    }
    token = _token()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    body = _request(f"{ROUTER}/cdp", "POST", {"method": method, "params": params or {}},
                    headers=headers, timeout=timeout)
    if isinstance(body, dict) and body.get("error"):
        raise ConsoleError(f"cdp {method} failed: {body}", body=json.dumps(body)[:400])
    return body.get("result") or {}


def evaluate(sandbox_id: str, expression: str, timeout: float = TIMEOUT):
    result = cdp(sandbox_id, "Runtime.evaluate",
                 {"expression": expression, "returnByValue": True}, timeout=timeout)
    inner = result.get("result") or {}
    return inner.get("value")


def element_at(sandbox_id: str, x: float, y: float) -> dict | None:
    """What is under a viewport point, described the way the scores need it.

    Reads the element, its tag, its accessible-ish name, its data attributes and
    whether it is a button, so a click can be proved to have landed on the intended
    control rather than on a neighbour.
    """
    expression = (
        "(() => { const e = document.elementFromPoint(%s, %s); if (!e) return null;"
        " const t = e.closest('button,a,[role=button],article,input,select,textarea') || e;"
        " return JSON.stringify({tag: t.tagName.toLowerCase(),"
        " text: (t.textContent||'').trim().replace(/\\s+/g,' ').slice(0,80),"
        " target: (t.dataset && t.dataset.target) || '',"
        " act: (t.dataset && t.dataset.act) || '',"
        " id: (t.dataset && t.dataset.id) || '',"
        " disabled: !!t.disabled}); })()" % (float(x), float(y))
    )
    value = evaluate(sandbox_id, expression)
    return json.loads(value) if value else None


def node_at(sandbox_id: str, x: float, y: float) -> dict | None:
    """The DOM node under a viewport point, as `{backendNodeId, nodeId}`.

    `elementFromPoint` names the element; this gives it an identity that can be
    compared with the accessibility tree's `backendDOMNodeId`, which is how a click
    is proved to have landed on the intended control rather than on a neighbour with
    the same text.
    """
    try:
        cdp(sandbox_id, "DOM.enable", {})
        result = cdp(sandbox_id, "DOM.getNodeForLocation",
                     {"x": int(x), "y": int(y), "includeUserAgentShadowDOM": True})
    except ConsoleError:
        return None
    return {"backendNodeId": result.get("backendNodeId"), "nodeId": result.get("nodeId")}


def page_state(sandbox_id: str) -> dict:
    """URL, scroll offset, viewport size and a page signature, in one round trip."""
    expression = (
        "(() => JSON.stringify({url: location.href, scrollY: Math.round(window.scrollY),"
        " w: innerWidth, h: innerHeight, title: document.title,"
        " buttons: document.querySelectorAll('button').length,"
        " signature: (document.body && document.body.innerText || '').length }))()"
    )
    value = evaluate(sandbox_id, expression)
    return json.loads(value) if value else {}


def ax_nodes(sandbox_id: str) -> list[dict]:
    """The full accessibility tree, flattened to the fields the resolver uses."""
    cdp(sandbox_id, "Accessibility.enable", {})
    tree = cdp(sandbox_id, "Accessibility.getFullAXTree", {})
    out = []
    for node in tree.get("nodes") or []:
        role = ((node.get("role") or {}).get("value") or "")
        name = ((node.get("name") or {}).get("value") or "")
        ignored = bool(node.get("ignored"))
        backend_id = node.get("backendDOMNodeId")
        out.append({"nodeId": node.get("nodeId"), "backendDOMNodeId": backend_id,
                    "role": role, "name": name, "ignored": ignored})
    return out


def box_for_backend_node(sandbox_id: str, backend_node_id: int) -> dict | None:
    """The layout box of an AX node, in viewport coordinates, or None."""
    try:
        cdp(sandbox_id, "DOM.enable", {})
        model = cdp(sandbox_id, "DOM.getBoxModel", {"backendNodeId": backend_node_id})
    except ConsoleError:
        return None
    quad = (model.get("model") or {}).get("border") or []
    xs, ys = quad[0::2], quad[1::2]
    if not xs or not ys:
        return None
    left, right, top, bottom = min(xs), max(xs), min(ys), max(ys)
    return {"x": round((left + right) / 2), "y": round((top + bottom) / 2),
            "left": round(left), "top": round(top),
            "w": round(right - left), "h": round(bottom - top),
            "visible": right > 0 and bottom > 0 and left < 1280 and top < 600
                       and (right - left) > 0 and (bottom - top) > 0}


def wait_for(seconds: float) -> None:
    time.sleep(seconds)

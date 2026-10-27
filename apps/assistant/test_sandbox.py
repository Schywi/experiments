"""Sandbox client tests against a fake console that enforces the OpenAPI contract.

Anything not in CONTRACT answers 405, so a wrong method or path fails loudly.

    python3 apps/assistant/test_sandbox.py
"""

import importlib
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import sandbox  # noqa: E402

# (method, path) pairs the console actually exposes, and what we answer.
CONTRACT = {
    ("GET", "/api/sandboxes"): (200, {"ok": True, "tiles": [{"id": "sb-1", "phase": "ready"}]}),
    ("GET", "/api/sandboxes/sb-1"): (200, {"id": "sb-1", "phase": "ready"}),
    ("POST", "/api/sandboxes/sb-1/url"): (200, {"ok": True, "url": "x"}),
    ("POST", "/api/sandboxes/sb-1/reachability"): (200, {"ok": True}),
    ("POST", "/sandboxes"): (202, {"kind": "claim", "name": "claim-abc", "namespace": "sandbox-demo"}),
    ("DELETE", "/sandboxes/sb-1"): (202, {"removed": "sb-1"}),
}


class _Handler(BaseHTTPRequestHandler):
    mode = "ok"
    calls = []

    def log_message(self, *a):
        pass

    def _reply(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _handle(self):
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length) if length else b""
        body = json.loads(raw) if raw else None
        path = self.path.split("?")[0]
        type(self).calls.append((self.command, path, body))
        if self.mode == "noapi" and path.startswith("/api/"):
            return self._reply(404, {"detail": "not found"})
        if path == "/openapi.json":
            spec = {"paths": {}}
            for m, p in sandbox.CONTRACT_ROUTES:
                spec["paths"].setdefault(p, {})[m.lower()] = {}
            if self.mode == "drift":
                spec["paths"]["/api/sandboxes/{sandbox_id}/reachability"].pop("post", None)
            return self._reply(200, spec)
        hit = CONTRACT.get((self.command, path))
        if hit is None:
            return self._reply(405, {"detail": "Method Not Allowed"})
        return self._reply(*hit)

    do_GET = do_POST = do_DELETE = lambda self: self._handle()  # noqa: E731


def _client(mode="ok"):
    handler = type("H", (_Handler,), {"mode": mode, "calls": []})
    server = HTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    os.environ["CONSOLE_URL"] = f"http://127.0.0.1:{server.server_port}"
    module = importlib.reload(sandbox)
    return server, module, handler.calls


def test_create_posts_kind_claim():
    server, s, calls = _client()
    out = s.create_sandbox()
    assert out["name"] == "claim-abc", out
    assert ("POST", "/sandboxes", {"kind": "claim"}) in calls, calls
    server.shutdown()


def test_create_bare_sandbox_kind():
    server, s, calls = _client()
    s.create_sandbox(kind="sandbox")
    assert ("POST", "/sandboxes", {"kind": "sandbox"}) in calls, calls
    server.shutdown()


def test_create_rejects_bad_kind_without_calling():
    server, s, calls = _client()
    try:
        s.create_sandbox(kind="bogus")
    except s.ConsoleError as exc:
        assert "kind must be one of" in str(exc), exc
    else:
        raise AssertionError("expected ConsoleError")
    assert calls == [], calls
    server.shutdown()


def test_reachability_is_post():
    server, s, calls = _client()
    s.reachability("sb-1")
    assert ("POST", "/api/sandboxes/sb-1/reachability", None) in calls, calls
    server.shutdown()


def test_list_and_get_read_tiles():
    server, s, calls = _client()
    assert s.sandbox_list()["tiles"][0]["id"] == "sb-1"
    assert s.get_sandbox("sb-1")["phase"] == "ready"
    assert ("GET", "/api/sandboxes", None) in calls
    assert ("GET", "/api/sandboxes/sb-1", None) in calls
    server.shutdown()


def test_open_posts_url_body():
    server, s, calls = _client()
    out = s.sandbox_open("sb-1", "http://192.168.0.245/")
    assert out["image_url"].endswith("/sandboxes/sb-1/shot"), out
    assert ("POST", "/api/sandboxes/sb-1/url", {"url": "http://192.168.0.245/"}) in calls, calls
    server.shutdown()


def test_shot_fresh_adds_query():
    server, s, _ = _client()
    assert s.shot("sb-1")["image_url"].endswith("/sandboxes/sb-1/shot")
    assert s.shot("sb-1", fresh=True)["image_url"].endswith("/sandboxes/sb-1/shot?fresh=1")
    server.shutdown()


def test_destroy_uses_delete():
    server, s, calls = _client()
    assert s.destroy_sandbox("sb-1")["removed"] == "sb-1"
    assert ("DELETE", "/sandboxes/sb-1", None) in calls, calls
    server.shutdown()


def test_missing_api_surface_raises():
    server, s, _ = _client(mode="noapi")
    try:
        s.sandbox_list()
    except s.ConsoleError as exc:
        assert "console API unavailable" in str(exc), exc
    else:
        raise AssertionError("expected ConsoleError")
    server.shutdown()


def test_unreachable_console_raises():
    os.environ["CONSOLE_URL"] = "http://127.0.0.1:1"
    s = importlib.reload(sandbox)
    try:
        s.sandbox_list()
    except s.ConsoleError as exc:
        assert "unreachable" in str(exc), exc
    else:
        raise AssertionError("expected ConsoleError")


def test_contract_check_clean():
    server, s, _ = _client()
    assert s.contract_check() == [], s.contract_check()
    server.shutdown()


def test_contract_check_reports_missing_route():
    server, s, _ = _client(mode="drift")
    missing = s.contract_check()
    assert any("reachability" in item for item in missing), missing
    server.shutdown()


if __name__ == "__main__":
    failures = 0
    names = sorted(k for k in list(globals()) if k.startswith("test_"))
    for name in names:
        try:
            globals()[name]()
            print("ok  ", name)
        except Exception as exc:                       # noqa: BLE001
            failures += 1
            print("FAIL", name, "->", exc)
    print(f"\n{len(names) - failures} passed, {failures} failed")
    sys.exit(1 if failures else 0)

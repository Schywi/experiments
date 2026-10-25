"""Sandbox tool tests against a fake console (no cluster, no real console).

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


class _Handler(BaseHTTPRequestHandler):
    mode = "ok"

    def log_message(self, *a):        # silence
        pass

    def _send(self, code, payload=None, raw=None):
        self.send_response(code)
        if raw is not None:
            self.send_header("content-type", "image/png")
            self.end_headers()
            self.wfile.write(raw)
            return
        body = json.dumps(payload or {}).encode()
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _route(self):
        p = self.path
        if self.mode == "noapi" and p.startswith("/api/"):
            return self._send(404, {"error": "not found"})
        if p == "/api/sandboxes" and self.command == "GET":
            return self._send(200, {"sandboxes": [{"id": "sb-1", "ready": True,
                                                    "phase": "Ready"}]})
        if p == "/api/sandboxes" and self.command == "POST":
            return self._send(200, {"id": "sb-new", "ready": False})
        if p.endswith("/reachability"):
            return self._send(200, {"reachable": True})
        if p.endswith("/url") and self.command == "POST":
            return self._send(200, {"navigated": True})
        if p.startswith("/sandboxes/") and p.endswith("/shot"):
            return self._send(200, raw=b"\x89PNG\r\n\x1a\n")
        if p.startswith("/api/sandboxes/") and self.command == "DELETE":
            return self._send(200, {"deleted": True})
        return self._send(404, {"error": "no route"})

    do_GET = do_POST = do_DELETE = lambda self: self._route()  # noqa: E731


def _serve(mode):
    handler = type("H", (_Handler,), {"mode": mode})
    server = HTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def _reload(url):
    os.environ["CONSOLE_URL"] = url
    return importlib.reload(sandbox)


def test_list_reads_console_inventory():
    srv = _serve("ok")
    s = _reload(f"http://127.0.0.1:{srv.server_port}")
    out = s.sandbox_list()
    assert out["sandboxes"][0]["id"] == "sb-1", out
    srv.shutdown()


def test_open_returns_frame_url():
    srv = _serve("ok")
    s = _reload(f"http://127.0.0.1:{srv.server_port}")
    out = s.sandbox_open("sb-1", "http://grafana.localhost")
    assert out["image_url"].endswith("/sandboxes/sb-1/shot"), out
    assert out["result"]["navigated"] is True, out
    srv.shutdown()


def test_create_and_destroy_go_through_console():
    srv = _serve("ok")
    s = _reload(f"http://127.0.0.1:{srv.server_port}")
    assert s.create_sandbox()["id"] == "sb-new"
    assert s.destroy_sandbox("sb-1")["deleted"] is True
    srv.shutdown()


def test_missing_api_surface_raises_console_error():
    srv = _serve("noapi")
    s = _reload(f"http://127.0.0.1:{srv.server_port}")
    try:
        s.sandbox_list()
    except s.ConsoleError as exc:
        assert "console API unavailable" in str(exc), exc
    else:
        raise AssertionError("expected ConsoleError")
    srv.shutdown()


def test_unreachable_console_raises_console_error():
    s = _reload("http://127.0.0.1:1")
    try:
        s.sandbox_list()
    except s.ConsoleError as exc:
        assert "unreachable" in str(exc), exc
    else:
        raise AssertionError("expected ConsoleError")


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

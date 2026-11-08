#!/usr/bin/env python3
"""Authenticating reverse proxy in front of the MinIO console.

MinIO's console has no anonymous mode: a browser gets a login form and, once it
logs in, the session is carried in a `token` cookie. This proxy logs in once with
the root credentials from Secret `minio-credentials`, keeps that cookie, and puts
it on every request it forwards. The UI is then reachable with no login step.

Same shape as config/kargo/gateway/proxy.py — not a general-purpose proxy. It
strips hop-by-hop and client Authorization headers and buffers responses, so
WebSocket-backed console features are not proxied; object browsing, upload,
download and delete are plain HTTP and work.

The S3 API is not proxied: cmd/persist-lab signs its own requests against
minio:9000.
"""

import json
import os
import sys
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UPSTREAM = os.environ.get("MINIO_UPSTREAM", "http://minio.sandbox-demo.svc:9001")
ACCESS_KEY = os.environ["MINIO_ACCESS_KEY"]
SECRET_KEY = os.environ["MINIO_SECRET_KEY"]
LISTEN_PORT = int(os.environ.get("PORT", "8080"))

HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
    "host",
    "authorization",
    # The session cookie is set below; a client's own cookie jar would only
    # shadow it.
    "cookie",
}

_lock = threading.Lock()
_session = {"cookie": None}


def login() -> str:
    # Verified against RELEASE.2025-07-23T15-54-02Z: this console answers
    #   POST /api/v1/login {"accessKey": ..., "secretKey": ...}
    # with 204 and a `token=<jwt>` cookie — there is no JSON body to read.
    body = json.dumps({"accessKey": ACCESS_KEY, "secretKey": SECRET_KEY}).encode()
    request = urllib.request.Request(
        UPSTREAM + "/api/v1/login",
        data=body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=15) as response:
        cookies = response.headers.get_all("Set-Cookie") or []
    for cookie in cookies:
        pair = cookie.split(";", 1)[0]
        name, _, value = pair.partition("=")
        if name.strip() == "token" and value:
            print("gateway: logged in to MinIO", file=sys.stderr, flush=True)
            return value
    raise RuntimeError(f"login returned no token cookie: {cookies}")


def cookie(refresh: bool = False) -> str:
    with _lock:
        if refresh or not _session["cookie"]:
            _session["cookie"] = login()
        return _session["cookie"]


def _cookie_header(value: str) -> str:
    # Raw, not percent-encoded: this console's cookie is a base64 blob and
    # quoting it turns a valid session into "invalid session" (verified against
    # both forms on the running server).
    return "token=" + value


def _public_cookies(client_cookies: str) -> list[str]:
    """The client's cookies minus any token it carries itself."""
    kept = []
    for pair in client_cookies.split(";"):
        pair = pair.strip()
        if not pair:
            continue
        name, _, _ = pair.partition("=")
        if name.strip() == "token":
            continue
        kept.append(pair)
    return kept


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # keep the noise down
        sys.stderr.write("gateway: %s\n" % (fmt % args))

    def _forward(self, method: str) -> None:
        if self.path == "/healthz":
            self.send_response(200)
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"ok")
            return

        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else None

        response = None
        for attempt in (0, 1):
            headers = {
                key: value
                for key, value in self.headers.items()
                if key.lower() not in HOP_BY_HOP
            }
            jar = _public_cookies(self.headers.get("Cookie") or "")
            jar.append(_cookie_header(cookie(refresh=attempt == 1)))
            headers["Cookie"] = "; ".join(jar)
            request = urllib.request.Request(
                UPSTREAM + self.path, data=body, method=method, headers=headers
            )
            try:
                response = urllib.request.urlopen(request, timeout=120)
            except urllib.error.HTTPError as error:
                response = error
            if response.status == 401 and attempt == 0:
                response.close()
                continue
            break

        try:
            payload = response.read()
        finally:
            response.close()

        self.send_response(response.status)
        for key, value in response.headers.items():
            if key.lower() in ("content-length", "transfer-encoding", "connection"):
                continue
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if method != "HEAD":
            self.wfile.write(payload)

    def do_GET(self):
        self._forward("GET")

    def do_HEAD(self):
        self._forward("HEAD")

    def do_POST(self):
        self._forward("POST")

    def do_PUT(self):
        self._forward("PUT")

    def do_PATCH(self):
        self._forward("PATCH")

    def do_DELETE(self):
        self._forward("DELETE")

    def do_OPTIONS(self):
        self._forward("OPTIONS")


if __name__ == "__main__":
    cookie()
    server = ThreadingHTTPServer(("", LISTEN_PORT), Handler)
    print(f"gateway: {UPSTREAM} -> :{LISTEN_PORT}, session in hand", file=sys.stderr, flush=True)
    server.serve_forever()

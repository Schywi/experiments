#!/usr/bin/env python3
"""Authenticating reverse proxy in front of the Kargo API and UI.

Kargo has no anonymous mode, so a browser gets a login form. This proxy logs in
once with the admin account from Secret `kargo-gateway-admin`, keeps the JWT,
and injects it into every proxied request. The UI is then reachable at
http://<loadbalancer-ip>/ with no login step.

Not a general-purpose proxy: it strips hop-by-hop and client Authorization
headers, and it buffers responses (Kargo's JSON REST plus the UI assets; live
log streaming through it is not supported).
"""

import json
import os
import ssl
import sys
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UPSTREAM = os.environ.get("KARGO_UPSTREAM", "https://kargo-api.kargo.svc:443")
PASSWORD = os.environ["KARGO_ADMIN_PASSWORD"]
LISTEN_PORT = int(os.environ.get("PORT", "8080"))

# In-cluster hop to a cert signed by the local Kargo CA, which rotates when
# gen-certs.sh is re-run; verifying it here would mean shipping the CA into the
# proxy for no security gain on a pod network.
TLS = ssl.create_default_context()
TLS.check_hostname = False
TLS.verify_mode = ssl.CERT_NONE

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
    "content-length",
}

_lock = threading.Lock()
_token = {"value": None}


def login() -> str:
    # Kargo's login endpoint takes the plaintext password as a bearer token and
    # answers with a signed idToken. Verified against v1.12.0:
    #   POST /v1beta1/login  Authorization: Bearer <password>  ->  {"idToken": ...}
    request = urllib.request.Request(
        UPSTREAM + "/v1beta1/login",
        data=b"",
        method="POST",
        headers={"Authorization": "Bearer " + PASSWORD},
    )
    with urllib.request.urlopen(request, context=TLS, timeout=15) as response:
        payload = json.load(response)
    token = payload.get("idToken") or payload.get("token")
    if not token:
        raise RuntimeError(f"login returned no token: {sorted(payload)}")
    print("gateway: logged in to Kargo", file=sys.stderr, flush=True)
    return token


def token(refresh: bool = False) -> str:
    with _lock:
        if refresh or not _token["value"]:
            _token["value"] = login()
        return _token["value"]


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
            headers["Authorization"] = "Bearer " + token(refresh=attempt == 1)
            request = urllib.request.Request(
                UPSTREAM + self.path, data=body, method=method, headers=headers
            )
            try:
                response = urllib.request.urlopen(request, context=TLS, timeout=120)
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
    token()
    server = ThreadingHTTPServer(("", LISTEN_PORT), Handler)
    print(
        f"gateway: {UPSTREAM} -> :{LISTEN_PORT}, token in hand",
        file=sys.stderr,
        flush=True,
    )
    server.serve_forever()

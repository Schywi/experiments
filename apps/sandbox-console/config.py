"""Runtime configuration, read from the environment only.

Every value has a default that matches the live `experiments` cluster, so the
module can be imported in a test with nothing set. The chart passes the same
names through `values.yaml`; see `chart/values.yaml`.
"""

import os
from pathlib import Path


def _int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, default))
    except (TypeError, ValueError):
        return default


def _float(name: str, default: float) -> float:
    try:
        return float(os.environ.get(name, default))
    except (TypeError, ValueError):
        return default


# Where the sandboxes live and how the console reaches them.
SANDBOX_NAMESPACE = os.environ.get("SANDBOX_NAMESPACE", "sandbox-demo")
# The router's LoadBalancer address (Cilium LB-IPAM, lan-pool). All sandbox
# traffic goes through it: template-derived pods drop every other ingress peer.
#
# KNOWN STALE DEFAULT: 192.168.0.248 is no longer the router. The two
# kargo-gateway LoadBalancers took .248 (default/kargo-gateway) and .249
# (kargo/kargo-gateway) on 2026-10-24, the console took the last free address
# .250, and the router has been a ClusterIP ever since
# (sandbox-router-svc.agent-sandbox-system:8080, 10.43.71.0). Nothing in the
# cluster sets this default: the chart passes ROUTER_URL explicitly, so the
# running console never dials .248. A run outside the chart does, and reaches
# Kargo instead of the router.
ROUTER_URL = os.environ.get("ROUTER_URL", "http://192.168.0.248").rstrip("/")
# CDP port inside the sandbox. Chromium's default is 9222, but the router's own
# default for X-Sandbox-Port is 8888, so the header is always sent explicitly.
SANDBOX_PORT = _int("SANDBOX_PORT", 9222)
# How the console speaks DevTools to the sandbox: "http" posts commands to the
# in-pod bridge (the router cannot carry a WebSocket in v1.0.7/v1.0.8, see
# apps/browser-sandbox/cdp-bridge.py), "ws" dials Chromium through the router
# directly and is kept for when upstream fixes the 101 body wrap.
CDP_TRANSPORT = os.environ.get("CDP_TRANSPORT", "http").strip().lower()
# Bearer token for --authz-mode=tokenreview. Empty means "send none", which is
# only correct against a router running with --authz-mode=allow-all.
SANDBOX_ROUTER_TOKEN = os.environ.get("SANDBOX_ROUTER_TOKEN", "").strip()

# The warm pool claims are made against, and the pod shape a bare Sandbox gets.
WARM_POOL = os.environ.get("SANDBOX_WARM_POOL", "browser-pool")
SANDBOX_IMAGE = os.environ.get("SANDBOX_IMAGE", "127.0.0.1:5000/browser-sandbox:local")
TARGET_URL = os.environ.get("SANDBOX_TARGET_URL", "https://example.com/")

# M4: creating and deleting from the pane. When false the mutating routes answer
# 403 and the buttons are not rendered, so a read-only deployment has no dead
# controls.
WRITE_ENABLED = os.environ.get("WRITE_ENABLED", "true").strip().lower() in {"1", "true", "yes"}

# M6: the bare column of compare mode. A bare `kind: Sandbox` gets no
# NetworkPolicy, keeps cluster DNS and mounts a service-account token -- exactly
# what the comparison shows, and why it is off by default.
ALLOW_RAW_SANDBOX = os.environ.get("ALLOW_RAW_SANDBOX", "false").strip().lower() in {"1", "true", "yes"}

# M7: how many tiles the demo run fills, and the ceiling that refuses more.
DEMO_TILES = _int("DEMO_TILES", 8)
# ~9.1 actively-driven Chromium instances is what experiment 02 measured on this
# host; creating past the ceiling is refused rather than left to the scheduler.
MAX_CONCURRENT_SANDBOXES = _int("MAX_CONCURRENT_SANDBOXES", 9)

# How long a create may take before its timers are reported as partial.
CREATE_TIMEOUT_SECONDS = _float("CREATE_TIMEOUT_SECONDS", 120.0)
# How long a claim may take to be bound to a warm pod before a create-with-url
# gives up and says so. Adoption is ~1.5 s; the budget is slack for a busy node.
CREATE_ADOPT_SECONDS = _float("CREATE_ADOPT_SECONDS", 10.0)

# Stamped on every object the pane creates, so teardown removes only its own.
MANAGED_LABEL = "sandbox-console"

# Tile geometry. 1280x600 keeps a browser at ~180 MiB; the viewport never
# changes mid-demo, so click coordinates stay stable.
VIEWPORT_WIDTH = _int("VIEWPORT_WIDTH", 1280)
VIEWPORT_HEIGHT = _int("VIEWPORT_HEIGHT", 600)
# 8, not 12: experiment 02 measured ~9.1 actively-driven Chromium
# instances against this host's free CPU, and a demo should not sit above
# that line. Raise it in the chart if the desktop can take it.
MAX_TILES = _int("MAX_TILES", 8)

# Refresh cadence and screenshot reuse. The grid polls every POLL_SECONDS; a
# tile is only re-captured when its cached frame is older than SHOT_TTL_SECONDS.
POLL_SECONDS = _int("POLL_SECONDS", 3)
SHOT_TTL_SECONDS = _float("SHOT_TTL_SECONDS", 10.0)

# How often the open dialog replaces its frame. Each refresh is a real Chromium
# capture (~50-150 ms of that sandbox), which is why this is seconds and not
# milliseconds: it is a slideshow, not a screencast.
FRAME_REFRESH_MS = _int("FRAME_REFRESH_MS", 2000)

# CDP budgets. A sandbox that was just created answers slowly the first time.
CDP_CONNECT_TIMEOUT = _float("CDP_CONNECT_TIMEOUT", 5.0)
CDP_COMMAND_TIMEOUT = _float("CDP_COMMAND_TIMEOUT", 10.0)

# Isolation probes: one AbortController deadline per fetch, measured from inside
# the sandbox's own browser.
PROBE_TIMEOUT_MS = _int("PROBE_TIMEOUT_MS", 4000)
# A failure that returns faster than this never lost a packet, so the target was
# reachable (TLS or certificate error after the TCP handshake completed).
PROBE_REACHABLE_MS = _int("PROBE_REACHABLE_MS", 1000)

# In-process bound on concurrent CDP work (screenshots and probes).
CDP_CONCURRENCY = _int("CDP_CONCURRENCY", 4)
CDP_QUEUE_SIZE = _int("CDP_QUEUE_SIZE", 32)
RETRY_AFTER_SECONDS = _int("CDP_RETRY_AFTER_SECONDS", 3)
# Leaked refusal when the queue is full: the console never blocks unboundedly.
AUDIT_LOG = os.environ.get("AUDIT_LOG", "/var/log/sandbox-console/audit.jsonl")

# Run wall (research/session-lifecycle-elasticity/16-session-wall-ui.md).
#
# Where the runner writes its per-run SQLite and report JSON. The default is the
# repository checkout, which is what a console started by hand reads; the chart
# mounts the same directory from the host at /runs.
_default_runs = Path(__file__).resolve().parents[2] / "docs" / "session-lab-runs"
RUNS_DIR = Path(os.environ.get("SANDBOX_CONSOLE_RUNS_DIR", str(_default_runs)))
# Slots one Chromium is expected to serve. Only a default for the wall's free
# positions: a run's own config (`contexts_per_browser`) wins when it says
# something, and an instance that has been seen carrying more slots than this
# renders as many as it carried.
CONTEXTS_PER_BROWSER = _int("CONTEXTS_PER_BROWSER", 1)
# Ceiling on sessions read from one run, so a 10k-session database cannot make
# one page load a million DOM nodes. The wall says how many it left out.
RUN_MAX_SESSIONS = _int("RUN_MAX_SESSIONS", 4000)
# Skyline cells: sessions are bucketed into at most this many columns.
RUN_MAX_SKYLINE = _int("RUN_MAX_SKYLINE", 480)
# How often a live run's wall re-reads the database.
RUN_REFRESH_SECONDS = _int("RUN_REFRESH_SECONDS", 5)
# How long a `Target.getTargets` answer is reused. One call per instance per
# window keeps a 40-instance run at ~13 requests/second at the default 3s, which
# the bounded CDP limiter can serve without starving screenshots.
SLOT_TARGETS_TTL = _float("SLOT_TARGETS_TTL", 3.0)

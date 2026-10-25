# sandbox-console

A read-only pane over live agent-sandbox Chromium sandboxes. Twelve tiles at
most, one screenshot per tap, and a badge that says whether the sandbox can
reach the cluster. Reached at `http://192.168.0.249/`.

It exists to answer two questions on one screen: *is this actually a browser*,
and *why can I not reach my cluster from inside it*. The reasoning is in
[`research/rfc-sandbox-console.md`](../../research/rfc-sandbox-console.md); this
release covers milestones M1-M3 (grid, screenshot and tap, isolation badge).
Create/delete, compare mode and the recipe runner are not implemented, and no
code path here writes to the Kubernetes API.

```
browser ──HTTP──► console:8080 ──HTTP + X-Sandbox-*──► router (ClusterIP)
                                                          │
                                     NetworkPolicy allows only the router
                                                          ▼
                                              pod:9222  cdp-bridge.py  ──WS──►  Chromium 127.0.0.1:9223
```

Every hop the console owns is HTTP. The WebSocket to Chromium lives inside the
sandbox, in `apps/browser-sandbox/cdp-bridge.py`, because the sandbox-router
cannot forward an upgrade in v1.0.7 or v1.0.8 (`proxy.go:545` wraps the 101 body).
`CDP_TRANSPORT=ws` switches back to dialling Chromium through the router for the
day that is fixed.

## Build and deploy

```bash
config/registry/install.sh                                  # once: in-cluster registry
buildah bud -t sandbox-console:local apps/sandbox-console
config/registry/push-image.sh sandbox-console

config/sandbox/install.sh                                   # the platform it reads
helm upgrade --install sandbox-console apps/sandbox-console/chart \
  -n sandbox-console --create-namespace
```

Or let Argo CD do it: `config/argocd/applications/sandbox-console.yaml`.

## API

| Method | Path | Does |
|---|---|---|
| GET | `/` | the one screen |
| GET | `/sandboxes` | the grid fragment the page polls every 3 s |
| GET | `/sandboxes/{id}/shot` | one PNG via `Page.captureScreenshot`; cached `SHOT_TTL_SECONDS` |
| POST | `/sandboxes/{id}/click` | `{x, y, scale}` -> `Input.dispatchMouseEvent` -> refreshed tile |
| POST | `/sandboxes/{id}/reachability` | live isolation evidence, returned as the tile |
| GET | `/sandboxes/{id}/expanded` | the modal fragment |
| GET | `/healthz` | config echo and counters, no secrets |

Inside the sandbox the bridge answers `POST /cdp {"method","params"}`, `GET
/json/version` (the readiness probe) and `GET /json/list`.

There is no `POST /sandboxes`, no `DELETE`, and no recipe route: creating and
deleting are M4, compare mode is M6.

## Compare mode and the recipe runner

`POST /compare` creates a Claim and a bare `kind: Sandbox` in the same second and
times four stages on each: `t_sched`, `t_ready`, `t_cdp`, `t_paint`. The bars in
`/compare` share one scale across both columns, because 43 s next to 1.5 s only
reads as a gap when the scale is shared, and the p50 of the last three runs sits
in each bar's tooltip. The reachability row underneath is the same classifier the
tile badge uses, so the bare column shows what it costs: reachable cluster,
mounted token, cluster DNS.

The recipe runner executes three operations in-process — `claim-burst`,
`burst-then-teardown`, `capacity-check` — and streams their output as NDJSON.

## Deviations from the RFC, all deliberate

- **Claims only, no warm-pool tiles.** The router keeps unclaimed warm-pool
  members out of its name index (`sandbox-router/cache/cache.go`), so their
  screenshots can only 502. The grid draws claimed sandboxes; idle pool members
  are a counter in the header. The RFC says "lists live Sandboxes"; this is the
  version that does not lie about reachability.
- **No htmx.** The markup is server-rendered fragments, but the page swaps them
  with `fetch()` and plain DOM instead of loading htmx from a public CDN. The
  pane has to work on a LAN.
- **UnoCSS, vendored.** `static/unocss.js` is the `@unocss/runtime` bundle served
  from `/static` (241 KB, cached), configured with a shadcn-shaped theme so
  utilities read `bg-card`, `text-muted-foreground`, `border-border`,
  `rounded-lg`. Tailwind proper or the shadcn package would need a node build
  step this image does not have; `.btn`, `.badge`, `.card`, `.chip` remain the
  component layer, which is how shadcn is structured anyway.
- **Server-Sent Events, not WebSocket.** The grid subscribes to `/events` and
  gets a fragment pushed when the state changes, with polling as a fallback. SSE
  because the traffic is one-way and the router cannot carry an upgrade.
- **No outbound dashboard links.** The header links to Grafana, Hubble and Argo
  CD were removed: the pane is the pane, and a link nobody asked for is noise.
- **CDP runs over HTTP to a bridge inside the sandbox.** See the diagram above;
  the router cannot carry the WebSocket the protocol requires.
- **Recipes run in-process, not as shells.** The RFC shells
  `dev/load-test/test-recipes/` scripts, which live in the agent-sandbox checkout
  on the host; this process runs in the cluster and cannot reach them. The three
  recipes here do the same three things (burst, burst + teardown, capacity
  report) with the same bound on concurrency, and stream line by line.

## How the isolation badge is measured

Three claims are live, from one `Runtime.evaluate` that issues three `fetch`
calls **from inside the sandbox's own Chromium**:

| Claim | URL | Blocked looks like |
|---|---|---|
| `cluster` | `https://10.43.0.1:443` | `AbortController` abort at 4 s |
| `metadata` | `https://169.254.169.254` | abort at 4 s |
| `egress` | `https://1.1.1.1` | abort at 4 s (`allowed` is the good outcome here) |

A fast failure (a few ms) is a completed TCP handshake followed by a TLS or
certificate error, so it is reported `reachable`. The default-deny policy drops
rather than rejects -- experiment 01 recorded `EAGAIN`, a 4 s hang -- which is
what makes the timing a reliable discriminator.

Two claims are read from the pod spec, and the badge labels them `spec`:
`sa token` (`automountServiceAccountToken`) and `dns`
(`dnsPolicy`/`dnsConfig.nameservers`). The browser cannot distinguish "the name
does not resolve" from "the name resolves to a closed port", so claiming a live
measurement for those would be inventing evidence.

No `pods/exec` is used or granted. The Role in `chart/templates/rbac.yaml` is
`get/list/watch` on `sandboxes`, `sandboxclaims`, `pods` and `pods.metrics.k8s.io`.

## Configuration

All of it is environment variables, plumbed through `chart/values.yaml`:
`SANDBOX_NAMESPACE`, `ROUTER_URL`, `SANDBOX_PORT`, `VIEWPORT_WIDTH/HEIGHT`,
`MAX_TILES`, `POLL_SECONDS`, `SHOT_TTL_SECONDS`, `CDP_TRANSPORT`, `CDP_*`,
`PROBE_TIMEOUT_MS`, `AUDIT_LOG`. `SANDBOX_ROUTER_TOKEN` is empty by default and the
console falls back to its mounted ServiceAccount token, which the router's
TokenReview authorizer validates.

## Tests

```bash
cd apps/sandbox-console && python3 -m pytest -q
```

60 tests, no cluster and no browser: `kube.snapshot` is replaced with fixtures,
the CDP transport with an `httpx.MockTransport` for discovery and a real
`websockets` server for framing.

## Limits

- One replica, one uvicorn worker: the session cache, the screenshot cache and
  the bounded FIFO are in-process state. Scaling out multiplies the limiter.
- No auth on the pane itself. It inherits the cluster's LAN posture
  (`README.md` §Limits); the router at least requires a bearer token.
- `MAX_TILES=12` is above the ~9.1 active-browser ceiling experiment 02 measured
  on this host. Lower it if the desktop lags during a demo.
- Screenshots are on demand, not streamed. A websocket-based screencast would
  need Cilium's idle timeout raised, which is why it is not here.
- Compare history is in-process: the p50 of the last three runs is lost when the
  pod restarts. A run is cheap (~20 s), so the split view simply asks for another.
- A recipe holds the runner for its whole duration; there is no queue. The
  refusal is a 409 naming the recipe that is already running.

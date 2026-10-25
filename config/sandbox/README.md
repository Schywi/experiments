# config/sandbox — the agent-sandbox platform the console draws from

Three objects, plus the namespaces they live in:

| Object | Where | What it does |
|---|---|---|
| `sandbox-router` Deployment/Service | `agent-sandbox-system` | Routes `X-Sandbox-ID` + `X-Sandbox-Port` requests to a sandbox pod. LoadBalancer IP `192.168.0.248`. |
| `SandboxTemplate` `browser-tpl` | `sandbox-demo` | The hardened pod shape: Chromium on 9222, readiness on `/json/version`, generated default-deny NetworkPolicy. |
| `SandboxWarmPool` `browser-pool` | `sandbox-demo` | 8 pre-provisioned sandboxes, so a claim is adopted in ~1.5 s instead of started cold in ~40 s. |

The **controller and the four CRDs are not here**. Upstream publishes them as a
release manifest; `install.sh` downloads the pinned file, verifies its sha256 and
applies it. Vendoring a 428 KB generated manifest into this repo would mean
reviewing upstream's RBAC by eye on every bump.

## Install

```bash
config/sandbox/install.sh --check   # download + verify only
config/sandbox/install.sh           # CRDs + controller, then this chart
```

Then build the browser image, which the template references:

```bash
buildah bud -t browser-sandbox:local apps/browser-sandbox
config/registry/push-image.sh browser-sandbox
kubectl -n sandbox-demo rollout status sandboxwarmpool/browser-pool   # optional
```

## The four things that silently break this stack

Each of these produces a UI symptom, not an error message, which is why they are
written down here rather than discovered during a demo:

0. **The router cannot forward a WebSocket.** `sandbox-router/proxy/proxy.go:545`
   wraps every upstream body in a counting wrapper, so Go's ReverseProxy aborts
   the `101` with `101 switching protocols response with non-writable body`. The
   console therefore speaks HTTP to `cdp-bridge.py` inside the sandbox, which
   holds the WebSocket over loopback. Reproduced on v1.0.7 and v1.0.8.
1. **Router egress on 9222.** Upstream's `sandbox-router/deploy/networkpolicy.yaml`
   allows sandbox port `8888` only. `templates/router-networkpolicy.yaml` adds
   `9222`; without it every screenshot is a 502 after four dial retries.
2. **`X-Sandbox-Port` is not optional.** The router defaults to `8888`, not to
   Chromium's `9222`. The console always sends the header.
3. **Unclaimed warm-pool members cannot be routed.** The router keeps them out
   of its name index on purpose (`sandbox-router/cache/cache.go`), so a `Sandbox`
   with the `agents.x-k8s.io/warm-pool-sandbox` label is a number in the
   console header, never a tile.
4. **`/json/list` reports a loopback socket.** It answers
   `ws://localhost:9222/devtools/page/<id>`, which another pod cannot dial. The
   console rewrites host and port to the router and carries the routing headers
   on the WebSocket handshake.

## Verify before pointing the console at it

```bash
# CDP answers through the router, using a claimed sandbox name
kubectl -n sandbox-demo get sandboxes -o custom-columns=NAME:.metadata.name,READY:.status.conditions[?\(@.type==\"Ready\"\)].status

TOKEN="$(kubectl create token sandbox-console -n sandbox-console 2>/dev/null || kubectl create token default)"
curl -sS -H 'X-Sandbox-ID: <name>' -H 'X-Sandbox-Port: 9222' \
     -H "Authorization: Bearer ${TOKEN}" \
     http://192.168.0.248/json/version

# The generated NetworkPolicy: router-only ingress, LAN/private egress blocked
kubectl -n sandbox-demo get networkpolicy -o yaml
```

A `400 {"detail":"X-Sandbox-ID header is required."}` means the request reached
the router. A `401` means the token is missing or the auth-delegator binding did
not apply. A `502` means the router reached the sandbox name but not the port:
re-read item 1 above.

## Sizing

`browser-pool: 8` is a measured choice, not a round number. Experiment 02
(`agent-sandbox/research/experiments/02-capacity-6vcpu-100k.md`) puts this host
at **~9.1 actively-driven Chromium instances** against free CPU, while idle
memory would allow ~28. A 12-tile grid on a 6-core desktop sits above that line;
the pool leaves headroom for the console's own captures.

## Teardown

```bash
helm uninstall sandbox -n agent-sandbox-system
kubectl delete ns sandbox-demo
# The CRDs and the controller stay installed: experiment 01 left them in place,
# and another workload may be using them.
```

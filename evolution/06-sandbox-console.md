# Phase 6 — agent-sandbox and the console that proves it (2026-10-18 → )

**Branch:** `feat/sandbox-console` · **tracked paths:**
[`../apps/sandbox-console/`](../apps/sandbox-console/README.md),
[`../apps/browser-sandbox/`](../apps/browser-sandbox/README.md),
[`../config/sandbox/`](../config/sandbox/README.md) · **design note:**
[`../research/rfc-sandbox-console.md`](../research/rfc-sandbox-console.md)

## The question

Two claims about agent-sandbox are easy to assert and hard to demonstrate from a
terminal: that a sandbox is a real browser, and that it cannot reach the
cluster. `kubectl get sandboxes` proves neither. A screenshot of a live
Chromium proves the first; a four-second hang on `10.43.0.1:443` proves the
second.

The console is the smallest thing that shows both at once. It does not
reimplement Grafana, Hubble or Argo CD, and it does not link to them either: the
pane is the pane.

## What exists, and where it lives

The agent-sandbox project is a Kubernetes SIGs repository, checked out read-only
at `~/Documentos/git_repos/agent-sandbox`. Nothing in this phase wrote to it.
What the cluster runs comes from two places:

| Piece | Source | Applied by |
|---|---|---|
| Four CRDs + controller `v1.0.7` | upstream release manifest, verified by sha256 `136c10c9…` | `config/sandbox/install.sh` |
| Router, browser `SandboxTemplate`, warm pool | `config/sandbox/` (this repo) | the same script, or the Argo Application |
| Chromium-with-CDP image | `apps/browser-sandbox/` (this repo) | `buildah` + `config/registry/push-image.sh` |
| The pane | `apps/sandbox-console/` (this repo) | `helm upgrade --install` or Argo |

Pinning the controller by release and digest keeps upstream's generated RBAC out
of this repo's review surface; the objects that are ours (the router flags, the
policy, the template) are hand-written and linted by `config/ci/validate.sh`.

## The four things that turn into a 502

Found by reading the router and the template controller, before writing the
console:

1. **The router's own NetworkPolicy allows egress TCP `8888` only.** Chromium
   listens on `9222`. Upstream's policy is correct for the default payload and
   wrong for this one, so `config/sandbox/templates/router-networkpolicy.yaml`
   adds the port. The symptom is not an error message; it is every tile showing
   `unreachable` after four dial retries.
2. **`X-Sandbox-Port` defaults to `8888`, not to Chromium's port.** The header
   is sent explicitly on every request and on the WebSocket handshake.
3. **Chromium binds the DevTools port to loopback by default.** Without
   `--remote-debugging-address=0.0.0.0` the router dials the pod IP and reaches
   nothing.
4. **`/json/list` answers with a loopback socket URL.** `ws://localhost:9222/...`
   is unusable from another pod, so the console rewrites host and port to the
   router and keeps the path.

A fifth is not a port at all: the router deliberately keeps unclaimed warm-pool
pods out of its name index (`sandbox-router/cache/cache.go`), so a grid built
from "every Sandbox" would draw tiles whose screenshots can never succeed. The
console draws claimed sandboxes and counts idle pool members in the header.

A sixth needed a shim. Chromium 130 binds DevTools to `127.0.0.1` whatever
`--remote-debugging-address` says (verified in both headless modes, from inside
the pod's network namespace), and the sandbox-router cannot forward the WebSocket
the protocol requires: `sandbox-router/proxy/proxy.go:545` wraps every upstream
response body in a byte-counting wrapper, so Go's `ReverseProxy` aborts the `101`
with `101 switching protocols response with non-writable body`. That reproduces
on v1.0.7 and v1.0.8, and the metrics transport that wraps the body is created
unconditionally (`cmd/main.go:117`), so no flag avoids it.

`apps/browser-sandbox/cdp-bridge.py` answers it: plain HTTP on the pod IP,
holding the WebSocket to Chromium over loopback. Every hop the console owns is
HTTP; the router keeps its router-only ingress property; the upgrade never
crosses it. `CDP_TRANSPORT=ws` restores the direct path for the day upstream
fixes the wrap. The upstream bug is worth reporting on its own: their README
advertises code-server and VSCode through this router.

## What the isolation badge actually measures

The RFC asked for `nc 10.43.0.1:443` from inside the pod, which means
`pods/exec`. That is arbitrary command execution granted to a pane the RFC calls
read-only, so the probe runs in the sandbox's own Chromium instead: one
`Runtime.evaluate` issuing three `fetch` calls with a 4 s `AbortController`.

The classification depends on *how* the fetch failed. Experiment 01 recorded
`EAGAIN` — a silent drop with a four-second hang — so an abort at the deadline
means the packet went nowhere. A failure after a few milliseconds is a completed
TCP handshake followed by a certificate error, which is what a reachable API
server looks like from an unauthenticated browser. Two claims (`sa token` and
`dns`) cannot be measured this way — a name that does not resolve and a name
that resolves to a closed port are indistinguishable to `fetch` — so they are
read from the pod spec and the badge labels them `spec`.

The Role the console holds is `get/list/watch` on `sandboxes`, `sandboxclaims`,
`pods` and `pods.metrics.k8s.io`, in one namespace. There is no `pods/exec` and
no write verb.

## Sizing, taken from measurement rather than from the RFC

| | |
|---|---|
| Concurrent Chromium by idle memory | ~28 (250 MiB each, ~9 GiB free) |
| Concurrent Chromium by **active** CPU | **~9.1** (experiment 02, 0.3 vCPU per driven browser) |
| Warm pool | 8 (`config/sandbox/values.yaml`) |
| Grid ceiling | 12 tiles (`MAX_TILES`) |

The RFC's 12 tiles sit above what a 6-core desktop can drive at once. The pool
is 8 so the demo has headroom; `MAX_TILES` stays 12 so the pane can show what
happens past the line rather than hiding it.

## M4-M7: the pane drives, compares and tears down

| Milestone | What shipped |
|---|---|
| M4 | `POST /sandboxes` creates a Claim, or a bare `kind: Sandbox` when `allowRawSandbox` is on; `DELETE /sandboxes/{id}` removes the Claim that owns the tile, or the Sandbox itself. Past 9 live sandboxes the create is refused with 409 |
| M5 | Three recipes — `claim-burst`, `burst-then-teardown`, `capacity-check` — streamed as NDJSON, one at a time. A measured run: 2/2 claims ready in 393 ms |
| M6 | `/compare` fires a warm Claim and a bare Sandbox in the same second, times `t_sched`/`t_ready`/`t_cdp`/`t_paint` on each, draws both columns on one scale and puts the p50 of the last three in each bar's tooltip |
| M7 | `POST /demo` fills the grid to 8, `POST /demo/teardown` deletes every claim the pane created (label `sandbox-console=managed`), returning the pool to its baseline |

Two deviations worth naming. Recipes run in-process: the RFC shells
`dev/load-test/test-recipes/` scripts, which live on the host, and this process
runs in the cluster, so the same three operations are executed directly with the
same concurrency bound. And the write verbs are deliberately narrow — `create`
and `delete` on `sandboxes` and `sandboxclaims`, nothing else — so templates,
pools and policies stay GitOps-owned and the pane cannot become a second
reconciler.

## Verify it

```bash
cd apps/sandbox-console && python3 -m pytest -q     # 60 tests, no cluster
bash config/ci/validate.sh                          # charts + kubeconform
curl -sS http://192.168.0.249/sandboxes | head      # the grid fragment
curl -sS -H 'X-Sandbox-ID: <claimed>' -H 'X-Sandbox-Port: 9222' \
     http://192.168.0.248/json/version              # CDP answers through the router
```

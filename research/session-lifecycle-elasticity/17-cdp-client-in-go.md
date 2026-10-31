# Note 17 — A CDP client in Go, in the runtime

| | |
|---|---|
| Status | Design, not implemented |
| Date | 2026-10-30 |
| Phase | 8 — session lifecycle elasticity |
| Code | proposed `internal/runtime/browser/`, `apps/browser-sandbox/entrypoint.sh`, `config/sandbox/templates/sandboxtemplate.yaml` |
| Follows | [16 — browser contexts and slots](16-browser-contexts-and-slots.md) |

---

## 1. Why the runner should speak CDP itself

The runner's browser work currently goes through the Python bridge as plain HTTP
(`POST /cdp {"method":…}`). That hop cost us today's outage: the bridge held a *page* socket,
`Target.createBrowserContext` is a browser-domain command, and Chromium's refusal arrived as
`502 Bad Gateway: WebSocketError: Not allowed`.

Three things the bridge cannot give the runtime:

1. **Sessions.** Flattened CDP sessions (`Target.attachToTarget{flatten:true}` → `sessionId`) let one
   connection drive many contexts with per-context events and commands. The bridge is stateless.
2. **Events.** `Page.loadEventFired`, `Target.targetCrashed`, `Page.frameNavigated`. The runner
   currently sleeps for the hold duration; it cannot know that a page loaded or a renderer died.
3. **Per-context metrics.** `Performance.getMetrics` per attached session is the only route to
   per-session CPU (note 16 §4 left this open because the cgroup counter belongs to the pod).

None of that is UI work. It is browser-session semantics, which places it in the demand plane:
`internal/runtime/browser`.

## 2. Topology

```
runner (Go, on the host)
  │  ws://<podIP>:9224/devtools/browser/<id>          ← raw TCP, published by cdp-forward.py
  ▼
browser pod
  ├── cdp-forward.py :9224 ──TCP──► 127.0.0.1:9223 ── Chromium DevTools (browser target)
  └── cdp-bridge.py  :9222 ──HTTP──► 127.0.0.1:9223    (unchanged: console + kubelet probe)
```

Two constraints force the pod side to stay:

- Chromium 130 binds DevTools to `127.0.0.1` regardless of `--remote-debugging-address`
  (`apps/browser-sandbox/cdp-forward.py:1-20`, verified on this cluster).
- The sandbox-router cannot proxy a WebSocket upgrade (`sandbox-router/proxy/proxy.go:540-550`
  wraps the response body for its byte counter).

What changes is that the forwarder becomes a **dumb TCP proxy** (`cdp-forward.py`, already in the
image, enabled by `CDP_WS_PORT`) instead of a protocol translator. The runner dials the pod directly —
the same path `Reset` already uses, so host → pod IP is reachable today (measured reset 20–39 ms).

## 3. Package layout

```
internal/runtime/browser/
├── conn.go       one WebSocket to the browser endpoint; JSON-RPC id ↔ result, ctx timeouts
├── browser.go    /json/version discovery, browser-level commands, event fan-out
├── context.go    NewContext, NewTarget, Dispose, CloseTarget
├── session.go    Attach(targetID) → flattened session; Do/DoAll; per-session event stream
├── metrics.go    Performance.getMetrics → Usage (TaskDuration, ScriptDuration, JSHeapUsedSize…)
└── crash.go      Target.targetCrashed / socket close → instance marked unhealthy
```

Rules that keep the plane boundary honest:

- `runtime/browser` imports **no** Kubernetes client and knows nothing about claims or pools.
- `infra/kube` loses its CDP calls and only reports an address: `func (k *Kube) CDPAddr(instance) string`.
- `internal/infra/sandbox` keeps `BrowserSession` (context id, target id); the transport moves out of
  the adapter so the fake stays a fake.
- The runner owns the browser connection per instance, and hands sessions out from `slots`.

## 4. Connection model

One WebSocket per Chromium, opened lazily on the first session and closed when the instance leaves
the pool. A single reader goroutine demultiplexes frames:

| Frame | Destination |
|---|---|
| `{"id":n,"result":…}` | the caller waiting on `n` |
| `{"id":n,"error":{"code":…,"message":…}}` | same, as a typed `CDPError` |
| `{"method":…,"sessionId":s}` | that session's event channel |
| `{"method":…}` (no sessionId) | browser-level: `Target.targetCrashed`, `Target.targetDestroyed` |

Per-command timeout from the call's context; a socket that stops answering is closed and the
instance is retired rather than reused — a half-open DevTools socket poisons every later call (the
Python bridge learned this the hard way, `cdp-bridge.py:296-302`).

## 5. Per-context usage

```go
usage, err := session.Metrics(ctx)   // Performance.getMetrics
// usage.TaskDurationSeconds, usage.ScriptDurationSeconds, usage.JSHeapUsedBytes, usage.LayoutCount
```

Sampled at hold start and hold end, differenced per session. This is **not** the same quantity as
`cpu.stat`'s `usage_usec`: task duration counts main-thread work, not total process CPU. So it lands
in its own columns (`task_duration_ms`), and the cgroup figure stays the instance truth. Report both;
never add them.

## 6. Crash handling (the blast radius note 16 accepted)

`Target.targetCrashed` with the affected `targetId`, or a socket close, means every context on that
browser is gone. Action:

1. mark the instance unhealthy, `slots.Retire` it, delete the claim;
2. settle each session attached to it as `lost_unknown` with the browser's reason;
3. let the next session create a fresh instance.

That turns "a crash takes eight sessions" into "a crash is recorded as eight `lost_unknown` and the
pool replaces the browser", which is the honest version of the trade-off.

## 7. Transport switch

| Flag | Behaviour |
|---|---|
| `--cdp-transport http` (default until proven) | today's bridge: one command per HTTP call, no events |
| `--cdp-transport ws` | direct browser WebSocket: contexts, sessions, events, per-context metrics |

`--cdp-port 9224` (the `CDP_WS_PORT` the pod publishes). The image already starts the forwarder when
`CDP_WS_PORT` is non-zero (`entrypoint.sh:69-71`); the template needs a second `containerPort` and
the value set. The NetworkPolicy currently admits the host on the CDP port (proven by `Reset`), so
the new port needs the same rule.

## 8. The WebSocket dependency

Two options:

| Option | Cost | Note |
|---|---|---|
| `github.com/coder/websocket` | one small dependency, context-aware, `Dial`/`Reader`/`Writer`, handles fragmentation and close codes | recommended: the module already carries client-go, prometheus, sqlite; one more focused dependency is cheaper than protocol bugs |
| hand-rolled RFC 6455 client | ~200 lines, zero dependencies | the Python implementation is 100 lines and has already produced two classes of bug: masking, and the page-vs-browser endpoint |

## 9. Test plan

1. **Unit, no cluster:** `httptest` server + `coder/websocket` accept loop that mimics the four
   frames a browser sends (`/json/version`, `Target.createBrowserContext`, `Target.createTarget`,
   attach, dispose). Tests: id/result matching, error frames as `CDPError`, session event routing,
   timeout → socket closed, crash event → unhealthy + `Retire`.
2. **Live smoke:** one pod, create 4 contexts, load a page in each, read `Performance.getMetrics`,
   dispose all, assert zero contexts left (`Target.getTargets`).
3. **Then** a `--layer session` run at 8 contexts.

## 10. Work order

1. Template + entrypoint: expose `CDP_WS_PORT=9224`, add the container port and the policy rule.
2. `runtime/browser`: conn, browser discovery, context/target lifecycle.
3. Sessions, events, crash path.
4. `performance` metrics -> new session columns.
5. Runner: `--cdp-transport ws` path through `runtime/browser`; `slots` unchanged.
6. Delete `Adapter.OpenSession/CloseSession` from `infra/kube` once `ws` is the default.

## 11. Decisions taken here (revisit if wrong)

- Package in `internal/runtime/browser`, because contexts, sessions and metrics are session
  semantics and the demand plane is where the runner's decisions live.
- The Python bridge stays: the console drives `Page.*` over HTTP and the kubelet probe reads
  `/json/version` from it. It stops being on the runner's path.
- One connection per Chromium, not one per session: sessions are cheap on top of an existing socket,
  and this is the whole reason for multiplexing.

# browser-sandbox

The image a tile shows. Headless Chromium with the DevTools protocol on
`0.0.0.0:9222`, driven by `apps/sandbox-console` through the sandbox-router.

```
console ──HTTP──► router:8080 ──HTTP──► pod:9222 cdp-bridge.py ──WS──► Chromium
                  X-Sandbox-ID / X-Sandbox-Port        (loopback 127.0.0.1:9223)
```

`cdp-bridge.py` exists because of two facts, both verified on this cluster:

* Chromium 130 binds DevTools to `127.0.0.1` and ignores
  `--remote-debugging-address=0.0.0.0` (same in `--headless=old`).
* The sandbox-router cannot forward a WebSocket: `proxy.go:545` wraps every
  upstream body, so Go's ReverseProxy aborts the `101` with
  `101 switching protocols response with non-writable body`. Reproduced on
  v1.0.7 and v1.0.8, and the metrics transport that does the wrapping cannot be
  disabled.

So the bridge holds the WebSocket over loopback and publishes plain HTTP on the
pod IP. `cdp-forward.py` is the raw TCP publisher for the WebSocket transport,
started only when `CDP_WS_PORT` is set.

## Build and push

Built on the host with buildah, pushed to the in-cluster registry, exactly like
the other apps:

```bash
buildah bud -t browser-sandbox:local apps/browser-sandbox
config/registry/push-image.sh browser-sandbox     # -> 127.0.0.1:5000/browser-sandbox:local
```

## Why these Chromium flags

| Flag | Why |
|---|---|
| `--remote-debugging-address=0.0.0.0` | Chromium binds CDP to loopback by default. The router dials the *pod IP*, so a loopback bind makes every screenshot a 502 after the router's retry budget. |
| `--no-sandbox` | The container cannot give Chromium the namespaces its own sandbox wants (experiment 02, Q2). The network policy is the boundary here, not Chromium's sandbox. |
| `--disable-dev-shm-usage` | `/dev/shm` is small in a pod; the template also mounts a memory-backed `emptyDir` there. |
| `--headless=new` | No X server, no compositor, no VNC. A screenshot does not need a display. |
| `--remote-debugging-port=9223` | Chromium listens on loopback only; `cdp-bridge.py` owns the published 9222. |

The page is `TARGET_URL` (default `https://example.com/`). `WINDOW_SIZE` starts
the window at the tile geometry; the console also sends
`Emulation.setDeviceMetricsOverride` so the viewport is exactly 1280×600
regardless of what the window manager did.

## The template points here

`config/sandbox/templates/sandboxtemplate.yaml` sets this image, port `9222`,
`imagePullPolicy: IfNotPresent`, a `readinessProbe` on `/json/version`, and
`restartPolicy: Never`. The image alone is not enough: only a pod derived from a
`SandboxTemplate` gets the default-deny NetworkPolicy, `dnsPolicy: None` and
`automountServiceAccountToken: false` that make the isolation badge mean
anything.

## Limits

- One page per sandbox. A second tab would show up in `/json/list` and the
  console would follow it, which is not what a tile means.
- ~250 MiB resident per browser (upstream measurement, `examples/gke-swap`).
- Chromium's own sandbox is off, so the isolation this image offers is the pod
  and the NetworkPolicy: `runc` on a shared kernel. Experiment 02 records why a
  stronger runtime is not installable on this host.

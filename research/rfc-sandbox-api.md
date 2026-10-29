# RFC — `sandbox-console` HTTP API for tools

| | |
|---|---|
| Status | Implemented and live |
| Date | 2026-10-25 |
| Scope | Six JSON routes in `apps/sandbox-console`, next to the HTML routes the pane already serves |
| Answering | "Do we expose APIs from the console, or does the assistant grow its own sandbox tools?" |
| Live | `http://192.168.0.250:8080/api/sandboxes` |

---

## 1. Summary

The console answered a person: HTML fragments, one per control. An assistant's
tool call is not a browser, so the same six actions are also served as JSON, from
the same handlers. There is one implementation of routing, readiness and frame
freshness, and both callers use it.

## 2. Why the console owns this

The console holds four things a tool would otherwise have to rebuild:

| Fact | Where it lives |
|---|---|
| Which sandboxes are routable (`X-Sandbox-ID` + `X-Sandbox-Port: 9222` + a TokenReview bearer) | `cdp.py:124` |
| An unclaimed warm-pool member is not routable, so it must not become a tile | `inventory.py`, `sandbox-router/cache/cache.go` |
| A frame older than the last action is the wrong frame (`?fresh=1`) | `server.py`, `Screen.shot` |
| The page a sandbox is on comes from the browser (`location.href`), not from the CR | `Screen.store_url` |

A second implementation of those four facts would disagree with this one within a
week. So: the console exposes the API, the assistant's tools are thin clients.

## 3. Routes

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/sandboxes` | — | counters + every claimed tile |
| GET | `/api/sandboxes/{id}` | — | one tile |
| POST | `/api/sandboxes/{id}/url` | `{"url": "…"}` | where it landed + tile |
| POST | `/api/sandboxes/{id}/click` | `{"x": 640, "y": 300, "scale": 1.0}` | tile |
| POST | `/api/sandboxes/{id}/scroll` | `{"dy": 400}` | clamped delta + tile |
| POST | `/api/sandboxes/{id}/reachability` | — | isolation evidence + tile |
| GET | `/sandboxes/{id}/shot?fresh=1` | — | `image/png` (pre-existing) |

Frames are not duplicated under `/api`. `/sandboxes/{id}/shot` already returns
`image/png` and is the URL the tile payloads carry.

The HTML routes (`/`, `/sandboxes`, `/sandboxes/{id}/expanded`, `/events`, and the
HTML variants of the actions) are untouched.

## 4. Shapes

A tile is `inventory.Tile.to_dict()` plus one field:

```json
{
  "id": "browser-pool-hjslp",
  "phase": "ready",
  "age_s": 1046,
  "pod_ip": "10.42.0.184",
  "node": "fedora",
  "template": "browser-tpl",
  "claim": "claim-mvojqmnq",
  "launch_type": "warm",
  "mem_working_set_bytes": 146161664,
  "cpu_millicores": 4,
  "reachability": null,
  "screenshot_ts": 1792976299,
  "note": null,
  "url": "http://192.168.0.250:8080/",
  "shot": "/sandboxes/browser-pool-hjslp/shot?v=1792976299"
}
```

`shot` carries the timestamp of the cached frame, so a client that polls
`/api/sandboxes/{id}` learns when a new capture exists without downloading pixels.

The list route adds what the header of the pane shows:

```json
{"ok": true, "namespace": "sandbox-demo", "tiles": [ … ],
 "running": 1, "pending": 0, "pool_ready": 8, "pool_idle": 8,
 "errors": [], "max_tiles": 8}
```

Action routes answer `{"ok": true, …}` plus the refreshed tile. `url` answers the
page the browser ended up on, which is what a redirect or an `https` upgrade
actually produced:

```json
{"ok": true, "url": "https://example.com/", "tile": { … }}
```

Reachability is the probe's own vocabulary, measured inside the sandbox's
Chromium:

```json
{"ok": true,
 "reachability": {
   "cluster": "blocked", "metadata": "blocked", "sa_token": "absent",
   "cluster_dns": "clusterfirst", "egress_public_ok": true,
   "evidence": {"cluster": {"ms": 4001, "outcome": "timeout", "source": "live"},
                "public":  {"ms": 184,  "outcome": "ok",      "source": "live"},
                "sa_token": "spec", "cluster_dns": "spec"}},
 "tile": { … }}
```

## 5. Errors

| Case | Status | Detail |
|---|---|---|
| Unknown sandbox | 404 | `no sandbox <id>` |
| Scheme outside http/https | 400 | `only http, https urls can be opened, not file` |
| `dy: 0` | 400 | `dy is zero, nothing to scroll` |
| Queue full | 429 | `at capacity`, with `Retry-After` |
| Router or bridge failure | 502 | `router: <detail>` or `cdp: <detail>` |

A tool sees the same statuses the pane shows a person, because both go through
`_http_error`. Every action writes the same audit line the pane writes, plus a
`*_failed` line when it fails.

## 6. Tool mapping

| Tool | Calls |
|---|---|
| `sandbox_list` | `GET /api/sandboxes`, read `tiles[].id`, `phase`, `note`, `url` |
| `sandbox_open` | `POST /api/sandboxes/{id}/url`, then `GET` the tile's `shot` |
| `sandbox_shot` | `GET` the tile's `shot` |
| `sandbox_click` / `sandbox_scroll` | the matching POST, then `shot` |
| `sandbox_evidence` | `POST /api/sandboxes/{id}/reachability` |
| create / destroy | unchanged: `POST /sandboxes`, `DELETE /sandboxes/{id}` (already JSON) |

Two rules for the tool layer: read `phase` instead of inferring readiness, and use
the returned `shot` path instead of building one.

## 7. Non-goals

- No new cluster access. The RBAC Role is unchanged: `get,list,watch,create,delete`
  on sandboxes and claims, `get,list` on pods and pod metrics, in one namespace.
- No authentication of its own. The console is a LAN service with no login; a tool
  that can reach `192.168.0.250` can call it, exactly like a browser can.
- No websocket transport for tools. The router cannot carry an upgrade, and the
  in-pod bridge already speaks HTTP.
- No create/delete under `/api`. Those routes were already JSON and their contract
  did not change.

## 8. How this was verified

```
curl -sS http://192.168.0.250:8080/api/sandboxes | head -c 200
curl -sS -X POST -H 'Content-Type: application/json' \
     -d '{"url":"https://example.com/"}' \
     http://192.168.0.250:8080/api/sandboxes/browser-pool-hjslp/url
curl -sS -o /tmp/f.png -w '%{http_code} %{size_download}\n' \
     'http://192.168.0.250:8080/sandboxes/browser-pool-hjslp/shot?fresh=1'
curl -sS -X POST -H 'Content-Type: application/json' -d '{"x":640,"y":300,"scale":1}' \
     http://192.168.0.250:8080/api/sandboxes/browser-pool-hjslp/click
curl -sS -X POST -H 'Content-Type: application/json' -d '{"dy":400}' \
     http://192.168.0.250:8080/api/sandboxes/browser-pool-hjslp/scroll
curl -sS -X POST http://192.168.0.250:8080/api/sandboxes/browser-pool-hjslp/reachability
```

Observed: list returns one ready tile with `claim-mvojqmnq`; navigate answers
`{"ok":true,"url":"https://example.com/"}`; the frame is `200` `image/png`
70,319 bytes; click and scroll answer with refreshed tiles; reachability reports
`cluster: blocked`, `egress_public_ok: true`; `GET /api/sandboxes/nope` is 404;
`file:///etc/passwd` is 400; `dy: 0` is 400.

Unit tests: `cd apps/sandbox-console && python3 -m pytest -q` → 172 passed,
including the API routes, their 404s, the payloads passed to click/scroll, and the
evidence dict.

## 9. Open questions

1. A poll on `/api/sandboxes/{id}` re-lists the cluster on each call (`Screen.report`
   does three API lists). At eight tiles and one caller that is fine; a tool that
   polls at 1 Hz would want a cached report with a short TTL.
2. `reachability` runs three fetches with a 4 s deadline inside the sandbox, so the
   route can take 12 s worst case. A tool calling it per turn should expect that.
3. There is no route that returns "the frame *and* the page's title/url" in one
   call. A tool that wants both does a `GET` on the tile and a `GET` on the shot.

---

## 10. Full surface, as served

`GET /openapi.json`, `/docs` (Swagger UI) and `/redoc` answer 200 on the live
console. `/docs` pulls its Swagger assets from a CDN, so on a network without
internet use `/openapi.json` instead.

Every route the process serves, with what it takes and what it answers:

| Method | Path | Takes | Answers |
|---|---|---|---|
| GET | `/` | — | HTML page: counters, tiles, isolation badges |
| GET | `/events` | — | `text/event-stream`: `grid` on change, heartbeat otherwise |
| GET | `/healthz` | — | config echo, tile count, `durable_timings` |
| GET | `/static/{name}` | path | vendored UnoCSS runtime, cached 1 day |
| GET | `/sandboxes` | — | HTML fragment: the grid |
| GET | `/sandboxes/{id}/expanded` | path | HTML fragment: the dialog |
| GET | `/sandboxes/{id}/shot` | path, `?fresh=1` | `image/png`; `fresh` skips the cache read |
| POST | `/sandboxes/{id}/click` | `ClickRequest` | HTML: refreshed card |
| POST | `/sandboxes/{id}/scroll` | `ScrollRequest` | HTML: refreshed card |
| POST | `/sandboxes/{id}/url` | `UrlRequest` | HTML: refreshed card |
| POST | `/sandboxes/{id}/reachability` | path | HTML: refreshed card |
| POST | `/sandboxes/{id}/compare` | path | HTML: two-column comparison |
| POST | `/sandboxes` | `CreateRequest` | `202` `{kind, name, namespace}` |
| DELETE | `/sandboxes/{id}` | path | `202` `{removed}` |
| GET | `/recipes` | — | `{recipes: [{name, …}]}` |
| POST | `/recipes/{name}` | `?count=&hold=` | `application/x-ndjson` stream |
| POST | `/compare` | — | JSON run record |
| GET | `/compare` | — | HTML page |
| POST | `/demo` | — | `202` `{created: […]}` |
| POST | `/demo/teardown` | — | `{removed: […], failed: […]}` |
| GET | `/api/sandboxes` | — | JSON: counters + tiles |
| GET | `/api/sandboxes/{id}` | path | JSON: one tile |
| POST | `/api/sandboxes/{id}/url` | `UrlRequest` | JSON: `{ok, url, tile}` |
| POST | `/api/sandboxes/{id}/click` | `ClickRequest` | JSON: `{ok, tile}` |
| POST | `/api/sandboxes/{id}/scroll` | `ScrollRequest` | JSON: `{ok, dy, tile}` |
| POST | `/api/sandboxes/{id}/reachability` | path | JSON: `{ok, reachability, tile}` |

### Request bodies

| Model | Fields | Defaults |
|---|---|---|
| `ClickRequest` | `x`, `y`, `scale` | `scale: 1.0`; `x`/`y` are display pixels, divided by `scale` and clamped to the 1280x600 viewport |
| `ScrollRequest` | `dy` | none; positive scrolls down, clamped to ±2000, `0` is a 400 |
| `UrlRequest` | `url` | none; scheme must be `http` or `https`, bare host is read as `https://host` |
| `CreateRequest` | `kind` | `"claim"`; `"sandbox"` needs `ALLOW_RAW_SANDBOX` |

### Status codes in use

| Code | Meaning here |
|---|---|
| 200 | done; the body differs per route family (HTML fragment, JSON, or PNG) |
| 202 | accepted: a claim or sandbox was created; the object does not exist yet |
| 400 | the request is valid HTTP but not something the pane will do: bad scheme, `dy: 0`, invalid name |
| 403 | `WRITE_ENABLED` is off, or a bare sandbox was asked for without `ALLOW_RAW_SANDBOX` |
| 404 | no such sandbox |
| 409 | the concurrency ceiling would be exceeded, or a recipe is already running |
| 422 | FastAPI validation: a missing or mistyped field in the JSON body |
| 429 | the CDP queue is full; `Retry-After` is set |
| 502 | the router or the in-pod bridge failed; the detail carries which |

### Known defect

`GET /sandboxes/{id}/shot` does not check whether the sandbox is in the report.
For a name that no longer exists it answers `502 {"detail":"router: …"}` where the
other six routes answer `404 {"detail":"no sandbox …"}`. A tool that guesses a
name therefore sees a routing failure instead of a missing sandbox.

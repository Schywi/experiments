# Note 16 — The session wall: the console as the surface where slots are visible

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Branch | `research/session-lifecycle-elasticity` |
| Scope | `apps/sandbox-console/` only. No Go, no change to the existing routes |
| Subject | How the console draws a run: one seat per slot, `(instance, browserContextId)` |
| Related | [15 — capacity plane and demand plane](15-capacity-plane-and-demand-plane.md) §3, [`../rfc-sandbox-console.md`](../rfc-sandbox-console.md) |

## 1. What the console is for, after this note

`sandbox-console` exists to answer two questions that need pixels: what is this instance
showing right now, and what did it cost. `apps/session-lab/` answers a third question, how
many sessions the platform delivers and where it stops, and until now the console could
only render the first question, because its data model is pool rows:
`inventory.Report` carries tiles with `phase`, `pod_ip`, `claim`, `cpu_millicores` and a
single `screenshot_ts` (`inventory.py:192-206`).

A run of `session-lab` produces sessions, and note 15 fixes what a session is at the
hardware level: **a slot is `(instance, browserContextId)`**, and one Chromium carries up
to eight of them. A claim is therefore not a session, and a tile is not a session. The
console's run view is a wall of slots.

## 2. The three tiers, and which of them exist today

| Tier | Identity | Liveness | Where it lives now |
|---|---|---|---|
| bucket row | `sessions.bucket` (`30s`, `2m`, `5m`) | static per run | run SQLite |
| instance group | claim / sandbox name | 100 ms warm, 40–80 s cold | `Tile.claim` in inventory, `sessions.instance` in the run DB |
| slot | `(instance, browserContextId)` → one target | ms | `Target.getTargets` on the pod's `/cdp` |
| session row | `sessions.id` | one hold window | run SQLite, one row per lifecycle |

The join the whole view depends on:

```
run DB          sessions.instance  ─────┐
                                        ├──> instance group
inventory       Tile.claim        ──────┘
                Tile.id ────────────────┘  (sandbox name, the pod the bridge lives in)

browser         Target.getTargets ──────> browserContextId, targetId, url   → slot
```

`Tile.claim` and `sessions.instance` are the same string: the sandbox name that owns the
pod. The console's default `SANDBOX_NAMESPACE=sandbox-demo` and `WARM_POOL=browser-pool`
(`config.py:26,52`) match what `config/session-lab/values.yaml` targets, so a run's
instances are already the pods the console is polling.

The slot tier is the one that has no row in the run DB. `store.go` writes no
`browser_context_id` column, because `--contexts-per-browser` is unbuilt (note 15 §7). Two
consequences for the UI, both handled below: at `--contexts-per-browser 1` the binding is
exact, and above 1 the slot is drawn from the browser's own view of itself while its
session label waits for the run row to name it.

## 3. Can a browser context be read and driven on its own?

Yes, and the distinction matters for where the console's capture code goes.

Chromium gives every page its own **target**. A browser context owns its targets, so a
context holding one page exposes an independently attachable CDP session:

```
Target.getTargets                                → [{targetId, browserContextId, url, type}]
Target.attachToTarget {targetId, flatten:true}   → sessionId
Page.captureScreenshot  {sessionId}              → that slot's frame, and only that slot's
Input.dispatchMouseEvent{sessionId}              → that slot's input
Page.navigate           {sessionId}              → that slot's page
```

Eight contexts is eight frame streams and eight input channels over the single DevTools
port the pod publishes. What Chromium does **not** offer is a DevTools address per context:
there is one endpoint per browser, and all of a browser's targets and contexts are
multiplexed over it as sessions. `Page.startScreencast` follows the same rule and pushes
frames as CDP events rather than returning them, which is the one capability the current
bridge cannot carry.

### 3.1 What the bridge supports today

`apps/browser-sandbox/cdp-bridge.py` is the only way to reach a pod's browser: Chromium
binds DevTools to loopback, the sandbox-router cannot forward a WebSocket, and the bridge
holds the WebSocket itself and republishes plain HTTP on `podIP:9222`.

Read against this note, three properties of that file decide the build order:

| Property | Line | Effect on a slot wall |
|---|---|---|
| payload is `{method, params}`, no `sessionId` field | `cdp-bridge.py:283-301` | every slot command lands on the default page, so all eight slots collapse onto one frame |
| one cached socket, to `pages[0]` | `cdp-bridge.py:212`, `226` | no target selection; browser-level commands are not the documented path |
| events are read and discarded | `cdp-bridge.py:238` | `Page.startScreencast` frames never reach the console; capture has to poll |

Two changes make slots addressable, both small and both in this Python file rather than in
`apps/session-lab/`:

1. accept an optional top-level `sessionId` in the `POST /cdp` body and echo it on the
   outbound message, so a flattened session can be addressed;
2. connect to `/devtools/browser/<id>` instead of `pages[0]`, so `Target.*` is the
   documented route rather than a page session's tolerated one.

`Target.getTargets` is a plain method call through the current bridge, which is why slot
discovery ships before slot capture.

## 4. The wall

Four zooms of one object. The rack axis is the bucket, fixed for the run
(`sessions.bucket`, chosen at run open, never regrouped mid-run), and inside a bucket the
sessions are grouped by instance. Seats never move.

### 4.1 Run overview: the skyline

```
╔═════════════════════════════════════════════════════════════════════════════════════════════╗
║ ▉ sandbox-console            Pool │ [Run]           run 20261029T194543Z        ● live   ║
╠═════════════════════════════════════════════════════════════════════════════════════════════╣
║ 240 sessions   40 instances   6 ctx/browser   176 slots lit peak   41 waiting   04:12/12:00 ║
║ ◀─────────────────────●───────────────────────────────────────────────────▶  follow head   ║
╠═════════════════════════════════════════════════════════════════════════════════════════════╣
║ every slot, left to right, in session-index order                                          ║
║ █████████████████████████████████████████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  ║
║ 0         30        60        90        120       150       180       210       240          ║
╚═════════════════════════════════════════════════════════════════════════════════════════════╝
```

The skyline is pinned and always visible. Its shape is the answer to the capacity question
before any numeral is read: a long lit region with a grey tail says the platform is
delivering, and a grey region that grows faster than the lit one says delivery is the
boundary. `176 slots lit` is the concurrency figure, `41 waiting` is admitted sessions with
no slot, and both are counted from the same scan that draws the glyphs.

### 4.2 Wall: buckets, instances, slots

```
 30s bucket ───────────────────────────────────── 92 sessions · 21 instances · p50 hold 30.1s

 ┌ browser-7k2 ──────────────────┐ ┌ browser-p4m ──────────────────┐ ┌ browser-x8f ────────────
 │ ██ ██ ██ ░░ ██ ·· ·· ·· ··   │ │ ██ ██ ██ ██ ██ ██ ██ ██      │ │ ██ ░░ ░░ ░░ ·· ·· ·· ·
 │ 5/8 lit   q 1.2s   cpu 2.31   │ │ 8/8 lit   q 0.4s   cpu 3.90   │ │ 1/8 lit   q 8.7s  cpu 0.9
 └───────────────────────────────┘ └───────────────────────────────┘ └───────────────────────
 ┌ browser-k2v ──────────────────┐ ┌ browser-r9c ──────────────────┐ ┌ browser-t3w ────────────
 │ ██ ██ ·· ·· ·· ·· ·· ··      │ │ ## ## ## ░░ ░░ ·· ·· ·· ··   │ │ ██ ██ ██ ██ ██ ██ ██ ██
 │ 2/8 lit   q 0.2s   cpu 0.77   │ │ 0/8 lit   crashed 11:41:02    │ │ 8/8 lit   q 0.1s  cpu 3.1
 └───────────────────────────────┘ └───────────────────────────────┘ └───────────────────────

 2m bucket ───────────────────────────────────── 60 sessions · 10 instances · p50 hold 118.4s
 ...
```

Glyphs, one character pair per slot position in the browser:

| Glyph | State | Drawn from |
|---|---|---|
| `██` | rendering, frame younger than 2× cadence | `Page.captureScreenshot` reply timestamp |
| `▓▓` | slot alive, frame stale (browser busy or blocked) | target in `Target.getTargets`, no recent frame |
| `░░` | session assigned to this instance, no context yet | run row with `ready_ns` set, no target found |
| `··` | free slot: the browser can take another context | context count below `--contexts-per-browser` |
| `##` | context lost or target crashed | `Target.targetCrashed`, or target vanished mid-hold |
| `⊗` | hole: the session never got an instance | run row with no `ready_ns`, `outcome=waiting` |
| `▪`  | stub: the session ended, the seat keeps its outcome tone | run row terminal |

`··` is the glyph that makes this a capacity instrument. Idle slot positions are drawn
rather than omitted, so headroom and scarcity are both visible at the same instant, and a
near-monotonic wall of `██` with no `··` predicts the queue before the queue forms.

### 4.3 Lane zoom: one instance's eight tracks

A browser is a machine with eight tracks; each track carries sessions one after another,
because a context is created per session and disposed at release (note 15 §3). Clicking an
instance group opens its tracks on one shared clock. This is where the multiplicity the
wall compresses becomes legible.

```
 browser-7k2 · sandbox browser-7k2-8h4x · pod 10.42.0.7 · fedora · 6/8 tracks busy
 cpu 2.31 cores   mem 1.94 GiB   contexts 6   q p50 1.2s

                        0s        30s       60s       90s      120s
                        ├─────────┼─────────┼─────────┼────────┤
 track 0  ✓ s112  ██████████████│s118 ████████│s124 ████████████████│·····
 track 1  ✓ s113  ██████████████████████│s119 ██████│s125 ░░░░░░░░░░░░░░░ waiting
 track 2  ✓ s114  ████████│s120 ████████████████████│s126 ████████████│·····
 track 3  ✓ s115  ██████████████████│s121 ██████████████│·····  ████ s130
 track 4  ░░ s116  ················ctx create······················  waiting 41.8s
 track 5  ·· free
 track 6  ·· free
 track 7  ## crashed 11:41:02   s117 lost, session-lab rebound s131 to browser-t3w
```

Bars are the hold window measured by the run (`hold_start_ns`→`hold_end_ns`), the `│` marks
are release plus next claim, and the clock is shared with the overview scrubber so the two
views agree about "now". Every slot card is reachable from its track.

### 4.4 Slot zoom: one session in one context

```
 /runs/20261029T194543Z/slot/browser-7k2/9f3c1b2e

┌──────────────────────────────────────────────────────────────────────────────────────────┐
│ s112  30s  policy reuse  ✓ success          browser-7k2 · ctx 9f3c1b2e · target 4A1C     │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ frames while the context lived          click a frame → full size, then drive it  ● 0.5fps│
│ ┌────┐┌────┐┌────┐┌────┐┌────┐┌────┐┌────┐┌────┐┌────┐                                    │
│ │ ░░ ││ ░█ ││ ██ ││ ██ ││ ██ ││ ██ ││ ██ ││ ██ ││ ◻  │   ◻ = the frame being captured     │
│ └────┘└────┘└────┘└────┘└────┘└────┘└────┘└────┘└────┘                                    │
│ 0.0s  3.7s  7.5s 11.2s 15.0s 18.7s 22.5s 26.2s 29.9s                                     │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ life  arrive┤admit┤claim│ready├────────── hold 30.1s ──────────┤dispose┤gone          ✓  │
│         0ms  1.2s  150ms                                    40ms  3ms                      │
│ p50    ░░░░░░░░░░░░▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░░░░░░░  run-wide ghost baseline      │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ cpu 0.38 core·s (shared: pod 2.31 ÷ 6)   mem 412 MiB peak (shared)   throttled 0   oom 0   │
│ page about:blank   [ edit url ]  [ click ]  [ type ]  [ scroll ]   ⚠ touched 14:52:07     │
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

The per-slot CPU and memory figures are the pod's numbers divided by the contexts in use,
labelled `shared` on the card. The cgroup cannot see inside a slot, and note 15 §4 records
the same caveat for the report. A card that printed them without the division would look
like a per-session measurement.

## 5. Interaction

A slot is drivable with the routes that already exist. `POST /sandboxes/{id}/click` takes the
sandbox name, and a slot's instance is exactly that name; the slot route resolves
`(instance, browser_context)` to a target and forwards the same CDP method now carrying a
`sessionId`. Existing behaviour stays intact by adding optional query fields rather than new
endpoint shapes:

```
POST /sandboxes/{id}/click?browser_context=9f3c1b2e
POST /sandboxes/{id}/url?browser_context=9f3c1b2e
GET  /sandboxes/{id}/shot?browser_context=9f3c1b2e&v=<ts>
```

Without the query field each route behaves as it does today, against `pages[0]`, so the Pool
mode and every existing caller keep their meaning and `/api/sandboxes*` is untouched.

Interaction is allowed at any point in a live run, including a measured one, and the written
input is recorded against the session:

- the slot card, its wall glyph and its track gain a `touched` mark with the time of the last
  input;
- the mark lives in the console's own capture record, keyed `run_id` / `session` /
  `browser_context_id`, and is never written into the run's SQLite;
- a frame captured after operator input is flagged in the filmstrip, so a CPU or memory
  excursion in the run report has an explanation available beside it.

Two lifetime limits follow from the design and are drawn rather than explained away. A slot's
drivability ends when its context is disposed, which is at release; the filmstrip remains as
the record. In replay of a finished run every slot is read-only, because nothing is behind it.

## 6. Data: the run SQLite, read-only

The rack is fed from `docs/session-lab-runs/<ts>.db`, opened `mode=ro`. This works live
because `store.go` commits one statement per transition with `PRAGMA journal_mode=WAL` and
`synchronous=FULL`, so a run in flight already carries every row it has reached. The sibling
`<ts>.json` appears only at the end and is read for the report panel; a `.db` with no `.json`
is a run that died, and its wall still renders.

Discovery is directory polling: a `.db` that was not present at startup, or whose
`started_ns` is newer than the newest known run, is a run opening. The chart gains one
values key for the host path:

```yaml
# apps/sandbox-console/chart/values.yaml
runs:
  # Populated by a hostPath volume; the console reads it mode=ro and never writes.
  hostPath: /home/dev/Documentos/git_repos/experiments/docs/session-lab-runs
```

Prometheus (`192.168.0.246`) is not a source for this view. `session_lab_*` carries no session
identity and no pod mapping, so it cannot place a slot; it is the right source for a
cross-run aggregate panel later, and needs the scrape job that note 11 records as missing.

## 7. What breaks

| Failure | Where it shows | Mitigation |
|---|---|---|
| one bridge lock serialises all eight slots | frames lag uniformly across a browser | capture budget per slot: 0.5 fps at wall zoom, higher for the slot in focus; a stale glyph at 2× cadence |
| a stalled browser blocks its group | `▓▓` across all eight glyphs | `COMMAND_TIMEOUT` is 20 s (`cdp-bridge.py:38`); the bridge drops the socket on timeout and the card keeps the last frame with its timestamp |
| browser crash takes eight sessions | `##` on all eight, tracks marked lost | note 15 §3's `Target.targetCrashed` watchdog and rebinding; the wall is the evidence the rebind happened |
| contexts per browser above 1 with no context id in the DB | slot glyphs render, session labels are approximate | bind by `instance` + hold window, mark the binding `by time`; exact once the slot id is stored |
| 40 browsers × 8 slots at one cadence | 320 capture calls per tick | capture cycle is a bounded FIFO through `concurrency.py`, the same 429 + `Retry-After` path the existing screenshot route uses |
| renderer memory per slot | 40–80 MiB each (note 15 §4) | slots are the memory metric's real unit; the wall shows pod memory and the context count beside it |

Capture cost, measured before the wall is built: 8 slots × 0.5 fps = 4 calls/s per bridge;
at 1280×600 JPEG the reply is 30–80 ms of bridge work, so a full browser spends 12–32% of
its bridge duty inside captures, against a session-lab run that issues one `Page.navigate`
per reset on the same lock. That is the number to measure on the cluster before the cadence
is fixed; the figures here are from the protocol and the image size, not from a run.

## 8. Build order

| Slice | Ships | Needs |
|---|---|---|
| 1 | `/runs` + `/runs/{run}`: buckets, instance groups, `░░`/`⊗`/`▪`, `176 slots` counts | SQLite reader, chart hostPath. No bridge change |
| 2 | slot discovery: `Target.getTargets` per instance, `██`/`··` glyphs, context counts | nothing beyond slice 1 |
| 3 | per-slot frames, filmstrip, stale glyph | bridge `sessionId` support (`cdp-bridge.py`) |
| 4 | slot interaction, `touched` marking | slice 3, and `/sandboxes/*?browser_context=` query fields |
| 5 | lane zoom, shared clock, scrubber over a finished run | slices 1–3 |
| 6 | per-slot shared CPU label, crash marks | slice 2, plus the `targetCrashed` event path the bridge does not carry |

Slices 1 and 2 need no change to `apps/browser-sandbox/` and no change to
`apps/session-lab/`, and both are testable with a fixture `.db` under `pytest`
(`apps/sandbox-console/test_render.py` already renders HTML from a fixture `Report`).

## 9. To verify on the cluster

Question 1 is answered. Questions 2 and 3 are not.

```bash
# 1. does a page session see every target, or only its own context?
curl -s -XPOST "$POD_IP:9222/cdp" -H 'X-Sandbox-Port: 9222' \
  -d '{"method":"Target.getTargets"}' | head -c 400
```

Run against `claim-mvvlyivc` (`10.42.0.170`, Chrome/130.0.6723.31) on 2026-10-30:

```
{"result": {"targetInfos": [{"targetId": "8A37AF…", "type": "page",
  "title": "Example Domain", "url": "https://example.com/", "attached": true,
  "browserContextId": "8196E3649178E8DBF83DA9B05701977E"}]}}
real 0m0,043s
```

A page session does see the browser's targets, `browserContextId` comes back on the
target, and the round trip is 43 ms through the in-pod bridge. That is why slot
discovery shipped without a bridge change — `Target.getTargets` is the only call the
wall needs for occupancy.

```bash
# 2. does creating a context from a page session work, and what does it cost?
curl -s -XPOST "$POD_IP:9222/cdp" -H 'X-Sandbox-Port: 9222' \
  -d '{"method":"Target.createBrowserContext"}'

# 3. capture cost per slot at wall-zoom size, eight at once
curl -s -XPOST "$POD_IP:9222/cdp" -H 'X-Sandbox-Port: 9222' \
  -d '{"method":"Page.captureScreenshot","params":{"format":"jpeg","quality":45}}'
```

Question 2 decides whether `Target.*` on the current page socket is the path, or whether
connecting to `/devtools/browser/<id>` is required. Question 3 fixes the capture cadence.

## 10. What shipped in this pass

Slices 1 and 2 of §8, with no change to `apps/session-lab/` and no change to any existing
route or `/api/sandboxes*` response.

| File | What it does |
|---|---|
| `apps/sandbox-console/runs.py` | Reads a run database `mode=ro` (copying the `.db`/`-wal`/`-shm` trio to a temp directory when the WAL cannot be joined), selects only the columns its `Session` dataclass declares so an older database still renders, and groups bucket → instance → slot |
| `apps/sandbox-console/render_runs.py` | The four zooms: the skyline strip, the bucket/instance/slot rack, one instance's tracks, and the slot card's counters. Own CSS on top of `BASE_CSS`, and the live refresh that swaps `#wall-inner` |
| `apps/sandbox-console/server.py` | `GET /runs`, `GET /runs/{run_id}`, `GET /runs/{run_id}/wall`, `GET /api/runs`, `GET /api/runs/{run_id}` |
| `apps/sandbox-console/render.py` | The Pool/Run switch, and its rules in `BASE_CSS` so both pages share it |
| `apps/sandbox-console/chart/` | `runs.{enabled,hostPath,mountPath,contextsPerBrowser,maxSessions,refreshSeconds}`, four env vars, and a read-only `hostPath` mount at `/runs` |
| `apps/sandbox-console/test_runs.py` | 30 tests over the reader, the renderer and the five routes, against a fixture database carrying `store.go`'s real columns and a pre-shared-browser variant |

The proof: `python -m pytest -q` in `apps/sandbox-console` — 208 passed. The chart proof:
`helm template sandbox-console apps/sandbox-console/chart` renders the env vars, the read-only
mount and the hostPath volume.

Three things the implementation settled that the design left open:

- **The cut.** A finished run draws its peak instant, because at `ended_ns` nothing is held and
  the rack would be a wall of stubs. A live run draws now. `?at=<ns>`, plus the `◀ peak now end ▶`
  buttons, override it, and every URL is a deep link to an instant.
- **`hole` is the runner's word.** Only `outcome=waiting` is drawn as `⊗`; a `pending` row reads
  as `░░`, because the runner writes `waiting` only for a session still queued when the run ended.
- **A failure outranks the hold window.** A `reset_failed` session keeps `ready_ns` and never
  writes `gone_ns`, so reading timestamps first would draw a failure as a slot still in use. The
  first real database read after this change (`20261030T231708Z`, twelve `reset_failed` rows)
  showed exactly that, and is why the order in `Session.state_at` is what it is.

### Deployed

Built in-cluster with `config/registry/kaniko-build.sh sandbox-console
research/session-lifecycle-elasticity` and rolled with `helm upgrade --install sandbox-console
apps/sandbox-console/chart --namespace sandbox-console` (revision 6). Read back from
`http://192.168.0.250`:

```
GET /runs             200, 22 run rows from the hostPath mount
GET /api/runs         200, 22 runs with stored/instances/outcomes, newest first
GET /runs/20261029T182628Z   200, 12 holding + 1 waiting + 10 free positions at the peak cut
GET /runs/20261029T215138Z   200 with live cluster reads enabled
GET /                the Pool page carries the Run switch
```

The first rollout crashlooped: the image runs the app from `/app`, where
`Path(__file__).parents[2]` is an `IndexError`. `_default_runs_dir` now searches upward for
`docs/session-lab-runs` and falls back to `/runs`, with a test that resolves from both, and
that fix is what revision 6 runs.

## 11. Synthetic sessions: traffic from the console

§9 question 2 is answered, and the answer decided the design: `Target.*` is refused on a page
socket and allowed on the browser socket.

```
POST /cdp {"method": "Target.getBrowserContexts"}   page socket  → "Not allowed"
POST /cdp {"method": "Target.createBrowserContext"} page socket  → "Not allowed"
```

`apps/browser-sandbox/cdp-bridge.py` routes `Target.`, `Browser.` and `SystemInfo.` to
`/devtools/browser/<id>` (`BROWSER_DOMAINS`, commit `7a87a35`), so the console only has to send
the command. The pod that answered "Not allowed" was carrying an image from before that commit;
rebuilding `browser-sandbox` with kaniko and creating a fresh claim made both work, verified on
`claim-mvvnfvnk` (`10.42.0.192`):

```
POST /sandboxes/claim-mvvnfvnk/contexts {"count": 3,
     "urls": ["https://example.com/", "https://example.org/", "https://httpbin.org/html"]}
  → FDF44BB7… example.com, 1DDCFE0F… example.org, 62243702… httpbin.org/html
GET  /sandboxes/claim-mvvnfvnk/contexts
  → three contexts, each with its page, created_by_console: true
```

### What it does

`apps/sandbox-console/traffic.py` treats a session as the slot §2 defined: create a context,
then `Target.createTarget {url, browserContextId}` so the page is populated in one call, which is
why starting traffic needs no `sessionId` and no `Page.navigate`. Eight distinct origins are the
default site list, because same-origin sessions share caches and connections, which is the thing
a per-session context exists to isolate.

The console keeps a per-instance ledger of what it created. `DELETE /sandboxes/{id}/contexts`
disposes only those, so stopping console traffic can never dispose a context session-lab owns;
`?all=1` disposes every non-default context for a clean slate. Contexts the runner created are
listed with `created_by_console: false`.

Starting traffic twice continues through the site list instead of restarting it — the first
implementation restarted at index 0 and gave session four the site of session one.

### Where it is drawn

The Pool tile's dialog carries a sessions block: count, site list, start, stop, and the live list
of contexts with their URLs. The run rack already draws one slot per context from
`Target.getTargets` and labels a group with `N ctx`, so a run whose instances are alive shows the
traffic it is carrying. A claim that belongs to no run has no position in a rack, which is why
the control lives on the sandbox rather than on the wall.

### What traffic still cannot do

Photograph each context. A browser-level `Page.captureScreenshot` shows the page the renderer
composites — the group card's frame — and a per-context frame needs `Target.attachToTarget` plus
a top-level `sessionId` on the bridge's `/cdp` body, which the bridge does not accept yet. That is
slice 3, unchanged by this section: creating contexts did not need it, but seeing each one does.

# RFC — `sandbox-console`: a Pareto pane to inspect, drive and demo agent-sandbox

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-24 |
| Scope | A usecase application over the live `experiments/` cluster. Not an upstream repo change |
| Upstream anchor | `kubernetes-sigs/agent-sandbox` `roadmap.md:113` — **UI Support in OSS** `📅 Planned` |
| Prior art in this repo | `research/platform-cockpit-deep-research-prompt.md` (broader cockpit question, same non-goals) |

---

## 1. Summary

Build one small FastAPI pane that lists live Sandboxes, can create and delete them, and shows a
screenshot of each real Chromium. Four buttons, three outbound links to dashboards the cluster
already runs. It exists to answer two questions in one screen: *"is this actually a browser"* and
*"why can I not reach my cluster from inside it"*.

It does **not** reimplement Grafana, Hubble, Argo CD or Prometheus. It links to them.

## 2. Motivation

### 2.1 Upstream asks for exactly this

`agent-sandbox/roadmap.md` §"Documentation, UI & Community Enablement", whose own heading is
*"Beautiful guides, **interactive tools**, and UI dashboards"*:

```
109: ### 📝 Documentation, UI & Community Enablement
111: Lowering the barrier to entry, beautiful guides, interactive tools, and UI dashboards.
113: *   **UI Support in OSS** `📅 Planned`
114:     *   Build a lightweight open-source web dashboard/UI to visually inspect active
         sandboxes, warmpools, and templates.
```

Ship this and it can be offered upstream as the reference implementation of line 113. That is a
better story than "we built a dashboard": it is a planned roadmap item, implemented.

### 2.2 What a demo currently cannot show

Today the only way to prove the system works is `kubectl` output. That fails the two things that
actually sell:

| Claim | How it is shown today | Why it fails |
|---|---|---|
| It is a real browser | nobody shows it | pixels are the only proof; YAML is not |
| Warm adoption is fast | a latency number | the number is invisible without a side-by-side |
| Sandboxes cannot reach the cluster | a NetworkPolicy YAML | needs to be seen failing, live |
| Sandboxes are disposable | `kubectl delete` | looks like manual cleanup, not a property |

## 3. Existing substrate — what this RFC builds on

Nothing here is invented. Every capability below exists and was read during the writing of this
document.

| Capability | Where it exists | State |
|---|---|---|
| Sandbox / Template / WarmPool / Claim CRDs | `api/`, `extensions/`; installed on this cluster since 2026-10-18 | live |
| Controller, v1.0.7 | `kubectl -n agent-sandbox-system get deploy` | Running |
| Per-sandbox identity + header routing | `sandbox-router/proxy/headers.go:42,79` — `X-Sandbox-ID`, required, 63-char capped at `:137` | live |
| Router auth | `sandbox-router/authz/tokenreview.go:92`, `--authz-mode=tokenreview`, `deploy/rbac-tokenreview.yaml` | live |
| Router deployment | `sandbox-router/deploy/{deployment,service,networkpolicy,pdb}.yaml` | manifests |
| Claim latency + pool size metrics | `internal/metrics/metrics.go:67,84,99,124,145,176` — `agent_sandbox_claim_startup_latency_ms`, `…_controller_…`, `…_client_…`, `agent_sandbox_creation_latency_ms`, `agent_sandbox_claim_creation_total`, `agent_sandbox_warmpool_size` | live |
| Metrics store | VictoriaMetrics `192.168.0.246` | live |
| Dashboards | Grafana `192.168.0.245` | live, no login |
| Flow visibility | Hubble UI `192.168.0.241` | live |
| GitOps state | Argo CD `192.168.0.247`, anonymous read-only | live |
| Capacity test recipes | `dev/load-test/test-recipes/`, selectors `WORKLOAD`, `KWOK_NODES`, `NETWORK_POLICY`, `PROMETHEUS_NODE_SELECTOR` | scripts |
| Reference stack to copy | `experiments/apps/assistant/` — FastAPI + uvicorn single worker + htmx (`server.py:55,343`, `concurrency.py`) | live at `.243` |
| Multi-port headless Services | upstream `#156` (roadmap, completed) — required for `9222` alongside another port | available |

### 3.1 Measured baseline this pane is designed against

From `agent-sandbox/research/experiments/02-capacity-6vcpu-100k.md` and experiment 01, on this node:

| | |
|---|---|
| Concurrent Chromium sandboxes | **~28** (250 MiB each, ~9 GiB free); 60 if the box were dedicated |
| Warm pool that leaves burst headroom | **20–25**; demo shows 12 |
| Claim adoption | 15 claims → all Ready in **1.55 s** |
| Cold start | 41–50 s p50 (upstream benchmark); warm pool is 40 replicas in **31 s** here |
| Node ceilings | 6 vCPU, 15.5 GiB, **110 pods** |
| Sandbox reachability | template-derived pods drop all ingress except from `sandbox-router`; egress allows `0.0.0.0/0`, blocks `10/8` and `169.254.169.254` |

## 4. Goals

1. Show N live Chromium sandboxes at once, each provably a real browser.
2. Create and delete a sandbox/claim from the pane.
3. Show, live, that a sandbox cannot reach the cluster.
4. Surface per-sandbox memory and state without writing a metrics backend.
5. Be small enough that one person maintains it.
6. Show Claim vs bare Sandbox side by side, with stage timers and reachability, in one screen.

## 5. Non-goals

| Rejected | Why |
|---|---|
| Replacing Grafana / Hubble / Argo CD | they are running and better. Link out |
| Traffic splitting | nothing to split. LB-IPAM is one IP per Service, L4 only |
| Auth, RBAC, multi-tenant | LAN posture. Inherits this cluster's known exposures, documented in `experiments/README.md` §Limits |
| A workflow engine, chaos actions, service-connection manager | that is `platform-cockpit-deep-research-prompt.md`, a separate question |
| Editing templates | GitOps owns config; a pane that edits templates is a second reconciler |
| Video streaming (screencast/WebRTC) | one screenshot per tap is enough and is ~30 lines |
| Reimplementing the load-test harness | the recipes exist; the pane shells them |

## 6. Design

### 6.1 One screen

```
┌─ sandbox-console ─────────────────────────── 12 running · 0 pending ─┐
│ [+ claim]  [+ sandbox]  [run recipe ▾]        mem 2.8 / 9.0 GiB      │
├──────────────────────────────────────────────────────────────────────┤
│ ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐          │
│ │ [screenshot]│ │ [screenshot]│ │ [screenshot]│ │ [screenshot]│      │
│ │ sb-4f2a    │ │ sb-8c1b    │ │ sb-2e9d    │ │ sb-7a03    │          │
│ │ ● ready 2m │ │ ● run  41s │ │ ● ready 5m │ │ ● ready 1m │          │
│ └────────────┘ └────────────┘ └────────────┘ └────────────┘          │
│   … 3 rows × 4 columns = 12 tiles …                                  │
├──────────────────────────────────────────────────────────────────────┤
│ isolation  nc 10.43.0.1:443 → ████ hang 4s → BLOCKED                 │
│ grafana ↗   hubble ↗   argo ↗        router 192.168.0.248            │
└──────────────────────────────────────────────────────────────────────┘
```

12 tiles = 4×3. Matches the demo warm pool. 28 is possible but unreadable and makes the node lag.
Tile viewport 1280×600 → ~180 MiB per browser at that size, so the demo costs ~2.2 GiB.

### 6.2 Tiles are screenshots, not streams

Click a tile → `Input.dispatchMouseEvent` → `Page.captureScreenshot` → swap the `<img>`.

Why not VNC: VNC needs an X server, which forces non-headless Chromium, +150–200 MiB per pod and
a compositor. Headless Chrome cannot paint to a display, so VNC buys nothing a screenshot does not
already give.

Why not screencast: smooth frames matter for a video; a tap-response loop does not need them.

Why not iframes on the target site: `X-Frame-Options: DENY` and CSP `frame-ancestors` block it.
You iframe your own viewer, never the remote page.

Proof, not mock: the same browser the agent drives. `Runtime.evaluate("location.href")` on demand
gives a text answer that a fake cannot produce.

### 6.3 Expanded tile

Click → modal, image scaled up. **Viewport stays 1280×600**; divide incoming click coordinates by
the scale factor. Never resize Chrome mid-demo — the page relayouts and the demo wobbles.

### 6.4 Typing

Focus lives in the browser, not in the UI. Click the field (real `dispatchMouseEvent`), then
`Input.insertText`. Two calls, no synthetic key events needed for a demo.

### 6.5 Compare mode — Claim vs Bare, split screen, with timers

The single strongest screen in the deck. Two columns, same image, same URL, same moment.
Left is a Claim from the warm pool. Right is a bare `kind: Sandbox`.

```
┌─ compare ─────────────── same image · same URL · fired together ─┐
│  CLAIM (warm pool)          │  BARE SANDBOX                      │
│  ┌───────────────────────┐  │  ┌───────────────────────────────┐ │
│  │ [screenshot]          │  │  │ [screenshot]                  │ │
│  └───────────────────────┘  │  └───────────────────────────────┘ │
│  ready  ██ 1.55 s  ✓        │  ready  ███████████████████ 43.2 s │
│  cdp    ██ 1.61 s           │  cdp    █████████████████████ 44.0 │
│  paint  ██ 1.72 s           │  paint  ██████████████████████ 45.1│
│                             │                                   │
│  ● cluster BLOCKED          │  ▲ cluster REACHABLE              │
│  ● metadata BLOCKED         │  ▲ SA token mounted               │
│  ● token not mounted        │  ▲ cluster DNS resolves           │
└───────────────────────────────────────────────────────────────────┘
```

Two truths side by side. **Time** on the left of each column, **reachability** below it. The
timing gap is the warm-pool pitch; the reachability gap is the isolation pitch. One screen, both.

#### The four timers

Every creation records four stamps, all in the same clock, all measured server-side:

| Timer | Start | Stop | What it proves |
|---|---|---|---|
| `t_sched` | POST `/sandboxes` | pod is `Scheduled` | scheduler and capacity |
| `t_ready` | POST `/sandboxes` | `Sandbox` condition `Ready=True` | the number the product quotes |
| `t_cdp` | POST `/sandboxes` | `GET :9222/json/version` answers | the browser is actually up |
| `t_paint` | POST `/sandboxes` | first screenshot whose bytes > a blank-page floor | a real page rendered |

`t_sched → t_ready` is pod start; `t_ready → t_cdp` is Chromium boot; `t_cdp → t_paint` is
navigation. That split is what makes the timer useful — a slow number tells you *which* stage.

Bars are drawn to a shared scale across both columns, so the bare sandbox's 43 s makes the claim's
1.55 s a stub. Do not use separate scales; that hides the point.

#### Method, so the comparison is fair

1. Pre-pull the image on the node first. A cold pull would land inside `t_sched` and poison the
   comparison by 30–120 s. Warm it with one throwaway sandbox, then run compare.
2. Fire both creations within the same second. Record wall-clock offsets, not just durations.
3. Same viewport (1280×600), same target URL, same concurrency.
4. Run three times; show the last run and the p50 of all three on hover.
5. Keep the reachability probes out of the timers — they run after `t_paint`.

The bare column needs `--allow-raw-sandbox` enabled (§14.1). Compare mode is the one feature that
requires it, and the only place a bare Sandbox is ever created.

## 7. Data model

One JSON object per sandbox, assembled server-side from CRD status (no new storage):

```json
{
  "id": "sb-4f2a",
  "phase": "ready",
  "age_s": 134,
  "pod_ip": "10.42.0.36",
  "node": "fedora",
  "template": "browser-tpl",
  "pool": "browser-pool",
  "claim": "demo-3",
  "mem_working_set_bytes": 262144000,
  "cpu_millicores": 42,
  "chromium_ready": true,
  "cdp_ok": true,
  "egress_public_ok": true,
  "ingress_cluster_blocked": true,
  "screenshot_ts": 1761,
  "shutdown_at": "2026-10-24T17:20:00Z",
  "created_by": "claim",
  "timers": {
    "t_sched_ms": 1520,
    "t_ready_ms": 1553,
    "t_cdp_ms": 1610,
    "t_paint_ms": 1724
  },
  "reachability": {
    "cluster": "blocked",
    "metadata": "blocked",
    "sa_token": "absent",
    "cluster_dns": "unresolvable"
  }
}
```

| Field | Source |
|---|---|
| `phase`, `age_s`, `pod_ip`, `shutdown_at` | `Sandbox.status` — `conditions[]`, `podIPs[]` |
| `template`, `pool`, `claim` | pod labels (`sandbox-template-ref-hash`, `warm-pool-sandbox`, `agents.x-k8s.io/sandbox-name-hash`) |
| `mem_working_set_bytes`, `cpu_millicores` | `kubectl top pod` via metrics-server, or a PromQL call to VictoriaMetrics |
| `chromium_ready` | `GET /json/version` on `:9222` through the router |
| `egress_public_ok` | one cached probe per sandbox: TCP to `1.1.1.1:443` |
| `ingress_cluster_blocked` | one cached probe: TCP to `10.43.0.1:443`, 4 s timeout, expect timeout |
| `created_by` | `claim` or `sandbox` — set by the create call, drives compare mode |
| `timers.*` | stamped server-side at four events; see §6.5 |
| `reachability.*` | same probe set as the badges, run once after `t_paint` |

The two isolation booleans are the sales payload. Probe once on sandbox creation, cache, re-run on
demand from a button.

## 8. API surface

Seven endpoints. htmx fragments, no JavaScript framework.

| Method | Path | Does |
|---|---|---|
| GET | `/` | the grid + header counters |
| GET | `/sandboxes` | the grid fragment (htmx poll, 3 s) |
| GET | `/sandboxes/{id}/shot` | PNG, `Page.captureScreenshot` through the router |
| POST | `/sandboxes/{id}/click` | `{x, y, scale}` → `dispatchMouseEvent` → returns new shot |
| POST | `/compare` | creates one Claim and one bare Sandbox together, returns both tiles with timers |
| GET | `/compare` | the split-screen view, last run + p50 of the last three |
| POST | `/sandboxes` | create a Claim (or a bare Sandbox); returns the new tile |
| DELETE | `/sandboxes/{id}` | delete the Claim/Sandbox, foreground |
| POST | `/recipes/{name}` | shell an existing `dev/load-test/test-recipes/` script, stream stdout |

Reuse the assistant's shape: single uvicorn worker, bounded queue, `429 + Retry-After` instead of
unbounded concurrency (`experiments/apps/assistant/concurrency.py` is the template).

## 9. Exposure

| | |
|---|---|
| LB-IPAM pool | `192.168.0.240–250`. **8 used**: `.240` cilium-ingress, `.241` hubble, `.242` neo4j, `.243` assistant, `.244` fuwa, `.245` grafana, `.246` victoriametrics, `.247` argocd |
| Free | `.248`, `.249`, `.250` — **3 addresses** |
| Allocation | one LoadBalancer Service for `sandbox-router` at **`192.168.0.248`**; the console also gets a Service (`.249`) or rides the router |
| Per-sandbox IPs | **not possible.** 12 sandboxes into 3 free addresses, and a Service per ephemeral pod churns LB-IPAM and L2 ARP announcements on every create/delete |
| Routing | console → `192.168.0.248` with `X-Sandbox-ID: sb-4f2a` → router → `pod:9222` |
| Why not pod IPs | `10.42.x` is tunnelled, not LAN-routable, and ingress to template-derived sandboxes is default-deny except from the router |

**Websocket caveat:** unused if tiles are screenshots. If a later change adds long-lived
connections, Cilium's idle timeout must be raised and keepalives sent every ~30 s, or tiles die
silently after a minute.

## 10. Actions, blast radius, audit

| Action | Blast radius | Guard |
|---|---|---|
| Create claim | +250 MiB, +1 pod slot | refuse above a configured ceiling (28 here), show the counter |
| Delete | the sandbox only | foreground delete; the tile disappears when the pod is gone |
| Click / type | that sandbox's page | coordinates clamped to the viewport |
| Run recipe | **cluster-wide** | confirm dialog naming the recipe and its selectors; refuses while another recipe is running |
| Nothing else | — | no template edits, no pool scale, no policy changes |

Every mutation appends one line to a local JSONL audit file: timestamp, action, target, actor
header, result. No database.

## 11. The Pareto cut

Build four things. Link three. Build nothing else.

| | Decision |
|---|---|
| Inventory + state + IP | **build** — `kubectl` cannot do this at a glance, and "at scale" is the ask |
| Create / delete | **build** — the interactive part |
| Screenshot per tile + click | **build** — the only proof it is a real browser |
| Isolation badge | **build** — the only thing here that actually sells |
| Memory, CPU, pool size over time | **link** to Grafana `.245` (VictoriaMetrics already scrapes `agent_sandbox_*`) |
| Flows, policy verdicts | **link** to Hubble `.241` |
| What is deployed and why | **link** to Argo `.247` |
| Traffic split | **do not build** — the primitives exist (Cilium backend weights, Gateway API weighted `backendRefs`, `CiliumEnvoyConfig`) but at the wrong layer: sandboxes are distinct identities, not interchangeable replicas. Canarying a *browser image* is an allocation decision at claim time, not a network one. See `rfc-traffic-split.md` |
| Auth / tenancy | **do not build** — LAN posture, stated in `experiments/README.md` §Limits |
| Metrics pipeline of our own | **do not build** — reuse VictoriaMetrics |

## 12. Milestones

| # | Milestone | Acceptance test |
|---|---|---|
| M1 | Read-only grid | `GET /sandboxes` returns every sandbox in the namespace with `phase`, `age_s`, `pod_ip`; a created sandbox appears within one 3 s poll; deleting it removes the tile |
| M2 | Proof of browser | `GET /sandboxes/{id}/shot` returns a valid PNG of the page; a known prompt page reads back through `Runtime.evaluate("document.title")`; clicking a link changes the screenshot |
| M3 | Isolation badge | for a template-derived sandbox, `ingress_cluster_blocked` is `true` and `egress_public_ok` is `true`; both flips are visible in the UI within 4 s, and the badge shows the actual error (`timeout`, not `refused`) |
| M4 | Lifecycle from the pane | `POST /sandboxes` creates a Claim that reaches Ready; `DELETE` removes the pod; the pod count returns to baseline with no orphan |
| M5 | Recipe runner | `POST /recipes/capacity-cliff` runs the existing script and streams its output; a second concurrent request is refused |
| M6 | Compare mode | `POST /compare` creates one Claim and one bare Sandbox within 1 s of each other; both record all four timers; the Claim's `t_ready` is under 5 s and the bare one's over 30 s; the reachability panel differs on `cluster` and `sa_token`; bars share one scale |
| M7 | Demo run | 12 tiles, all live; one tap per tile proves a real browser; `BLOCKED` badge visible; compare mode shown once; full teardown leaves the cluster at its recorded baseline |

## 13. Risks

| Risk | Mitigation |
|---|---|
| A pane that mutates becomes a second reconciler and drifts from GitOps | only Claims and Sandboxes are mutable; Templates, WarmPools and policies stay GitOps-only |
| 12 Chromium instances make this desktop lag during a demo | 1280×600 viewports, 12 tiles max, screenshots on demand rather than on a timer |
| Screenshot latency reads as "slow" to a viewer | show the round-trip time on the tile; 30–150 ms over a LAN is the expected number |
| The demo is mistaken for a product | the README states the non-goals and the LAN posture |
| Upstream declines the UI | it stays a usecase app in `experiments/apps/`; the value is the demo, not the merge |

## 14. Open questions

1. **Create a Claim or a bare Sandbox from the pane?** A Claim inherits pool warmth and the
   hardened defaults; a bare Sandbox is the unhardened path (no NetworkPolicy, cluster DNS, mounted
   token — see experiment 01). Recommend Claim-only, with bare Sandbox behind a flag used once, on
   purpose, to show the difference.
2. **Does the console ride the router or get its own LB IP?** Its own IP (`.249`) is clearer for a
   demo and costs one of three free addresses.
3. **Where does the screenshot proxy live?** In the console process (simplest, 30 lines) or a
   sidecar. Recommend in-process; a sidecar doubles the deploy surface for no gain.
4. **Is compare mode a demo feature or a real diagnostic?** It started as a demo. It is also the
   cheapest regression test for warm-pool health: if `t_ready` for a Claim climbs toward the bare
   sandbox's, the pool is drained or the refill gate is stuck. Recommend keeping it as a diagnostic
   and running it before every demo.
5. **Does the canary work belong in this RFC?** No. Traffic and allocation splitting is
   `rfc-traffic-split.md`, kept separate because it is experimental and this console does not need
   it.

## Appendix — numbers used, and where they came from

| Number | Source |
|---|---|
| 250 MiB per Chromium sandbox | `agent-sandbox/examples/gke-swap/README.md` (measured) |
| ~28 concurrent on this node | memory-bound from ~9 GiB free; `research/experiments/02` |
| 15 claims in 1.55 s, 40-replica pool in 31 s | `research/experiments/02`, measured on this node |
| 41–50 s p50 cold start | `agent-sandbox/docs/performance-tuning.md` (upstream benchmark) |
| 110 pods, 6 vCPU, 15.5 GiB | `kubectl get node fedora -o jsonpath='{.status.allocatable}'` |
| 8 of 11 LB addresses in use | `kubectl get svc -A -o wide` |
| default-deny is template-only | `research/experiments/01-k3s-sandbox-demo.md` §H4 caveat |
